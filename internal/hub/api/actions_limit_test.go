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
