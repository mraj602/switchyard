package api_test

import "testing"

// Review phase 4 finding 5: /v1/actions/{token} is unauthenticated, so it is rate limited per IP.
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
