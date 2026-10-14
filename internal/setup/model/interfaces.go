package model

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mraj602/switchyard/internal/claudecli"
)

// ErrNotImplemented is returned by the Unimplemented* stand-ins that wave 1 replaces.
var ErrNotImplemented = errors.New("setup: not implemented")

// Sharer is the file-system and JSON editing side of apply, implemented by internal/share
// (WP-C) and internal/jsonedit through it. Every method is atomic with respect to its path
// (temp name + rename), refuses to follow a path through a forbidden file (.credentials.json),
// and never touches the login. Callers (internal/setup/apply) save originals into the backup
// set *before* calling, unless a method takes a save location.
type Sharer interface {
	// Link makes path a link to target (absolute; must exist). kind is a Link* constant.
	// A symlink is created at a temp name next to path and renamed over it, so path is never
	// absent when it already existed. LinkCopy copies target to path (Windows synced copy).
	Link(path, target, kind string) error
	// ReplaceWithLink replaces the existing item at path with a link to target. If saveTo is
	// not empty the original is first moved there (rename; copy+delete across file systems);
	// if empty the caller has already saved it and the original is discarded.
	ReplaceWithLink(path, target, kind, saveTo string) error
	// Move renames from to to (copy+delete across file systems). to must not exist.
	Move(from, to string) error
	// MergeLines merges the history.jsonl at from into path: union of lines de-duplicated by
	// exact bytes, ordered by each line's "timestamp" field (lines without one keep their
	// relative order at the end). path is rewritten; from is left for the caller to link.
	MergeLines(path, from string) error
	// MergeDir moves every entry of src missing from dst into dst, drops entries identical in
	// both from src, and leaves entries that differ in src (5.3). saveDir receives originals
	// of dropped entries. The result lists what happened per entry for the journal.
	MergeDir(dst, src, saveDir string) (MergeResult, error)
	// JSONSet sets the value at an RFC 6901 pointer in file by byte-span splice (every other
	// byte unchanged), at the resolved target of a link chain, keeping the mode.
	JSONSet(file, pointer string, value json.RawMessage) (JSONResult, error)
	// JSONDelete removes the key at pointer (empty parents are not removed).
	JSONDelete(file, pointer string) (JSONResult, error)
	// MCPSync runs the three-way mcpServers merge of setup.md 5.4 over the request's files.
	MCPSync(ctx context.Context, req MCPSyncRequest) (MCPSyncResult, error)
	// Snapshot captures the PathState of path: lstat, raw link target, mode, mtime, and the
	// SHA-256 of regular files.
	Snapshot(path string) (PathState, error)
}

// MergeResult is what MergeDir did, entry names relative to the merged directory.
type MergeResult struct {
	Moved     []string `json:"moved,omitempty"`     // from src into dst
	Dropped   []string `json:"dropped,omitempty"`   // identical in both; src copy removed
	Conflicts []string `json:"conflicts,omitempty"` // differ; left in src (the folder is then not linked)
}

// JSONResult is what a JSONSet/JSONDelete changed, for the journal's key entries.
type JSONResult struct {
	Target        string          `json:"target"` // resolved file actually written
	BeforePresent bool            `json:"before_present"`
	Before        json.RawMessage `json:"before,omitempty"` // value that was replaced/removed
	Changed       bool            `json:"changed"`          // false when the file already had that value
}

// MCPFile is one state file in the MCP sync set.
type MCPFile struct {
	Folder    string `json:"folder"`     // "~" form
	StateFile string `json:"state_file"` // absolute path of the .claude.json
	Dir       string `json:"dir"`        // the config folder (for the live-session check)
	Primary   bool   `json:"primary,omitempty"`
}

// MCPSyncRequest asks for one sync pass.
type MCPSyncRequest struct {
	Files    []MCPFile `json:"files"`
	StateDir string    `json:"state_dir"`         // for share/mcp-base.json and mcp-conflicts.jsonl
	DryRun   bool      `json:"dry_run,omitempty"` // compute names, write nothing
	Now      bool      `json:"now,omitempty"`     // `sy share sync --now`: ignore the live-session skip
}

// MCPSyncResult reports a sync pass. It never contains server values.
type MCPSyncResult struct {
	Written   []string    `json:"written,omitempty"` // state files rewritten
	Skipped   []MCPSkip   `json:"skipped,omitempty"`
	Names     NameChanges `json:"names"`
	Conflicts []string    `json:"conflicts,omitempty"` // names where the primary won
	// Guard is "would-remove-all" when the pass was refused: it would have removed every
	// server of the base from the folders that still have them (nothing written).
	Guard string `json:"guard,omitempty"`
}

// MCPSkip is a file left for the next sync.
type MCPSkip struct {
	File   string `json:"file"`
	Reason string `json:"reason"` // "live-session", "locked", "changed-while-writing", ...
}

// PluginInstaller installs and removes the switchyard plugin (setup.md 5.2), implemented by
// internal/setup/pluginmkt (WP-D) on a claudecli.Runner. All calls run as the given folder.
type PluginInstaller interface {
	// WriteMarketplace writes <dir> (the marketplace tree: .claude-plugin/marketplace.json
	// "sy-local" and plugins/switchyard) from the embedded plugin with hook commands rewritten
	// to the absolute syPath, into a temp folder then renamed over dir.
	WriteMarketplace(dir, syPath, version string) error
	// Install runs `marketplace add <dir>` then `install switchyard@sy-local` as folder.
	Install(ctx context.Context, folder claudecli.Folder, marketplaceDir string) error
	// Enable enables switchyard@sy-local in folder's settings target (shared root, own settings).
	Enable(ctx context.Context, folder claudecli.Folder) error
	// Disable disables the plugin id (a duplicate switchyard@* ) in folder's settings target.
	Disable(ctx context.Context, folder claudecli.Folder, id string) error
	// Uninstall runs `uninstall switchyard@sy-local` then `marketplace remove sy-local` once
	// for the root folder belongs to.
	Uninstall(ctx context.Context, folder claudecli.Folder) error
}

// ServiceSpec describes the hub user service (setup.md step 4, `--hub here`).
type ServiceSpec struct {
	Name   string   `json:"name"`             // "switchyard-hub"
	Unit   string   `json:"unit,omitempty"`   // unit/plist/XML path
	Listen string   `json:"listen,omitempty"` // 127.0.0.1:8787
	Args   []string `json:"args,omitempty"`   // sy hub serve ...
}

// ServiceStatus is the state of a user service.
type ServiceStatus struct {
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
	// LingerOff is true on Linux when `loginctl show-user $USER -p Linger` is "no".
	LingerOff bool `json:"linger_off,omitempty"`
}

// Service installs the hub user service (systemd user unit, launchd agent, Task Scheduler),
// implemented by internal/service (WP-D).
type Service interface {
	Install(ctx context.Context, spec ServiceSpec) error
	Uninstall(ctx context.Context, name string) error
	Status(ctx context.Context, name string) (ServiceStatus, error)
}

// ConnectRequest is `sy connect` as the hub step uses it. Token is read from stdin by the
// command layer and passed here; it is never logged, journaled or put in a plan.
type ConnectRequest struct {
	URL   string
	Token string
	// Phase 5: Dial and Reach for hub.dial and hub.reach; Pull sets hub.reach = "pull" with no
	// URL and no token and installs the timer (machines.md 3.4).
	Dial        string
	Reach       string
	Pull        bool
	InsecureLAN bool
}

// ConnectResult is the outcome of Connect.
type ConnectResult struct {
	Healthy bool   // /health answered
	Timer   bool   // the flush timer was installed
	Message string // human line, no secrets
}

// Connector wraps internal/client's connect (tokens.md 4): HTTPS rule, /health, token
// proof, <state>/token, hub.url, timer. Implemented by the command layer (WP-F).
type Connector interface {
	Connect(ctx context.Context, req ConnectRequest) (ConnectResult, error)
	// MintMachineToken makes a machine token against the local hub data folder
	// (`sy hub token create --kind machine`) for `--hub here`.
	MintMachineToken(ctx context.Context, machine string) (string, error)
	// WaitHealthy polls url's /health for up to the given seconds.
	WaitHealthy(ctx context.Context, url string, seconds int) error
}

// UnimplementedSharer implements Sharer with ErrNotImplemented everywhere.
type UnimplementedSharer struct{}

var _ Sharer = UnimplementedSharer{}

func (UnimplementedSharer) Link(string, string, string) error { return ErrNotImplemented }
func (UnimplementedSharer) ReplaceWithLink(string, string, string, string) error {
	return ErrNotImplemented
}
func (UnimplementedSharer) Move(string, string) error       { return ErrNotImplemented }
func (UnimplementedSharer) MergeLines(string, string) error { return ErrNotImplemented }
func (UnimplementedSharer) MergeDir(string, string, string) (MergeResult, error) {
	return MergeResult{}, ErrNotImplemented
}
func (UnimplementedSharer) JSONSet(string, string, json.RawMessage) (JSONResult, error) {
	return JSONResult{}, ErrNotImplemented
}
func (UnimplementedSharer) JSONDelete(string, string) (JSONResult, error) {
	return JSONResult{}, ErrNotImplemented
}
func (UnimplementedSharer) MCPSync(context.Context, MCPSyncRequest) (MCPSyncResult, error) {
	return MCPSyncResult{}, ErrNotImplemented
}
func (UnimplementedSharer) Snapshot(string) (PathState, error) { return PathState{}, ErrNotImplemented }

// UnimplementedPluginInstaller implements PluginInstaller with ErrNotImplemented.
type UnimplementedPluginInstaller struct{}

var _ PluginInstaller = UnimplementedPluginInstaller{}

func (UnimplementedPluginInstaller) WriteMarketplace(string, string, string) error {
	return ErrNotImplemented
}
func (UnimplementedPluginInstaller) Install(context.Context, claudecli.Folder, string) error {
	return ErrNotImplemented
}
func (UnimplementedPluginInstaller) Enable(context.Context, claudecli.Folder) error {
	return ErrNotImplemented
}
func (UnimplementedPluginInstaller) Disable(context.Context, claudecli.Folder, string) error {
	return ErrNotImplemented
}
func (UnimplementedPluginInstaller) Uninstall(context.Context, claudecli.Folder) error {
	return ErrNotImplemented
}

// UnimplementedService implements Service with ErrNotImplemented.
type UnimplementedService struct{}

var _ Service = UnimplementedService{}

func (UnimplementedService) Install(context.Context, ServiceSpec) error { return ErrNotImplemented }
func (UnimplementedService) Uninstall(context.Context, string) error    { return ErrNotImplemented }
func (UnimplementedService) Status(context.Context, string) (ServiceStatus, error) {
	return ServiceStatus{}, ErrNotImplemented
}

// UnimplementedConnector implements Connector with ErrNotImplemented.
type UnimplementedConnector struct{}

var _ Connector = UnimplementedConnector{}

func (UnimplementedConnector) Connect(context.Context, ConnectRequest) (ConnectResult, error) {
	return ConnectResult{}, ErrNotImplemented
}
func (UnimplementedConnector) MintMachineToken(context.Context, string) (string, error) {
	return "", ErrNotImplemented
}
func (UnimplementedConnector) WaitHealthy(context.Context, string, int) error {
	return ErrNotImplemented
}
