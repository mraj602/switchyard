package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mraj602/switchyard/internal/hub/store"
)

// Phase 5 operations (machines.md 6.2) follow the phase 4 pattern (routes_phase4.go): listed
// here once with the scopes of api.yaml, 501 until a handler file registers a factory with
// Register from init(), and server.go never changes again. `GET /v1/machines` (with its new
// `all` parameter) is a phase 2 operation and stays in server.go.

// phase5Routes: method, path, x-scopes exactly as api.yaml. Reserved is filled in by Routes().
var phase5Routes = []Route{
	{Method: "PATCH", Path: "/v1/machines/{id}", Scopes: []string{"owner"}},
	{Method: "DELETE", Path: "/v1/machines/{id}", Scopes: []string{"owner"}},
	{Method: "POST", Path: "/v1/machines/{id}/pull", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/push/direct-policy", Scopes: []string{"write"}},
}

func isPhase5(method, path string) bool { return inTable(phase5Routes, method, path) }

// phase5RoutesNow returns the table with Reserved set from the registry.
func phase5RoutesNow() []Route { return withReserved(phase5Routes) }

// PullStatus is a pull target's health (api.yaml PullStatus; machines.md 3.4, 6.1). The
// times are RFC 3339 strings (empty = null is written as a JSON null by the pointer fields).
type PullStatus struct {
	Name      string  `json:"name"`
	Target    string  `json:"target"` // user@host[:port], for display
	IntervalS int     `json:"interval_s"`
	State     string  `json:"state"` // new, ok, failing, host_key, auth, refused, disabled, frozen
	LastTry   *string `json:"last_try"`
	LastOK    *string `json:"last_ok"`
	LastError *string `json:"last_error"`
	NextAt    *string `json:"next_at"`
}

// Pull target states of PullStatus.State.
const (
	PullNew      = "new"
	PullOK       = "ok"
	PullFailing  = "failing"
	PullHostKey  = "host_key"
	PullAuth     = "auth"
	PullRefused  = "refused"
	PullDisabled = "disabled"
	PullFrozen   = "frozen"
)

// Errors a Puller returns; the pull-now handler maps them to 404 and 409.
var (
	ErrNoPullTarget = errors.New("api: machine has no pull target")
	ErrPullBlocked  = errors.New("api: pull target needs the owner first (host_key or refused)")
)

// SettingFrozen is the setting that freezes the hub for a move (machines.md 8.1); its value
// is {"since": <ms>, "moved_to": <url or null>}. SettingImported records an import (8.2).
const (
	SettingFrozen   = "hub.frozen"
	SettingImported = "hub.imported"
)

// Frozen is /health's `frozen` object (api.yaml Health.frozen).
type Frozen struct {
	Since   store.Time `json:"since"`
	MovedTo *string    `json:"moved_to"`
}

// frozenState reads hub.frozen; ok is false when the hub is not frozen (or unreadable).
func (s *Server) frozenState(ctx context.Context) (Frozen, bool) {
	if s.store == nil {
		return Frozen{}, false
	}
	raw, ok, err := s.store.AlertSetting(ctx, SettingFrozen)
	if err != nil || !ok {
		return Frozen{}, false
	}
	var v struct {
		Since   int64   `json:"since"`
		MovedTo *string `json:"moved_to"`
	}
	if json.Unmarshal(raw, &v) != nil {
		// Ingest treats an unparseable hub.frozen as frozen (store.FrozenIn); so does health.
		return Frozen{}, true
	}
	return Frozen{Since: store.Time(v.Since), MovedTo: v.MovedTo}, true
}

// Puller runs a pull now; the hub's pull scheduler (internal/hub/pull) implements it and
// `sy hub serve` passes it as Config.Puller. RunNow sets the target's next run to now and
// returns its status; errors the handler maps: ErrNoPullTarget (404), ErrPullBlocked (409).
// A nil Puller makes POST /v1/machines/{id}/pull answer 503.
type Puller interface {
	RunNow(ctx context.Context, machineID string) (PullStatus, error)
}
