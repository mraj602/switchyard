package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/outbox"
)

// A 403 machine_mismatch (the token is for another machine) must keep the outbox and the
// cursor where they are, like any other 403.
func TestMachineMismatch403KeepsOutboxAndCursor(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	fill(t, ob, 3, "2026-10-12")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"this token is bound to another machine","code":"machine_mismatch"}`)
	}))
	defer srv.Close()
	cl := &Client{BaseURL: srv.URL, Token: "tok", HTTP: &http.Client{Timeout: time.Second}}
	res := Flush(context.Background(), opts(ob, cl, newClock()))
	var se *StatusError
	if !errors.As(res.Err, &se) || se.Status != 403 || se.Code != "machine_mismatch" {
		t.Fatalf("err = %v", res.Err)
	}
	if res.Accepted != 0 || res.Rejected != 0 || res.Dropped != 0 || res.Drained {
		t.Fatalf("result %+v", res)
	}
	if c := ob.ReadCursor(); c.File != "" || c.Offset != 0 {
		t.Fatalf("cursor moved on 403: %+v", c)
	}
	b, err := ob.Read(ob.ReadCursor(), 10, 1<<20)
	if err != nil || len(b.Items) != 3 {
		t.Fatalf("outbox lost records: %d items, %v", len(b.Items), err)
	}
	if calls != 1 {
		t.Fatalf("%d requests", calls)
	}
}
