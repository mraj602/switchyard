package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A hub (or a proxy in front of it) that redirects an https request to plain http must not
// get the bearer token over the unencrypted hop.
func TestIngestRefusesHTTPSToHTTPRedirect(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/v1/ingest", http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	c := New(secure.URL, "secret-token")
	c.HTTP = secure.Client()
	_, err := c.Ingest(context.Background(), [][]byte{[]byte(`{"id":"a"}`)}, -1)
	if err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Fatalf("err = %v, want a refused redirect", err)
	}
	if n := plainHits.Load(); n != 0 {
		t.Fatalf("the plain http server was reached %d times", n)
	}
}
