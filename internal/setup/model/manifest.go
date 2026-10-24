package model

import (
	"encoding/json"
	"time"
)

// Manifest statuses (7.2).
const (
	StatusInProgress   = "in-progress"
	StatusApplied      = "applied"
	StatusRolledBack   = "rolled-back"
	StatusUndone       = "undone"
	StatusPartlyUndone = "partly-undone"
)

// Entry phases (7.2).
const (
	PhaseIntent   = "intent"
	PhaseDone     = "done"
	PhaseReverted = "reverted"
)

// Manifest is `<state>/backup/<id>/manifest.json`, the journal of one run (setup.md 7.2).
// It is rewritten atomically after every entry.
type Manifest struct {
	V          int     `json:"v"` // 1
	ID         string  `json:"id"`
	Command    string  `json:"command"`
	WyVersion  string  `json:"wy_version"` // sy_version in a manifest written before the rename
	Machine    string  `json:"machine"`
	PlanID     string  `json:"plan_id"`
	StartedAt  string  `json:"started_at"`            // TimeString
	FinishedAt string  `json:"finished_at,omitempty"` // TimeString; empty while in progress
	Status     string  `json:"status"`
	Entries    []Entry `json:"entries"`
}

// UnmarshalJSON reads wy_version, or sy_version, which a manifest written before the rename
// holds (legacy compat, rename.md 2.12).
func (m *Manifest) UnmarshalJSON(b []byte) error {
	type plain Manifest
	var v struct {
		plain
		SyVersion string `json:"sy_version"` // rename:keep: legacy compat
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*m = Manifest(v.plain)
	if m.WyVersion == "" {
		m.WyVersion = v.SyVersion
	}
	return nil
}

// Entry is one journaled action. Written with Phase "intent" (fsynced) before the action,
// "done" after, "reverted" once undone.
type Entry struct {
	Seq    int         `json:"seq"`
	Action string      `json:"action"` // action id
	Op     Op          `json:"op"`
	Phase  string      `json:"phase"`
	Paths  []PathEntry `json:"paths"`
	// Name is the service name of a `service` entry, or "timer" on a `connect` entry that
	// installed the flush timer: what undo stops (7.3). Absent for other ops.
	Name string `json:"name,omitempty"`
	// Created lists paths that did not exist before, deepest first (for `run` actions, found
	// by diffing the watched folders). Always present (may be empty).
	Created []string `json:"created"`
}

// PathEntry is the before/after of one path touched by an entry.
type PathEntry struct {
	Path   string     `json:"path"`
	Via    []string   `json:"via,omitempty"`
	Before PathState  `json:"before"`
	After  *PathState `json:"after,omitempty"` // nil until the action finished
	// Intended is what the action means to leave at Path, recorded with the intent when it
	// is known up front (a link's target, a written file's hash, a created folder). Recovery
	// of an interrupted entry (no After) restores Path only when it still equals Before or
	// Intended, so edits made after a crash are never overwritten (7.6).
	Intended *PathState  `json:"intended,omitempty"`
	Keys     []KeyChange `json:"keys,omitempty"` // JSON edits: key-level undo
}

// KeyChange is one JSON pointer edited by an entry.
type KeyChange struct {
	Pointer       string `json:"pointer"`
	BeforePresent bool   `json:"before_present"`
	Saved         string `json:"saved,omitempty"` // "keys/000003.json": the removed/replaced value
	AfterSHA256   string `json:"after_sha256,omitempty"`
	// Intended marks AfterSHA256 as recorded at intent time: the hash of the value the action
	// will write ("" with Intended = the action deletes the key).
	Intended bool `json:"intended,omitempty"`
}

// TimeString formats t the way manifests do: UTC, millisecond precision, "Z".
func TimeString(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// BackupID formats the backup id of a run started at t: basic ISO 8601 UTC, Windows-safe.
// A clash within the same second appends "-2", "-3" (done by the backup package).
func BackupID(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// Ledger is `<state>/setup/ledger.json`: what wy currently has installed (setup.md 9.3).
type Ledger struct {
	V          int                        `json:"v"` // 1
	Plugin     *LedgerPlugin              `json:"plugin,omitempty"`
	StatusLine map[string]LedgerStatus    `json:"statusline,omitempty"` // per settings target
	EnvKeys    []string                   `json:"env_keys,omitempty"`   // targets where wy set CLAUDE_CODE_PLUGIN_CACHE_DIR
	Launchers  []LedgerLauncher           `json:"launchers,omitempty"`
	MCPSync    []string                   `json:"mcp_sync,omitempty"` // state files in the sync set
	Copies     []string                   `json:"copies,omitempty"`   // Windows synced copies
	HubService *LedgerService             `json:"hub_service,omitempty"`
	Timer      bool                       `json:"timer,omitempty"`
	Terminal   *LedgerTerminal            `json:"terminal,omitempty"` // terminal.md T10
	Extra      map[string]json.RawMessage `json:"extra,omitempty"`    // forward-compatible slot
}

// LedgerPlugin records the plugin install.
type LedgerPlugin struct {
	Marketplace string   `json:"marketplace,omitempty"` // path of <state>/plugin/marketplace
	ID          string   `json:"id"`                    // "watchyard@wy-local"
	Roots       []string `json:"roots,omitempty"`
	Targets     []string `json:"targets,omitempty"` // settings targets it was enabled in
	Adopted     bool     `json:"adopted,omitempty"` // an existing install from another marketplace; not ours to remove
}

// LedgerStatus is one settings target's status line record: the original command, or Created.
type LedgerStatus struct {
	Original string `json:"original,omitempty"`
	Created  bool   `json:"created,omitempty"` // wy created statusLine (there was none)
}

// LedgerLauncher is a launcher wy wrote and the hash it had, to tell if it is still ours.
type LedgerLauncher struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// LedgerService records the hub user service.
type LedgerService struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
}
