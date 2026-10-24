package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/outbox"
)

// vHub is a fake hub that reads record v up to vmax, stores what it accepts and says what
// it reads in /health and (when withHub) in every ingest reply.
type vHub struct {
	mu      sync.Mutex
	vmax    int
	withHub bool
	stored  []string
	oldest  []string
	health  int
	srv     *httptest.Server
}

func newVHub(t *testing.T, vmax int, withHub bool) *vHub {
	h := &vHub{vmax: vmax, withHub: withHub}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if r.URL.Path == "/health" {
			h.health++
			fmt.Fprintf(w, `{"ok":true,"version":"0.5.0","schema":4,"record_v_max":%d}`, h.vmax)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var b struct {
			Records []json.RawMessage `json:"records"`
		}
		_ = json.Unmarshal(raw, &b)
		h.oldest = append(h.oldest, r.Header.Get("X-Wy-Backlog-Oldest"))
		reply := map[string]any{"accepted": []string{}, "duplicates": []string{}, "rejected": []any{}}
		var acc []string
		for _, rec := range b.Records {
			var e struct {
				ID string `json:"id"`
				V  int    `json:"v"`
			}
			_ = json.Unmarshal(rec, &e)
			if e.V > h.vmax {
				t.Errorf("hub got record %s with v %d above its record_v_max %d", e.ID, e.V, h.vmax)
			}
			h.stored = append(h.stored, e.ID)
			acc = append(acc, e.ID)
		}
		reply["accepted"] = acc
		if h.withHub {
			reply["hub"] = map[string]any{"version": "0.5.0", "record_v_max": h.vmax}
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *vHub) setVMax(v int) { h.mu.Lock(); h.vmax = v; h.mu.Unlock() }
func (h *vHub) ids() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.stored...)
}

func vline(i, v int) string {
	return fmt.Sprintf(`{"v":%d,"id":%q,"kind":"hook_event","ts":"2026-10-12T10:00:%02d.000Z","machine":"m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K","payload":{}}`, v, rid(i), i%60)
}

func writeDay(t *testing.T, ob *outbox.Outbox, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(ob.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ob.Dir, "2026-10-12.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFlushHoldsAtTooNewRecordAndResumesWhenHubReadsMore(t *testing.T) {
	root := t.TempDir()
	ob := outbox.New(filepath.Join(root, "outbox"))
	writeDay(t, ob, vline(0, 1), vline(1, 1), vline(2, 2), vline(3, 1))
	h := newVHub(t, 1, true)
	clk := newClock()

	res := Flush(context.Background(), opts(ob, New(h.srv.URL, "tok"), clk))
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if got := h.ids(); len(got) != 2 || got[0] != rid(0) || got[1] != rid(1) {
		t.Fatalf("sent %v, want only the two records before the v2 one", got)
	}
	if res.Held != 2 || res.Drained {
		t.Fatalf("held %d drained %v", res.Held, res.Drained)
	}
	st := ReadStatus(ob.Dir)
	if st.Held != 2 || st.HubTooOld == nil || st.HubTooOld.Needs != 2 || st.HubTooOld.RecordVMax != 1 || st.HubTooOld.Hub != "0.5.0" || st.Via != ViaPush {
		t.Fatalf("status %+v %+v", st, st.HubTooOld)
	}
	if c := ob.ReadCursor(); c.Offset == 0 {
		t.Fatal("the cursor must have moved past the sent prefix")
	}
	// hub.json caches what the reply said.
	if c, ok := ReadHubCache(root); !ok || c.RecordVMax != 1 || c.Version != "0.5.0" || c.URL != h.srv.URL {
		t.Fatalf("hub.json %+v %v", c, ok)
	}

	// The hub is updated: /health shows v2. The next flush asks /health once (the held
	// record is first in line), then sends the rest, in order.
	h.setVMax(2)
	res = Flush(context.Background(), opts(ob, New(h.srv.URL, "tok"), clk))
	if res.Err != nil || !res.Drained || res.Held != 0 {
		t.Fatalf("%+v", res)
	}
	if got := h.ids(); len(got) != 4 || got[2] != rid(2) || got[3] != rid(3) {
		t.Fatalf("sent %v", got)
	}
	if st := ReadStatus(ob.Dir); st.Held != 0 || st.HubTooOld != nil {
		t.Fatalf("status still holds: %+v", st)
	}
	if c, _ := ReadHubCache(root); c.RecordVMax != 2 {
		t.Fatalf("cache not refreshed: %+v", c)
	}
}

func TestFlushUsesCachedRecordVMaxWithoutAsking(t *testing.T) {
	root := t.TempDir()
	ob := outbox.New(filepath.Join(root, "outbox"))
	writeDay(t, ob, vline(0, 2), vline(1, 2))
	h := newVHub(t, 2, true)
	if err := WriteHubCache(root, HubCache{URL: h.srv.URL + "/", RecordVMax: 2, At: "2026-10-12T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	res := Flush(context.Background(), opts(ob, New(h.srv.URL, "tok"), newClock()))
	if res.Err != nil || !res.Drained || len(h.ids()) != 2 || h.health != 0 {
		t.Fatalf("%+v ids %v health %d", res, h.ids(), h.health)
	}
	// A cache for another hub is ignored: the default (1) applies.
	ob2 := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	writeDay(t, ob2, vline(0, 2))
	other := newVHub(t, 1, false)
	if err := WriteHubCache(filepath.Dir(ob2.Dir), HubCache{URL: "https://elsewhere", RecordVMax: 2}); err != nil {
		t.Fatal(err)
	}
	res = Flush(context.Background(), opts(ob2, New(other.srv.URL, "tok"), newClock()))
	if res.Held != 1 || len(other.ids()) != 0 {
		t.Fatalf("held %d sent %v", res.Held, other.ids())
	}
}

func TestFlushHeldWhenHubDoesNotSayItsVersion(t *testing.T) {
	// An older hub says nothing in /health or in replies: it reads v1.
	root := t.TempDir()
	ob := outbox.New(filepath.Join(root, "outbox"))
	writeDay(t, ob, vline(0, 1), vline(1, 2))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"ok":true,"version":"0.4.2","schema":3}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"v":2`) {
			t.Error("a v2 record reached an old hub")
		}
		fmt.Fprintf(w, `{"accepted":[%q],"duplicates":[],"rejected":[]}`, rid(0))
	}))
	defer srv.Close()
	res := Flush(context.Background(), opts(ob, New(srv.URL, "tok"), newClock()))
	if res.Err != nil || res.Held != 1 {
		t.Fatalf("%+v", res)
	}
	if st := ReadStatus(ob.Dir); st.HubTooOld == nil || st.HubTooOld.Hub != "0.4.2" || st.HubTooOld.RecordVMax != 1 {
		t.Fatalf("%+v", st.HubTooOld)
	}
}

func TestFlushSendsBacklogOldestOnlyWithMoreThanOneBatch(t *testing.T) {
	root := t.TempDir()
	ob := outbox.New(filepath.Join(root, "outbox"))
	var lines []string
	for i := 0; i < 7; i++ {
		lines = append(lines, vline(i, 1))
	}
	writeDay(t, ob, lines...)
	h := newVHub(t, 1, true)
	o := opts(ob, New(h.srv.URL, "tok"), newClock())
	o.MaxRecords = 3
	if res := Flush(context.Background(), o); res.Err != nil || !res.Drained {
		t.Fatalf("%+v", res)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Batches of 3, 3, 1: the first two have more behind them, the last does not.
	if len(h.oldest) != 3 || h.oldest[0] != "2026-10-12T10:00:00.000Z" || h.oldest[1] != "2026-10-12T10:00:03.000Z" || h.oldest[2] != "" {
		t.Fatalf("X-Wy-Backlog-Oldest per request: %q", h.oldest)
	}
}

func TestLastContactOnlyOnSuccess(t *testing.T) {
	root := t.TempDir()
	ob := outbox.New(filepath.Join(root, "outbox"))
	writeDay(t, ob, vline(0, 1))
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"accepted":[%q],"duplicates":[],"rejected":[]}`, rid(0))
	}))
	defer srv.Close()
	clk := newClock()
	o := opts(ob, New(srv.URL, "tok"), clk)
	o.Budget = time.Second
	if res := Flush(context.Background(), o); res.Err == nil {
		t.Fatal("503 must fail the run")
	}
	if st := ReadStatus(ob.Dir); st.LastContact != "" {
		t.Fatalf("a 503 is not contact: %+v", st)
	}
	fail = false
	o.Force = true
	clk.advance(time.Hour)
	if res := Flush(context.Background(), o); res.Err != nil {
		t.Fatal(res.Err)
	}
	st := ReadStatus(ob.Dir)
	if st.LastContact == "" || st.LastContact != st.LastSuccess || st.Via != ViaPush {
		t.Fatalf("%+v", st)
	}
	// A later failure keeps last_contact.
	fail = true
	clk.advance(time.Hour)
	writeDay(t, ob, vline(0, 1), vline(1, 1))
	if res := Flush(context.Background(), o); res.Err == nil {
		t.Fatal("want failure")
	}
	if st2 := ReadStatus(ob.Dir); st2.LastContact != st.LastContact {
		t.Fatalf("last_contact moved on failure: %q -> %q", st.LastContact, st2.LastContact)
	}
}

func TestRefreshHubWritesCache(t *testing.T) {
	root := t.TempDir()
	h := newVHub(t, 2, false)
	now := time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC)
	hh, err := RefreshHub(context.Background(), New(h.srv.URL, "x"), root, now)
	if err != nil || hh.VMax() != 2 {
		t.Fatal(err, hh)
	}
	c, ok := ReadHubCache(root)
	if !ok || c.RecordVMax != 2 || c.At != "2026-10-13T01:02:03Z" || c.URL != h.srv.URL {
		t.Fatalf("%+v", c)
	}
}
