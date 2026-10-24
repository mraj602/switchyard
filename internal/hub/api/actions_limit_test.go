package api_test

import (
	"bytes"
	"context"
	"log"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// /v1/actions/{token} is unauthenticated, so it is rate limited per IP.
func TestActionEndpointIsRateLimitedPerIP(t *testing.T) {
	r := newPushRig(t, true)
	for i := 0; i < 30; i++ {
		if st, _, b := r.do("POST", "/v1/actions/a1.AAAA.AAAA", "", nil); st == 429 {
			t.Fatalf("request %d limited too early: %s", i+1, b)
		}
	}
	st, h, _ := r.do("POST", "/v1/actions/a1.AAAA.AAAA", "", nil)
	if st != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("31st request: %d retry-after %q", st, h.Get("Retry-After"))
	}
}

// push.md 8: GET of an action URL is 405 (link previews and prefetching never act).
func TestActionEndpointGetIs405(t *testing.T) {
	r := newPushRig(t, true)
	for _, m := range []string{"GET", "HEAD", "PUT"} {
		st, h, _ := r.do(m, "/v1/actions/a1.AAAA.AAAA", "", nil)
		if st != 405 || h.Get("Allow") != "POST" {
			t.Fatalf("%s: %d allow %q, want 405 POST", m, st, h.Get("Allow"))
		}
	}
	if st, _, _ := r.do("POST", "/v1/actions/a1.AAAA.AAAA", "", nil); st == 405 {
		t.Fatal("POST must reach the handler")
	}
}

// The action token is the credential of its URL: the request log never shows it.
func TestRequestLogOmitsActionToken(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	srv := api.New(api.Config{Store: st, PingEvery: time.Hour, Log: log.New(&buf, "", 0)})
	h := srv.Handler()
	t.Cleanup(func() { srv.Close(); st.Close() })
	const tok = "a1.SECRETPART.MACPART"
	for _, m := range []string{"POST", "GET"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(m, "/v1/actions/"+tok, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/sessions", nil))
	out := buf.String()
	if strings.Contains(out, "SECRETPART") || strings.Count(out, "/v1/actions/…") != 2 {
		t.Fatalf("request log shows the action token:\n%s", out)
	}
	if !strings.Contains(out, "GET /v1/sessions 401") {
		t.Fatalf("other paths are logged as they are:\n%s", out)
	}
}
