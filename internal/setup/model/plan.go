package model

import (
	"encoding/json"
	"os"
	"strconv"
)

// Op is an action type (setup.md 6.1).
type Op string

// The ops of setup.md 6.1.
const (
	OpMkdir           Op = "mkdir"
	OpLink            Op = "link"
	OpReplaceWithLink Op = "replace_with_link"
	OpMove            Op = "move"
	OpMoveToBackup    Op = "move_to_backup"
	OpMergeLines      Op = "merge_lines"
	OpJSONSet         Op = "json_set"
	OpJSONDelete      Op = "json_delete"
	OpMCPSync         Op = "mcp_sync"
	OpWriteFile       Op = "write_file"
	OpWriteTree       Op = "write_tree"
	OpRun             Op = "run"
	OpService         Op = "service"
	OpConnect         Op = "connect"
	OpConfigSet       Op = "config_set"
	OpLogin           Op = "login"
)

// Step groups actions in apply order (6.2 step 3, then 5-7). A Plan's Actions are sorted by
// step in this order; the text output uses one section per step.
type Step string

// Steps in apply order.
const (
	StepFolders    Step = "folders"
	StepSharing    Step = "sharing"
	StepPlugin     Step = "plugin"
	StepStatusLine Step = "statusline"
	StepMCP        Step = "mcp"
	StepLaunchers  Step = "launchers"
	StepConfig     Step = "config"
	StepHub        Step = "hub"
	StepPrototype  Step = "prototype"
	StepLogin      Step = "login"
)

// StepOrder lists the steps in apply order.
var StepOrder = []Step{
	StepFolders, StepSharing, StepPlugin, StepStatusLine, StepMCP, StepLaunchers, StepTerminal,
	StepConfig, StepHub, StepPrototype, StepLogin,
}

// Local reports whether s belongs to the local steps, whose failure rolls everything
// back (6.2 steps 3-4). Hub, prototype and login steps never roll the local steps back.
func (s Step) Local() bool {
	switch s {
	case StepHub, StepPrototype, StepLogin:
		return false
	}
	return true
}

// Link kinds.
const (
	LinkSymlink  = "symlink"
	LinkJunction = "junction"
	LinkCopy     = "copy"
)

// PathType is the type of a path in a PathState.
type PathType string

// Path types (7.2).
const (
	TypeAbsent   PathType = "absent"
	TypeFile     PathType = "file"
	TypeDir      PathType = "dir"
	TypeSymlink  PathType = "symlink"
	TypeJunction PathType = "junction"
	TypeCopy     PathType = "copy"
)

// PathState is what is (or was, or must be) at a path: manifest `before`/`after` (7.2) and
// action preconditions (6.1). Zero Size/MtimeNS/SHA256 mean "not recorded / not checked".
type PathState struct {
	Type    PathType `json:"type"`
	Mode    string   `json:"mode,omitempty"` // permission bits as "0644" (ModeString)
	Size    int64    `json:"size,omitempty"`
	MtimeNS int64    `json:"mtime_ns,omitempty"`
	SHA256  string   `json:"sha256,omitempty"`
	// Target is the raw readlink string of a link, so relative links come back relative.
	Target string `json:"target,omitempty"`
	// Saved is the path of the saved bytes/tree inside the backup set ("files/000003").
	Saved string `json:"saved,omitempty"`
}

// ModeString formats permission bits as the four-digit octal string used in manifests.
func ModeString(m os.FileMode) string {
	s := strconv.FormatUint(uint64(m.Perm()), 8)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// ParseMode parses a ModeString value.
func ParseMode(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	return os.FileMode(v), err
}

// Precondition is the exact state an action expects to find at Path right before it acts.
// Apply re-checks every precondition and aborts (exit 4) on a mismatch.
type Precondition struct {
	Path  string    `json:"path"`
	State PathState `json:"state"`
}

// NameChanges are the server names an mcp_sync adds, changes and removes (names only).
type NameChanges struct {
	Added   []string `json:"added,omitempty"`
	Changed []string `json:"changed,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// Action is one planned change. One struct with the union of every op's fields (6.1); which
// fields an op uses is listed in setup.md 6.1. Unused fields are omitted from JSON.
type Action struct {
	ID   string `json:"id"` // "a1", "a2", ... in apply order
	Step Step   `json:"step"`
	Op   Op     `json:"op"`

	Path   string `json:"path,omitempty"`   // mkdir link replace_with_link move_to_backup merge_lines write_file write_tree
	Target string `json:"target,omitempty"` // link, replace_with_link: absolute link target
	Kind   string `json:"kind,omitempty"`   // link, replace_with_link: Link* constant
	Mode   string `json:"mode,omitempty"`   // mkdir, write_file: "0700"
	From   string `json:"from,omitempty"`   // move, merge_lines (history.jsonl being merged in)
	To     string `json:"to,omitempty"`     // move

	File    string          `json:"file,omitempty"`    // json_*: resolved target file
	Via     []string        `json:"via,omitempty"`     // json_*: link paths that lead to File
	Pointer string          `json:"pointer,omitempty"` // json_*: RFC 6901 pointer
	Before  json.RawMessage `json:"before,omitempty"`  // json_*: current value (absent = key missing); redacted in output for env and mcpServers
	Value   json.RawMessage `json:"value,omitempty"`   // json_set, config_set: new value; same redaction
	Covers  []string        `json:"covers,omitempty"`  // folders a shared target change covers

	Files []string     `json:"files,omitempty"` // mcp_sync: state files
	Names *NameChanges `json:"names,omitempty"` // mcp_sync

	ContentSHA256 string `json:"content_sha256,omitempty"` // write_file

	Argv    []string           `json:"argv,omitempty"`    // run
	Env     map[string]*string `json:"env,omitempty"`     // run: CLAUDE_CONFIG_DIR etc; null value = unset
	Touches []string           `json:"touches,omitempty"` // run: files saved before (never a .claude.json)
	Watch   []string           `json:"watch,omitempty"`   // run: folders diffed after

	Name string `json:"name,omitempty"` // service
	Unit string `json:"unit,omitempty"` // service: unit file path
	// Listen is the hub service's `--listen` address when --listen was given; empty means
	// the service default (127.0.0.1:8787).
	Listen string `json:"listen,omitempty"`
	URL    string `json:"url,omitempty"` // connect (the token is never in the plan)
	// connect: hub.dial (forward mode) and hub.reach ("pull": no URL, no token).
	Dial  string `json:"dial,omitempty"`
	Reach string `json:"reach,omitempty"`
	Key   string `json:"key,omitempty"` // config_set

	Folder string `json:"folder,omitempty"` // login

	// Note is a one-line human reason (the text after a `!` or `·`), safe to print.
	Note string `json:"note,omitempty"`
	// Pre lists the state expected at every path the action touches.
	Pre []Precondition `json:"pre,omitempty"`
}

// Keep is something found already set up and left alone (the `keep` array).
type Keep struct {
	What   string `json:"what"` // "link", "launcher", "plugin", ...
	Path   string `json:"path,omitempty"`
	Target string `json:"target,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Conflict is an item that differs from the primary and how it will be resolved (5.3).
type Conflict struct {
	Folder     string   `json:"folder"`
	Item       string   `json:"item"`
	Resolution string   `json:"resolution"` // Resolve* constant
	Choices    []string `json:"choices"`
}

// Need is something only the person can do (a login, a token).
type Need struct {
	Kind    string `json:"kind"` // "login", "hub-token", ...
	Folder  string `json:"folder,omitempty"`
	Command string `json:"command,omitempty"`
}

// Summary is the last line of the plan (6.3). LoginsTouched is always 0.
type Summary struct {
	Changes       int `json:"changes"`
	BackedUp      int `json:"backed_up"`
	LinksChanged  int `json:"links_changed"`
	LoginsTouched int `json:"logins_touched"`
}

// Plan is the output of the pure planner (setup.md 6): ordered actions plus everything the
// renderers need. PlanID is "p_" + 12 hex of SHA-256 over the canonical JSON of Actions and
// Conflicts including preconditions.
type Plan struct {
	PlanID    string     `json:"plan_id"`
	Actions   []Action   `json:"actions"`
	Keep      []Keep     `json:"keep"`
	Conflicts []Conflict `json:"conflicts"`
	Needs     []Need     `json:"needs"`
	Summary   Summary    `json:"summary"`
	// Warnings are `!` lines that are not conflicts (a second switchyard plugin disabled, ...).
	Warnings []string `json:"warnings,omitempty"`
}

// Empty reports whether the plan has no actions (a rerun with the same answers: "0 changes",
// no backup set). Logins are Needs, not actions.
func (p Plan) Empty() bool { return len(p.Actions) == 0 }

// Report modes.
const (
	ModeDryRun     = "dry-run"
	ModeApplied    = "applied"
	ModeRolledBack = "rolled-back"
	// ModePartial: the local steps applied but the hub step failed (exit 6, setup.md 6.2).
	ModePartial = "partly-applied"
)

// Report is the `--json` object of setup.md 6.3: the plan's fields inline, plus the envelope.
type Report struct {
	V       int    `json:"v"` // always 1
	Mode    string `json:"mode"`
	Primary string `json:"primary"`
	State   *State `json:"state,omitempty"`
	Plan
	// After an apply:
	Backup  string          `json:"backup,omitempty"`
	Applied []string        `json:"applied,omitempty"` // action ids
	Hub     *HubResult      `json:"hub,omitempty"`
	Doctor  json.RawMessage `json:"doctor,omitempty"` // `sy doctor --json`
	// Uninstall is the part of a `sy uninstall` report outside the journaled plan.
	Uninstall *UninstallSteps `json:"uninstall,omitempty"`
}

// UninstallSteps are the `sy uninstall` steps that are not plan actions (setup.md 9.5 step
// 3): listed in the plan shown before the confirmation, done after the plan applied.
type UninstallSteps struct {
	Timer      bool   `json:"timer,omitempty"`       // the OS timer that runs sy tick is removed
	HubService string `json:"hub_service,omitempty"` // the hub service of this name is removed
	// AgentService is set when the peers agent's unit (`sy agent enable`) exists here; it is
	// found by its file, not the ledger, and removed with the hub service.
	AgentService string   `json:"agent_service,omitempty"`
	Copies       []string `json:"copies,omitempty"` // synced copies that stop being synced (they stay)
	Purge        bool     `json:"purge,omitempty"`  // --purge: config.toml and the state folder
	Failed       []string `json:"failed,omitempty"` // after the run: steps that failed (kept in the ledger)
}

// HubResult is the `hub` object of an applied report.
type HubResult struct {
	Mode    string `json:"mode"` // Hub* constant
	URL     string `json:"url,omitempty"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"` // set when the hub step failed (exit 6)
	// Step is the op of the failed hub step ("service" or "connect"); Fix says what to do
	// next. Both only with Error.
	Step string `json:"step,omitempty"`
	Fix  string `json:"fix,omitempty"`
}
