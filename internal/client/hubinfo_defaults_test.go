package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAndReplyCarryVersionFields(t *testing.T) {
	var health, reply string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(health))
			return
		}
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	c := New(srv.URL, "sy_m_x")
	ctx := context.Background()

	// An older hub says nothing: it reads record v 1.
	health = `{"ok":true,"version":"0.4.2","schema":3,"time":"2026-10-13T00:00:00.000Z"}`
	reply = `{"accepted":["a"],"duplicates":[],"rejected":[]}`
	h, err := c.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.RecordVMax != 0 || h.VMax() != 1 || h.Frozen != nil || h.DrainProto != 0 {
		t.Fatalf("old hub: %+v", h)
	}
	r, err := c.Ingest(ctx, [][]byte{[]byte(`{"id":"a"}`)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Hub != nil || r.Hub.VMax() != 1 {
		t.Fatalf("old hub reply: %+v", r)
	}

	health = `{"ok":true,"version":"0.5.0","schema":4,"time":"t","record_v_max":2,"batch_v":[1],"drain_proto":1,` +
		`"frozen":{"since":"2026-10-13T09:00:00.000Z","moved_to":"https://new.example"},"future":1}`
	reply = `{"accepted":["a"],"duplicates":[],"rejected":[],"hub":{"version":"0.5.0","record_v_max":2},"future":1}`
	if h, err = c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if h.VMax() != 2 || len(h.BatchV) != 1 || h.DrainProto != 1 || h.Frozen == nil || h.Frozen.MovedTo == nil || *h.Frozen.MovedTo != "https://new.example" {
		t.Fatalf("new hub: %+v", h)
	}
	if r, err = c.Ingest(ctx, [][]byte{[]byte(`{"id":"a"}`)}, 0); err != nil {
		t.Fatal(err)
	}
	if r.Hub == nil || r.Hub.Version != "0.5.0" || r.Hub.VMax() != 2 {
		t.Fatalf("new hub reply: %+v", r.Hub)
	}
}
