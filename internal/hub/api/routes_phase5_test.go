package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPhase5TableIsUniqueAndComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range phase5Routes {
		k := r.Method + " " + r.Path
		if seen[k] {
			t.Errorf("%s listed twice", k)
		}
		seen[k] = true
		if !strings.HasPrefix(r.Path, "/v1/") {
			t.Errorf("%s: not under /v1/", k)
		}
		if isPhase4(r.Method, r.Path) {
			t.Errorf("%s is also in the phase 4 table", k)
		}
	}
	if len(phase5Routes) != 4 {
		t.Errorf("%d phase 5 operations, machines.md 6.2 adds 4 (GET /v1/machines?all is a phase 2 path)", len(phase5Routes))
	}
	// Every phase 5 route appears exactly once in Routes(), reserved while unregistered.
	s := New(Config{Store: nil})
	n := 0
	for _, r := range s.Routes() {
		if isPhase5(r.Method, r.Path) {
			n++
			if _, ok := registered(r.Method, r.Path); r.Reserved == ok {
				t.Errorf("%s %s: Reserved=%v but registered=%v", r.Method, r.Path, r.Reserved, ok)
			}
		}
	}
	if n != 4 {
		t.Errorf("Routes() has %d phase 5 operations", n)
	}
}

func TestRegisterAcceptsPhase5(t *testing.T) {
	rt := phase5Routes[len(phase5Routes)-1]
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
