package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// frozenHub answers /health with or without `frozen`, and ingest 200 unless frozen.
type frozenHub struct {
	mu     sync.Mutex
	frozen bool
	srv    *httptest.Server
}

func newFrozenHub(t *testing.T) *frozenHub {
	h := &frozenHub{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		fr := h.frozen
		h.mu.Unlock()
		switch r.URL.Path {
		case "/health":
			if fr {
				fmt.Fprint(w, `{"ok":true,"version":"0.5.0","schema":4,"record_v_max":1,"frozen":{"since":"2026-10-13T08:00:00.000Z","moved_to":"https://new.example"}}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"version":"0.5.0","schema":4,"record_v_max":1}`)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func TestRefreshHubCachesFrozenAndHubRefreshDue(t *testing.T) {
	state := t.TempDir()
	h := newFrozenHub(t)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	if !HubRefreshDue(state, h.srv.URL, now) {
		t.Fatal("no hub.json: refresh is due")
	}
	cl := New(h.srv.URL, "")
	if _, err := RefreshHub(context.Background(), cl, state, now); err != nil {
		t.Fatal(err)
	}
	if HubRefreshDue(state, h.srv.URL, now.Add(time.Hour)) {
		t.Error("fresh, unfrozen cache: due after 1 h")
	}
	if !HubRefreshDue(state, h.srv.URL, now.Add(HubRefreshEvery)) {
		t.Error("not due after 6 h")
	}
	if !HubRefreshDue(state, "https://other.example", now) {
		t.Error("another hub url: not due")
	}

	h.mu.Lock()
	h.frozen = true
	h.mu.Unlock()
	if _, err := RefreshHub(context.Background(), cl, state, now); err != nil {
		t.Fatal(err)
	}
	c, _ := ReadHubCache(state)
	if c.Frozen == nil || c.Frozen.MovedTo == nil || *c.Frozen.MovedTo != "https://new.example" {
		t.Fatalf("frozen not cached: %+v", c)
	}
	if HubRefreshDue(state, h.srv.URL, now.Add(time.Minute)) {
		t.Error("frozen cache: due before FrozenRecheck")
	}
	if !HubRefreshDue(state, h.srv.URL, now.Add(FrozenRecheck)) {
		t.Error("frozen cache: not re-read after FrozenRecheck (an unfreeze would go unseen)")
	}
	// Unfrozen: the next refresh drops it.
	h.mu.Lock()
	h.frozen = false
	h.mu.Unlock()
	if _, err := RefreshHub(context.Background(), cl, state, now.Add(FrozenRecheck)); err != nil {
		t.Fatal(err)
	}
	if c, _ := ReadHubCache(state); c.Frozen != nil {
		t.Errorf("unfrozen hub still cached as frozen: %+v", c)
	}
}

func TestHubRefreshDueAfterAFrozenRefusal(t *testing.T) {
	state := t.TempDir()
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	if err := WriteHubCache(state, HubCache{URL: "https://hub.example", RecordVMax: 1, At: now.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStatusFile(dir, Status{Error: "hub answered 503: hub is frozen for a move"})
	if HubRefreshDue(state, "https://hub.example", now.Add(time.Minute)) {
		t.Error("due before FrozenRecheck")
	}
	if !HubRefreshDue(state, "https://hub.example", now.Add(FrozenRecheck)) {
		t.Error("a flush refused by a frozen hub does not make the refresh due")
	}
}
