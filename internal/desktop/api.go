// Package desktop implements `wy desktop`: every hub-side action the desktop app (app/)
// needs, as one JSON object on stdout (docs/spec/desktop.md C4). The app runs no
// other hub command and holds no SSH logic.
//
// This file is the contract with the app: the JSON shapes of C4 with their exact tags, the
// exit codes and the service states. The app parses exactly these shapes, so they change
// only through desktop.md.
package desktop

// Exit codes of every `wy desktop` subcommand (desktop.md C4).
const (
	ExitOK          = 0
	ExitUsage       = 2
	ExitRefused     = 3 // refused by the hub: expired code, revoked token
	ExitNotHere     = 4 // not possible here: no local hub, no admin channel, wy not connected, service not installed
	ExitUnreachable = 5 // hub unreachable
)

// InfoV is the `v` of Info.
const InfoV = 1

// Hub service states (HubService.Service, InfoHub.Service).
const (
	ServiceRunning = "running"
	ServiceStopped = "stopped"
	ServiceMissing = "missing"
)

// Hub service actions (`wy desktop hub <action>`).
const (
	ActionStatus = "status"
	ActionStart  = "start"
	ActionStop   = "stop"
)

// AdminLocal is Info.Admin when the admin channel is this machine's own hub.db.
const AdminLocal = "local"

// Info is `wy desktop info --json`.
//
//	{"v": 1, "wy": "0.6.0", "machine": {"id": "m_…", "name": "laptop"},
//	 "hub": {"url": "http://127.0.0.1:8787", "connected": true, "reachable": true, "version": "0.6.0",
//	         "local": true, "dial": null, "service": "running"},
//	 "admin": "local", "admin_error": null, "outbox": {"pending": 3, "last_error": null}}
type Info struct {
	V       int         `json:"v"`
	Wy      string      `json:"wy"`
	Machine InfoMachine `json:"machine"`
	Hub     InfoHub     `json:"hub"`
	// Admin is the admin channel of machines.md 4.4: AdminLocal, the machines.hub_admin
	// command prefix (e.g. "ssh hub-host wy"), or null when there is none.
	Admin *string `json:"admin"`
	// AdminError is why machines.hub_admin cannot be used (a bad command prefix), else null.
	// `token` and `owner-link` then exit 4 with it; everything else in info still works.
	AdminError *string    `json:"admin_error"`
	Outbox     InfoOutbox `json:"outbox"`
}

// InfoMachine is Info.machine.
type InfoMachine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InfoHub is Info.hub.
type InfoHub struct {
	URL       *string `json:"url"` // null when wy is not connected
	Connected bool    `json:"connected"`
	Reachable bool    `json:"reachable"`
	Version   *string `json:"version"` // from /health; null when unreachable
	// Local is true when <data>/hub.db exists and URL is a loopback URL.
	Local bool `json:"local"`
	// Dial is hub.dial (forward mode, machines.md); the app refuses such setups.
	Dial *string `json:"dial"`
	// Service is the hub user service state, or null when the hub is not on this machine.
	Service *string `json:"service"`
}

// InfoOutbox is Info.outbox.
type InfoOutbox struct {
	Pending   int     `json:"pending"`
	LastError *string `json:"last_error"`
}

// Token is `wy desktop token [--code-stdin] [--name N] --json`. The token is printed once and
// never stored by wy.
//
//	{"device": "dev_…", "token": "wy_d_…", "scopes": ["write", "read"], "hub": {"url": "…"}}
type Token struct {
	Device string   `json:"device"`
	Token  string   `json:"token"`
	Scopes []string `json:"scopes"`
	Hub    TokenHub `json:"hub"`
}

// TokenHub is Token.hub.
type TokenHub struct {
	URL string `json:"url"`
}

// OwnerLink is `wy desktop owner-link --json`.
//
//	{"url": "https://hub…/#/owner?s=…", "expires_at": "…"}
type OwnerLink struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"` // RFC 3339 UTC
}

// HubService is `wy desktop hub status|start|stop --json`.
//
//	{"service": "running" | "stopped" | "missing", "unit": "…", "health": true}
type HubService struct {
	Service string `json:"service"`
	Unit    string `json:"unit"`
	Health  bool   `json:"health"`
}
