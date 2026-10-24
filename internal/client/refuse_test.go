package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/outbox"
)

// A hub or proxy that answers 400 to everything must not drain the outbox: one lone drop,
// then the flusher keeps the rest, backs off and says "hub refuses records".
func TestHubThatRefusesEverythingKeepsTheOutbox(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	fill(t, ob, 30, "2026-10-12")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"nope","code":"bad_request"}`)
	}))
	defer srv.Close()
	cl := &Client{BaseURL: srv.URL, Token: "tok", HTTP: &http.Client{Timeout: testHTTPTimeout}}
	clk := newClock()

	var dropped int
	for run := 0; run < 5; run++ {
		o := opts(ob, cl, clk)
		o.Force = true // ignore the stored backoff: simulate many triggers
		res := Flush(context.Background(), o)
		dropped += res.Dropped
		clk.t = clk.t.Add(time.Minute)
	}
	if dropped > 1 {
		t.Fatalf("dropped %d records; a hub refusing everything may cost at most one", dropped)
	}
	b, err := ob.Read(ob.ReadCursor(), 100, 1<<20)
	if err != nil || len(b.Items) < 29 {
		t.Fatalf("outbox drained: %d items left (%v)", len(b.Items), err)
	}
	st := ReadStatus(ob.Dir)
	if !st.RefusesRecords || st.OK || st.Error == "" {
		t.Errorf("status = %+v, want refuses_records", st)
	}
	if c := ob.ReadCursor(); c.RetryAfter == "" || c.Failures == 0 {
		t.Errorf("no backoff stored: %+v", c)
	}
}

// A truly bad record among good ones is still dropped, however often that happens, because
// the hub accepts the others in the same run.
func TestPoisonRecordsAmongGoodOnesStillDropped(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 40, "2026-10-12")
	h := newHub(t)
	h.poison = ids[0] // the very first record: lone on the first attempt, nothing earlier in the run
	clk := newClock()
	res := Flush(context.Background(), opts(ob, h.client(), clk))
	if res.Err != nil || !res.Drained || res.Dropped != 1 {
		t.Fatalf("%+v", res)
	}
	if c := ob.ReadCursor(); c.LoneDrops != 0 {
		t.Errorf("counter should reset once the hub accepted records: %+v", c)
	}
}
