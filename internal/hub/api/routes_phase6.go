package api

// Phase 6 operations (desktop.md C10) follow the phase 4 pattern (routes_phase4.go): listed
// here once with the scopes of api.yaml, 501 until a handler file (WP-H: accounts.go)
// registers a factory with Register from init(), and server.go never changes again.
// `Seat.places[].stopped_reason` is additive on GET /v1/accounts and needs no route.

// phase6Routes: method, path, x-scopes exactly as api.yaml. Reserved is filled in by Routes().
var phase6Routes = []Route{
	{Method: "PATCH", Path: "/v1/accounts/{seat}", Scopes: []string{"owner"}},
	{Method: "PUT", Path: "/v1/seats/order", Scopes: []string{"owner"}},
}

func isPhase6(method, path string) bool { return inTable(phase6Routes, method, path) }

// phase6RoutesNow returns the table with Reserved set from the registry.
func phase6RoutesNow() []Route { return withReserved(phase6Routes) }
