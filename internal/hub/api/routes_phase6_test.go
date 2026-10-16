package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPhase6TableIsUniqueAndComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range phase6Routes {
		k := r.Method + " " + r.Path
		if seen[k] {
			t.Errorf("%s listed twice", k)
		}
		seen[k] = true
		if !strings.HasPrefix(r.Path, "/v1/") {
			t.Errorf("%s: not under /v1/", k)
		}
		if isPhase4(r.Method, r.Path) || isPhase5(r.Method, r.Path) {
			t.Errorf("%s is also in the phase 4 or 5 table", k)
		}
	}
	if len(phase6Routes) != 2 {
		t.Errorf("%d phase 6 operations, desktop.md C10 adds PATCH /v1/accounts/{seat}, the seat order adds PUT /v1/seats/order", len(phase6Routes))
	}
	// Every phase 6 route appears exactly once in Routes(), reserved while unregistered.
	s := New(Config{Store: nil})
	n := 0
	for _, r := range s.Routes() {
		if isPhase6(r.Method, r.Path) {
			n++
			if _, ok := registered(r.Method, r.Path); r.Reserved == ok {
				t.Errorf("%s %s: Reserved=%v but registered=%v", r.Method, r.Path, r.Reserved, ok)
			}
		}
	}
	if n != 2 {
		t.Errorf("Routes() has %d phase 6 operations", n)
	}
}

func TestRegisterAcceptsPhase6(t *testing.T) {
	rt := phase6Routes[0]
	if _, taken := registered(rt.Method, rt.Path); taken {
		t.Skip("handler already registered (wave 1)")
	}
	Register(rt.Method, rt.Path, func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	})
	t.Cleanup(func() {
		registryMu.Lock()
		delete(registry, rt.Method+" "+rt.Path)
		registryMu.Unlock()
	})
	s := New(Config{Store: nil})
	for _, r := range s.Routes() {
		if r.Method == rt.Method && r.Path == rt.Path && r.Reserved {
			t.Fatal("still reserved after Register")
		}
	}
	s.Handler()
}

func TestRegisterRefusesUnlistedPath(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register accepted GET /v1/accounts/{seat}, which no phase table lists")
		}
	}()
	Register("GET", "/v1/accounts/{seat}", func(s *Server) http.Handler { return http.NotFoundHandler() })
}
