package api

import (
	"fmt"
	"net/http"
	"sync"
)

// Phase 4 operations (pairing, devices, push, rules, alerts) are listed here once, with the
// scopes of api.yaml. Each is reserved (501 for everyone, before auth) until a handler file
// registers a factory for it from init():
//
//	func init() {
//		Register("GET", "/v1/devices", func(s *Server) http.Handler { return http.HandlerFunc(s.listDevices) })
//	}
//
// server.go merges this table into Routes() and Handler() and never changes again: adding
// a handler means adding a file, deleting the operation's `x-reserved: phase 4` line in
// api.yaml, and nothing else. TestEveryAPIPathIsRouted checks the table, the registry and
// api.yaml agree (Reserved is true exactly while no factory is registered).

// HandlerFactory builds the handler of one operation. It runs once, in Server.Handler, so a
// handler may capture s and use s.Store(), s.Engine(), s.Auth, s.Broker, s.Config().
type HandlerFactory func(s *Server) http.Handler

var (
	registryMu sync.RWMutex
	registry   = map[string]HandlerFactory{}
)

// Register installs the handler of a phase 4, 5, 6 or addendum operation. Call it from init(). It
// panics when the operation is not in one of those tables (a typo, or a path owned by server.go) or is
// registered twice, so a mistake fails every test of the package at start.
func Register(method, path string, f HandlerFactory) {
	key := method + " " + path
	if f == nil {
		panic("api: Register " + key + ": nil factory")
	}
	if !isPhase4(method, path) && !isPhase5(method, path) && !isPhase6(method, path) && !isAddendum(method, path) {
		panic(fmt.Sprintf("api: Register %s: not a phase 4, 5, 6 or addendum operation (see routes_phase4.go, routes_phase5.go, routes_phase6.go, routes_addendum.go)", key))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[key]; dup {
		panic("api: Register " + key + ": registered twice")
	}
	registry[key] = f
}

func registered(method, path string) (HandlerFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[method+" "+path]
	return f, ok
}

func isPhase4(method, path string) bool { return inTable(phase4Routes, method, path) }

func inTable(table []Route, method, path string) bool {
	for _, r := range table {
		if r.Method == method && r.Path == path {
			return true
		}
	}
	return false
}

// phase4Routes: method, path, x-scopes exactly as api.yaml (nil = no bearer or cookie
// required: the operation authenticates itself). Reserved is filled in by Routes().
var phase4Routes = []Route{
	{Method: "GET", Path: "/v1/me", Scopes: []string{"read", "write", "push", "owner"}},
	{Method: "POST", Path: "/v1/session/owner"},
	{Method: "POST", Path: "/v1/pair/codes", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/pair/codes/{id}", Scopes: []string{"owner"}},
	{Method: "DELETE", Path: "/v1/pair/codes/{id}", Scopes: []string{"owner"}},
	{Method: "POST", Path: "/v1/pair/claim"},
	{Method: "GET", Path: "/v1/devices", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/devices/{id}", Scopes: []string{"owner", "read", "push"}},
	{Method: "PATCH", Path: "/v1/devices/{id}", Scopes: []string{"owner", "push"}},
	{Method: "DELETE", Path: "/v1/devices/{id}", Scopes: []string{"owner", "push"}},
	{Method: "GET", Path: "/v1/push/config", Scopes: []string{"read", "push", "owner"}},
	{Method: "PUT", Path: "/v1/push/settings", Scopes: []string{"owner"}},
	{Method: "PUT", Path: "/v1/push/channels/{kind}", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/push/targets", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/push/targets", Scopes: []string{"push", "owner"}},
	{Method: "PATCH", Path: "/v1/push/targets/{id}", Scopes: []string{"push", "owner"}},
	{Method: "DELETE", Path: "/v1/push/targets/{id}", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/push/targets/{id}/test", Scopes: []string{"push", "owner"}},
	{Method: "GET", Path: "/v1/rules", Scopes: []string{"read", "push", "owner"}},
	{Method: "POST", Path: "/v1/rules", Scopes: []string{"owner"}},
	{Method: "PUT", Path: "/v1/rules/{id}", Scopes: []string{"owner"}},
	{Method: "DELETE", Path: "/v1/rules/{id}", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/alerts", Scopes: []string{"read", "push", "owner"}},
	{Method: "GET", Path: "/v1/alerts/{id}", Scopes: []string{"read", "push", "owner"}},
	{Method: "POST", Path: "/v1/alerts/{id}/snooze", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/alerts/{id}/seen", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/actions/{token}"},
}

// phase4RoutesNow returns the table with Reserved set from the registry.
func phase4RoutesNow() []Route { return withReserved(phase4Routes) }

func withReserved(table []Route) []Route {
	out := make([]Route, len(table))
	for i, r := range table {
		_, ok := registered(r.Method, r.Path)
		r.Reserved = !ok
		out[i] = r
	}
	return out
}
