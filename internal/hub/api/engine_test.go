package api_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/store"
)

type fakeEngine struct {
	mu  sync.Mutex
	got []ingest.Changes
}

func (f *fakeEngine) Notify(ch ingest.Changes) {
	f.mu.Lock()
	f.got = append(f.got, ch)
	f.mu.Unlock()
}

// Config.Engine.Notify is called once per batch, after the stream publish, with the batch's
// changes; a hub without an engine ingests the same batch without complaint.
func TestEngineNotifiedAfterIngest(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{}
	srv := api.New(api.Config{Store: st, Engine: eng, PingEvery: time.Hour})
	hs := httptest.NewServer(srv.Handler())
	h := &hub{t: t, path: "", st: st, srv: srv, http: hs, tokens: map[string]string{}}
	t.Cleanup(h.stop)
	if srv.Engine() != api.Engine(eng) || srv.Store() != st {
		t.Fatal("accessors")
	}

	machine := h.token("machine")
	h.ingest(machine, simpleHook(t, h.machine, "s-eng", "SessionStart", t0))
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if len(eng.got) != 1 {
		t.Fatalf("Notify calls: %d, want 1", len(eng.got))
	}
	if _, ok := eng.got[0].Sessions["s-eng"]; !ok {
		t.Fatalf("changes %+v", eng.got[0])
	}
}

func TestNoEngineIsFine(t *testing.T) {
	h := startHub(t, "")
	if h.srv.Engine() != nil {
		t.Fatal("engine")
	}
	h.ingest(h.token("machine"), simpleHook(t, h.machine, "s-none", "SessionStart", t0))
}
