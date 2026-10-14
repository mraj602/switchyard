package api

import (
	"net/http"
	"strings"
	"testing"
)

// firstReserved returns a phase 4 operation nobody has registered yet (wave 1 files take
// them one by one), or skips.
func firstReserved(t *testing.T) Route {
	t.Helper()
	for _, r := range phase4RoutesNow() {
		if r.Reserved {
			return r
		}
	}
	t.Skip("every phase 4 operation has a handler")
	return Route{}
}

func TestPhase4TableIsUniqueAndComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range phase4Routes {
		k := r.Method + " " + r.Path
		if seen[k] {
			t.Errorf("%s listed twice", k)
		}
		seen[k] = true
		if !strings.HasPrefix(r.Path, "/v1/") {
			t.Errorf("%s: not under /v1/", k)
		}
	}
	if len(phase4Routes) != 27 {
		t.Errorf("%d phase 4 operations, api.yaml has 27 (TestEveryAPIPathIsRouted checks the rest)", len(phase4Routes))
	}
}

func TestRegisterFlipsReservedAndServes(t *testing.T) {
	rt := firstReserved(t)
	called := false
	Register(rt.Method, rt.Path, func(s *Server) http.Handler {
		called = true
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
	s.Handler() // builds the mux: this is where factories run
	if !called {
		t.Fatal("factory not called by Handler")
	}
}

func TestRegisterRejectsMistakes(t *testing.T) {
	for name, fn := range map[string]func(){
		"unknown": func() { Register("GET", "/v1/nope", func(*Server) http.Handler { return nil }) },
		"phase 2": func() { Register("GET", "/v1/sessions", func(*Server) http.Handler { return nil }) },
		"nil":     func() { Register("GET", "/v1/me", nil) },
		"duplicate": func() {
			f := func(*Server) http.Handler { return http.NotFoundHandler() }
			rt := phase4Routes[0]
			if _, taken := registered(rt.Method, rt.Path); taken {
				// Every operation has a handler (end of wave 1): registering one again
				// must panic. Calling firstReserved here would Skip inside the recover.
				Register(rt.Method, rt.Path, f)
				return
			}
			Register(rt.Method, rt.Path, f)
			defer func() {
				registryMu.Lock()
				delete(registry, rt.Method+" "+rt.Path)
				registryMu.Unlock()
			}()
			Register(rt.Method, rt.Path, f)
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			fn()
		}()
	}
}
