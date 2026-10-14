package api

// Addendum operations (F1 keys and link, PE1-PE3 peers, N2 app updates; keys.md, link.md,
// peers.md, phone.md P16) follow the phase 4 pattern (routes_phase4.go): listed here once
// with the scopes of api.yaml, 501 until a handler file registers a factory with Register
// from init(). Operations without scopes (/v1/link, /v1/link/poll) authenticate themselves
// with the link's challenge login, never with a bearer.

// addendumRoutes: method, path, x-scopes exactly as api.yaml. Reserved is filled in by Routes().
var addendumRoutes = []Route{
	{Method: "GET", Path: "/v1/link"},
	{Method: "POST", Path: "/v1/link/poll"},
	{Method: "GET", Path: "/v1/keys", Scopes: []string{"owner", "read", "push", "write"}},
	{Method: "POST", Path: "/v1/keys/owner", Scopes: []string{"owner", "write"}},
	{Method: "POST", Path: "/v1/keys/owner/confirm", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/keys/revocations", Scopes: []string{"owner", "read", "push", "write"}},
	{Method: "POST", Path: "/v1/keys/revocations", Scopes: []string{"owner", "write"}},
	{Method: "PUT", Path: "/v1/devices/{id}/keys", Scopes: []string{"write", "push"}},
	{Method: "GET", Path: "/v1/devices/{id}/cert", Scopes: []string{"owner", "write"}},
	{Method: "POST", Path: "/v1/devices/{id}/cert", Scopes: []string{"owner", "write"}},
	{Method: "GET", Path: "/v1/peers", Scopes: []string{"owner", "read"}},
	{Method: "GET", Path: "/v1/peers/matrix", Scopes: []string{"owner", "read", "push", "write"}},
	{Method: "PUT", Path: "/v1/peers/matrix", Scopes: []string{"owner", "push", "write"}},
	{Method: "POST", Path: "/v1/peers/pause", Scopes: []string{"owner", "push", "write"}},
	{Method: "POST", Path: "/v1/peers/threads/{id}/resume", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/peers/messages", Scopes: []string{"owner", "read"}},
	{Method: "GET", Path: "/v1/peers/audit", Scopes: []string{"owner", "read"}},
	{Method: "GET", Path: "/v1/app/android", Scopes: []string{"owner", "read", "push"}},
	{Method: "GET", Path: "/v1/app/android/apk", Scopes: []string{"owner", "read", "push"}},
}

func isAddendum(method, path string) bool { return inTable(addendumRoutes, method, path) }

// addendumRoutesNow returns the table with Reserved set from the registry.
func addendumRoutesNow() []Route { return withReserved(addendumRoutes) }
