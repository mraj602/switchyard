package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

type fakePuller struct {
	calls []string
	err   error
}

func (f *fakePuller) RunNow(ctx context.Context, id string) (api.PullStatus, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return api.PullStatus{}, f.err
	}
	now := "2026-10-12T09:00:00.000Z"
	return api.PullStatus{Name: "gpu-box", Target: "me@gpu-box", IntervalS: 60, State: api.PullOK, NextAt: &now}, nil
}

// startHubWith is startHub with a Puller and a public URL.
func startHubWith(t *testing.T, p api.Puller) *hub {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := api.Config{Store: st, PingEvery: time.Hour, PublicURL: "https://hub.example.ts.net/"}
	if p != nil {
		cfg.Puller = p
	}
	srv := api.New(cfg)
	h := &hub{t: t, st: st, srv: srv, http: httptest.NewServer(srv.Handler()), tokens: map[string]string{}}
	t.Cleanup(h.stop)
	return h
}

// ownerToken is an owner session opened from a desktop device of h.machine.
func ownerToken(t *testing.T, h *hub) string {
	t.Helper()
	if h.machine == "" {
		h.machine = newMachineID()
	}
	d, err := auth.Create(context.Background(), h.st, auth.CreateRequest{Kind: "desktop", Name: "laptop", Machine: h.machine})
	if err != nil {
		t.Fatal(err)
	}
	return ownerSessionBy(t, h.st, "login:"+d.Device.ID).Token
}

func machineInfoRec(t *testing.T, machine string) []byte {
	t.Helper()
	r, err := record.NewMachineInfo(record.Meta{Machine: machine, Client: "wy/0.5.0", Now: time.Now()}, record.MachineInfoPayload{
		OS: "linux", Arch: "arm64", Sy: "0.5.0", RecordV: 1, Reach: record.ReachForward,
		DirectPush: record.DirectPushState{Enabled: true, Private: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

func TestMachinesRenameRemoveAll(t *testing.T) {
	h := startHub(t, "")
	owner := ownerToken(t, h)
	other := startHubDevice(t, h, "machine")
	if rep := h.ingest(other.token, machineInfoRec(t, other.machine)); len(rep.Accepted) != 1 || rep.Hub == nil {
		t.Fatalf("%+v", rep)
	}
	code, body := h.do("GET", "/v1/machines", owner, nil)
	var list struct {
		Machines []map[string]any `json:"machines"`
	}
	if code != 200 || json.Unmarshal(body, &list) != nil {
		t.Fatalf("%d %s", code, body)
	}
	var m map[string]any
	for _, x := range list.Machines {
		if x["id"] == other.machine {
			m = x
		}
	}
	if m == nil || m["reach"] != "forward" || m["os"] != "linux" || m["direct_push"] != true || m["held"] != float64(0) || m["version_status"] == nil {
		t.Fatalf("machine read model: %s", body)
	}

	for _, c := range []struct {
		body string
		want int
	}{{`{"name": ""}`, 400}, {`{"name": "` + string(make([]byte, 61)) + `"}`, 400}, {`{"nom": "x"}`, 400}, {`[]`, 400}} {
		if code, b := h.do("PATCH", "/v1/machines/"+other.machine, owner, []byte(c.body)); code != c.want {
			t.Errorf("PATCH %s: %d %s", c.body, code, b)
		}
	}
	if code, b := h.do("PATCH", "/v1/machines/m_nope", owner, []byte(`{"name":"x"}`)); code != 404 {
		t.Errorf("PATCH unknown: %d %s", code, b)
	}
	code, body = h.do("PATCH", "/v1/machines/"+other.machine, owner, []byte(`{"name":"  big box "}`))
	if code != 200 || json.Unmarshal(body, &m) != nil || m["name"] != "big box" {
		t.Fatalf("rename: %d %s", code, body)
	}

	// The caller's own machine cannot be removed; an unknown one is 404.
	if code, b := h.do("DELETE", "/v1/machines/"+h.machine, owner, nil); code != 409 {
		t.Errorf("DELETE own machine: %d %s", code, b)
	}
	if code, b := h.do("DELETE", "/v1/machines/m_nope", owner, nil); code != 404 {
		t.Errorf("DELETE unknown: %d %s", code, b)
	}
	if code, b := h.do("DELETE", "/v1/machines/"+other.machine, owner, nil); code != 204 {
		t.Fatalf("DELETE: %d %s", code, b)
	}
	// Its token is revoked.
	if code, _ := h.do("POST", "/v1/ingest", other.token, batch(machineInfoRec(t, other.machine))); code != 401 {
		t.Errorf("removed machine's token still works: %d", code)
	}
	_, body = h.do("GET", "/v1/machines", owner, nil)
	if json.Unmarshal(body, &list) != nil || len(list.Machines) != 1 {
		t.Fatalf("removed machine listed: %s", body)
	}
	_, body = h.do("GET", "/v1/machines?all=true", owner, nil)
	if json.Unmarshal(body, &list) != nil || len(list.Machines) != 2 {
		t.Fatalf("all=true: %s", body)
	}
	if code, _ := h.do("GET", "/v1/machines?all=maybe", owner, nil); code != 400 {
		t.Errorf("all=maybe: %d", code)
	}
	_, body = h.do("GET", "/v1/overview", owner, nil)
	var ov struct {
		Machines []map[string]any `json:"machines"`
	}
	if json.Unmarshal(body, &ov) != nil || len(ov.Machines) != 1 {
		t.Fatalf("overview lists a removed machine: %s", body)
	}
}

func TestMachinePullNow(t *testing.T) {
	h := startHubWith(t, nil)
	owner := ownerToken(t, h)
	if code, b := h.do("POST", "/v1/machines/m_x/pull", owner, nil); code != 503 {
		t.Errorf("no puller: %d %s", code, b)
	}
	p := &fakePuller{}
	h = startHubWith(t, p)
	owner = ownerToken(t, h)
	code, body := h.do("POST", "/v1/machines/m_x/pull", owner, nil)
	var st api.PullStatus
	if code != 202 || json.Unmarshal(body, &st) != nil || st.State != api.PullOK || len(p.calls) != 1 || p.calls[0] != "m_x" {
		t.Fatalf("%d %s %v", code, body, p.calls)
	}
	p.err = api.ErrNoPullTarget
	if code, _ := h.do("POST", "/v1/machines/m_x/pull", owner, nil); code != 404 {
		t.Errorf("no target: %d", code)
	}
	p.err = api.ErrPullBlocked
	if code, _ := h.do("POST", "/v1/machines/m_x/pull", owner, nil); code != 409 {
		t.Errorf("blocked: %d", code)
	}
}

func TestDirectPolicy(t *testing.T) {
	h := startHubWith(t, nil)
	tok := h.token("machine")
	code, body := h.do("GET", "/v1/push/direct-policy", tok, nil)
	var p struct {
		V         int     `json:"v"`
		Hub       string  `json:"hub"`
		PublicURL *string `json:"public_url"`
		Machine   string  `json:"machine"`
		Settings  struct {
			QuietHours struct {
				TZ string `json:"tz"`
			} `json:"quiet_hours"`
			MaxAgeS int `json:"max_age_s"`
		} `json:"settings"`
		Rules []struct {
			Trigger  string   `json:"trigger"`
			DelayS   int      `json:"delay_s"`
			Seats    []any    `json:"seats"`
			Projects []string `json:"projects"`
		} `json:"rules"`
	}
	if code != 200 || json.Unmarshal(body, &p) != nil {
		t.Fatalf("%d %s", code, body)
	}
	if p.V != 1 || p.Hub == "" || p.PublicURL == nil || *p.PublicURL != "https://hub.example.ts.net" || p.Machine != h.machine ||
		p.Settings.MaxAgeS != 900 || p.Settings.QuietHours.TZ == "" || len(p.Rules) != 3 {
		t.Fatalf("%s", body)
	}
	for _, r := range p.Rules {
		if r.Seats == nil || r.Projects == nil {
			t.Fatalf("lists must be [] not null: %s", body)
		}
	}
	// A device without a machine.
	c, err := auth.Create(context.Background(), h.st, auth.CreateRequest{Kind: "browser", Name: "b", Scopes: []string{"write", "read"}})
	if err == nil {
		if code, b := h.do("GET", "/v1/push/direct-policy", c.Token, nil); code != 403 {
			t.Errorf("no machine: %d %s", code, b)
		}
	}
}

// While frozen, ingest answers 503 unavailable with Retry-After: 300 (machines.md 6.2).
func TestIngestFrozenOverHTTP(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		return tx.PutAlertSetting(api.SettingFrozen, []byte(`{"since":1,"moved_to":null}`), 1)
	}); err != nil {
		t.Fatal(err)
	}
	code, body := h.do("POST", "/v1/ingest", tok, batch(machineInfoRec(t, h.machine)), "X-Sy-Backlog-Oldest", "2026-10-12T08:00:00Z")
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &e)
	if code != 503 || e.Code != "unavailable" {
		t.Fatalf("%d %s", code, body)
	}
	resp, err := http.DefaultClient.Do(func() *http.Request {
		r, _ := http.NewRequest("POST", h.http.URL+"/v1/ingest", bytes.NewReader(batch(machineInfoRec(t, h.machine))))
		r.Header.Set("Authorization", "Bearer "+tok)
		return r
	}())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Retry-After") != "300" {
		t.Fatalf("Retry-After %q", resp.Header.Get("Retry-After"))
	}
}

// X-Sy-Backlog-Oldest fills backlog_oldest.
func TestIngestBacklogOldest(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	owner := ownerToken(t, h)
	code, body := h.do("POST", "/v1/ingest", tok, batch(machineInfoRec(t, h.machine)), "X-Sy-Backlog", "900", "X-Sy-Backlog-Oldest", "2026-10-12T08:00:00Z")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	_, body = h.do("GET", "/v1/machines", owner, nil)
	var list struct {
		Machines []struct {
			Backlog       *int64  `json:"backlog"`
			BacklogOldest *string `json:"backlog_oldest"`
			BacklogAt     *string `json:"backlog_at"`
		} `json:"machines"`
	}
	if json.Unmarshal(body, &list) != nil || len(list.Machines) != 1 {
		t.Fatalf("%s", body)
	}
	m := list.Machines[0]
	// 900 queued with this one-record batch: 899 left once it is answered.
	if m.Backlog == nil || *m.Backlog != 899 || m.BacklogOldest == nil || *m.BacklogOldest != "2026-10-12T08:00:00.000Z" || m.BacklogAt == nil {
		t.Fatalf("%s", body)
	}
	// The last batch of a flush (backlog = its own size) leaves nothing queued.
	if code, body := h.do("POST", "/v1/ingest", tok, batch(machineInfoRec(t, h.machine)), "X-Sy-Backlog", "1"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	_, body = h.do("GET", "/v1/machines", owner, nil)
	if json.Unmarshal(body, &list) != nil || len(list.Machines) != 1 || list.Machines[0].Backlog == nil || *list.Machines[0].Backlog != 0 {
		t.Fatalf("after the last batch: %s", body)
	}
}
