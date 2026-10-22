package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouteTableIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range handlerRoutes {
		k := r.Method + " " + r.Path
		if seen[k] {
			t.Errorf("%s listed twice", k)
		}
		seen[k] = true
		if !strings.HasPrefix(r.Path, "/v1/") {
			t.Errorf("%s: not under /v1/", k)
		}
	}
	// Every operation of the table appears exactly once in Routes(), reserved while
	// unregistered. TestEveryAPIPathIsRouted compares Routes() with api.yaml.
	n := 0
	for _, r := range New(Config{Store: nil}).Routes() {
		if inTable(handlerRoutes, r.Method, r.Path) {
			n++
			if _, ok := registered(r.Method, r.Path); r.Reserved == ok {
				t.Errorf("%s %s: Reserved=%v but registered=%v", r.Method, r.Path, r.Reserved, ok)
			}
		}
	}
	if n != len(handlerRoutes) {
		t.Errorf("Routes() has %d of the %d operations of handlerRoutes", n, len(handlerRoutes))
	}
}

// An operation without a registered handler is reserved: 501 for everyone, before auth.
func TestUnregisteredOperationIsReserved(t *testing.T) {
	rt := handlerRoutes[0]
	key := rt.Method + " " + rt.Path
	registryMu.Lock()
	f := registry[key]
	delete(registry, key)
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registry[key] = f
		registryMu.Unlock()
	})

	s := New(Config{Store: nil})
	for _, r := range s.Routes() {
		if r.Method == rt.Method && r.Path == rt.Path && !r.Reserved {
			t.Fatal("not reserved without a handler")
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(rt.Method, rt.Path, nil))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("%s: %d, want 501", key, w.Code)
	}
}

func TestRegisterRejectsMistakes(t *testing.T) {
	for name, fn := range map[string]func(){
		"unknown":   func() { Register("GET", "/v1/nope", func(*Server) http.Handler { return nil }) },
		"read path": func() { Register("GET", "/v1/sessions", func(*Server) http.Handler { return nil }) },
		"unlisted":  func() { Register("GET", "/v1/accounts/{seat}", func(*Server) http.Handler { return nil }) },
		"nil":       func() { Register("GET", "/v1/me", nil) },
		"duplicate": func() {
			rt := handlerRoutes[0]
			Register(rt.Method, rt.Path, func(*Server) http.Handler { return http.NotFoundHandler() })
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
