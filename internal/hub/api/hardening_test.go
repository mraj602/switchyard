package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

// The clamp keeps a record from a wrong clock from taking over ordering: the stored ts and
// machine.last_record never run more than the tolerance ahead of the hub clock.
func TestFutureTimestampIsClampedForOrdering(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	now := time.Now().UTC()
	h.ingest(tok, simpleHook(t, h.machine, "s-future", "Stop", now.Add(24*time.Hour)))
	limit := now.Add(6 * time.Minute).UnixMilli()
	if n := h.count(`SELECT count(*) FROM record WHERE ts > ?`, limit); n != 0 {
		t.Errorf("%d records ordered more than the tolerance ahead of the hub clock", n)
	}
	var last int64
	if err := h.st.R().QueryRow(`SELECT last_record FROM machine WHERE id = ?`, h.machine).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last > limit {
		t.Errorf("machine.last_record = %d is in the future", last)
	}
	// The raw ts stays in the stored body.
	var body string
	if err := h.st.R().QueryRow(`SELECT body FROM record WHERE kind = 'hook_event'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, now.Add(24*time.Hour).Format("2006-01-02")) {
		t.Errorf("raw ts not kept in body: %s", body)
	}
	// A ts inside the tolerance is not touched.
	h.ingest(tok, simpleHook(t, h.machine, "s-future", "UserPromptSubmit", now.Add(2*time.Minute)))
	if n := h.count(`SELECT count(*) FROM record WHERE ts >= ?`, now.Add(2*time.Minute).UnixMilli()); n != 1 {
		t.Errorf("a ts within the tolerance was clamped (%d records at or after it)", n)
	}
}

func TestRemoteURLOnlyKeptForClaudeAI(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	desktop := h.token("desktop")
	send := func(sid, url string, at time.Time) {
		p, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": "SessionStart", "cwd": "/home/u/proj"})
		r, err := record.NewHookEvent(record.Meta{Machine: h.machine, Slot: "~/.claude", Client: "wy/0.0.0-test", Now: at},
			p, record.HookOptions{})
		if err != nil {
			t.Fatal(err)
		}
		r.Derived = record.Derived{}
		r.Derived.Set("remote_url", url)
		b, _ := r.Encode()
		h.ingest(tok, b)
	}
	send("s-good", "https://claude.ai/code/session_1", t0)
	for i, bad := range []string{"javascript:alert(1)", "data:text/html,x", "http://claude.ai/x", "https://evil.example/https://claude.ai/", "https://claude.ai.evil.example/"} {
		send("s-bad"+string(rune('a'+i)), bad, t0.Add(time.Duration(i+1)*time.Second))
	}
	var s struct {
		RemoteURL *string `json:"remote_url"`
	}
	h.get("/v1/sessions/s-good", desktop, &s)
	if s.RemoteURL == nil || *s.RemoteURL != "https://claude.ai/code/session_1" {
		t.Errorf("good remote_url = %v", s.RemoteURL)
	}
	for i := range 5 {
		s.RemoteURL = nil
		h.get("/v1/sessions/s-bad"+string(rune('a'+i)), desktop, &s)
		if s.RemoteURL != nil {
			t.Errorf("remote_url %q survived", *s.RemoteURL)
		}
	}
}

func TestBadSessionIDIsRejected(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	for _, sid := range []string{"x'; rm -rf ~ #", "$(reboot)", "../../etc", "-rf", strings.Repeat("a", 65)} {
		r := h.ingest(tok, simpleHook(t, h.machine, sid, "SessionStart", t0))
		if len(r.Rejected) != 1 || r.Rejected[0].Reason != "bad_envelope" || len(r.Accepted) != 0 {
			t.Errorf("session id %q: %+v", sid, r)
		}
	}
	if n := h.count(`SELECT count(*) FROM session`); n != 0 {
		t.Errorf("%d sessions created from bad ids", n)
	}
}

func TestBoardSecurityHeaders(t *testing.T) {
	h := startHub(t, "")
	resp, err := http.Get(h.http.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'", "img-src 'self' data:"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	// script-src may only ever be 'self' (no inline, eval or other origins).
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") && d != "script-src 'self'" {
			t.Errorf("CSP must not loosen scripts: %s", csp)
		}
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not loosen scripts: %s", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers %v", resp.Header)
	}
}

func TestIngestAnswers403WhenEveryRecordIsForAnotherMachine(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	other := newMachineID()
	code, body := h.do("POST", "/v1/ingest", tok, batch(simpleHook(t, other, "s-other", "SessionStart", t0)))
	var e struct{ Code string }
	_ = json.Unmarshal(body, &e)
	if code != http.StatusForbidden || e.Code != "machine_mismatch" {
		t.Fatalf("%d %s", code, body)
	}
	if n := h.count(`SELECT count(*) FROM record`); n != 0 {
		t.Errorf("%d records stored", n)
	}
	// A mixed batch is still answered per record.
	r := h.ingest(tok, simpleHook(t, other, "s-other", "SessionStart", t0), simpleHook(t, h.machine, "s-mine", "SessionStart", t0))
	if len(r.Accepted) != 1 || len(r.Rejected) != 1 || r.Rejected[0].Reason != "machine_mismatch" {
		t.Errorf("mixed batch: %+v", r)
	}
}

func TestStreamEndsWhenDeviceIsRevoked(t *testing.T) {
	h := startHubPing(t, "", 30*time.Millisecond)
	phone := h.token("phone")
	ch, cancel := openStream(t, h, "", phone, "")
	defer cancel()
	next(t, ch, "hello", 2*time.Second)
	dev, err := h.st.Devices(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dev {
		if d.Kind == "phone" {
			if err := auth.Revoke(context.Background(), h.st, d.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream of a revoked device was not closed")
		}
	}
}

// The board passes overview.stream_id as ?last_event_id= on its first connect, so events
// published between the overview and the subscription are replayed.
func TestStreamLastEventIDQuery(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	phone := h.token("phone")
	var o struct {
		StreamID int64 `json:"stream_id"`
	}
	h.get("/v1/overview", phone, &o)
	h.ingest(tok, simpleHook(t, h.machine, "s-gap", "SessionStart", t0)) // after the overview, before the stream
	ch, cancel := openStream(t, h, "?last_event_id="+strconv.FormatInt(o.StreamID, 10), phone, "")
	defer cancel()
	next(t, ch, "hello", 2*time.Second)
	if e := next(t, ch, "session", 2*time.Second); !strings.Contains(e.data, "s-gap") {
		t.Errorf("missed event not replayed: %+v", e)
	}
}

// clockHub is a hub whose clock the test moves.
func clockHub(t *testing.T, offset *atomic.Int64) *hub {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/hub.db", store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(api.Config{Store: st, WebUI: fstest.MapFS{"index.html": {Data: []byte("x")}}, PingEvery: time.Hour,
		Now: func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }})
	h := &hub{t: t, path: "", st: st, srv: srv, http: httptest.NewServer(srv.Handler()), tokens: map[string]string{}}
	t.Cleanup(h.stop)
	return h
}

func TestBoardSessionExpiresServerSide(t *testing.T) {
	var offset atomic.Int64
	h := clockHub(t, &offset)
	phone := h.token("phone")
	body, _ := json.Marshal(map[string]string{"token": phone})
	resp, err := http.Post(h.http.URL+"/v1/session/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		cookie = c
	}
	if resp.StatusCode != 204 || cookie == nil {
		t.Fatalf("login %d", resp.StatusCode)
	}
	get := func() int {
		req, _ := http.NewRequest("GET", h.http.URL+"/v1/overview", nil)
		req.AddCookie(cookie)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if c := get(); c != 200 {
		t.Fatalf("fresh session: %d", c)
	}
	offset.Store(int64(29 * 24 * time.Hour))
	if c := get(); c != 200 {
		t.Fatalf("session at 29 days: %d", c)
	}
	offset.Store(int64(31 * 24 * time.Hour))
	if c := get(); c != 401 {
		t.Fatalf("session at 31 days: %d, want 401 even though the cookie is still sent", c)
	}
	// The pasted token itself does not expire.
	if code, _ := h.do("GET", "/v1/overview", phone, nil); code != 200 {
		t.Errorf("pasted token: %d", code)
	}
}

func TestLoginRefusedWhenParentRevokedInCreate(t *testing.T) {
	h := startHub(t, "")
	phone := h.token("phone")
	devs, _ := h.st.Devices(context.Background(), false)
	if err := auth.Revoke(context.Background(), h.st, devs[0].ID); err != nil {
		t.Fatal(err)
	}
	_, err := auth.Create(context.Background(), h.st, auth.CreateRequest{Kind: "browser", Name: "board", CreatedBy: "login:" + devs[0].ID,
		Parent: devs[0].ID, Scopes: []string{"read"}})
	if err == nil {
		t.Fatal("a session was created under a revoked device")
	}
	if n := h.count(`SELECT count(*) FROM device WHERE kind = 'browser'`); n != 0 {
		t.Errorf("%d browser devices left behind", n)
	}
	_ = phone
}

// Revoking a device revokes the board sessions opened from it (login:<id>), so their open
// streams end at once too, not at the next ping.
func TestStreamOfBoardSessionEndsWhenItsDeviceIsRevoked(t *testing.T) {
	h := startHub(t, "") // pings every hour: only the kick can end the stream in time
	ctx := context.Background()
	desk, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "desktop", Name: "laptop", Machine: newMachineID()})
	if err != nil {
		t.Fatal(err)
	}
	board, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "browser", Name: "board via laptop",
		CreatedBy: "login:" + desk.Device.ID, Parent: desk.Device.ID, Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	owner := ownerSessionToken(t, h.st)
	ch, cancel := openStream(t, h, "", board.Token, "")
	defer cancel()
	next(t, ch, "hello", 2*time.Second)
	if code, body := h.do("DELETE", "/v1/devices/"+desk.Device.ID, owner.Token, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", code, body)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream of a board session was not closed when its device was revoked")
		}
	}
}
