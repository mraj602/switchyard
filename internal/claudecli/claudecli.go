// Package claudecli wraps the `claude` command line that `wy setup` and `wy account` call:
// `claude --version`, `claude auth status|login`, `claude plugin marketplace
// add|remove|update|list`, `claude plugin install|uninstall|enable|disable|list`, and
// `claude agents --json`. Callers depend on the Runner interface; Exec is the os/exec
// implementation and Fake the in-memory one for unit tests.
//
// Rules every Runner follows (setup.md 5.2, identity.md 3):
//   - every call takes the config folder and sets CLAUDE_CONFIG_DIR to it, except for the
//     default ~/.claude (Folder.Unset), where the variable is removed from the environment;
//   - stdin is closed (except AuthLogin, which inherits the terminal);
//   - each call has a 60 s timeout (DefaultTimeout), shorter if the context says so;
//   - commands that take --json have the JSON line parsed into Result;
//   - the real runner never reads .credentials.json.
//
// Contract: docs/spec/setup.md sections 3, 5.2, 8.2, 13.
package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"
)

// DefaultTimeout bounds one non-interactive `claude` call.
const DefaultTimeout = 60 * time.Second

// ErrNotFound means the claude binary was not found (setup exits 7).
var ErrNotFound = errors.New("claudecli: claude not found")

// Folder names the config folder a call runs as.
type Folder struct {
	Dir   string // absolute config folder, symlinks not resolved
	Unset bool   // true for ~/.claude with CLAUDE_CONFIG_DIR unset (identity.Slot.Default)
}

// String is the folder as shown in errors and the fake's call log.
func (f Folder) String() string {
	if f.Unset {
		return f.Dir + " (default)"
	}
	return f.Dir
}

// Environ returns base with CLAUDE_CONFIG_DIR set to f.Dir, or removed when f.Unset.
// base is not modified. Variable names are compared exactly, except on Windows, where they
// are case-insensitive (so `claude_config_dir=` is removed too).
func (f Folder) Environ(base []string) []string {
	return f.environ(runtime.GOOS, base)
}

func (f Folder) environ(goos string, base []string, drop ...string) []string {
	out := stripEnv(goos, base, append([]string{"CLAUDE_CONFIG_DIR"}, drop...)...)
	if !f.Unset {
		out = append(out, "CLAUDE_CONFIG_DIR="+f.Dir)
	}
	return out
}

// authEnvVars are login credentials Claude Code takes from the environment ahead of the
// folder's own login. `claude auth status|login` run without them, so they report and change
// the folder's login, not the shell's.
var authEnvVars = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// authEnviron is Environ for `claude auth ...`: also without authEnvVars.
func (f Folder) authEnviron(goos string, base []string) []string {
	return f.environ(goos, base, authEnvVars...)
}

// stripEnv returns base without the variables named in names. base is not modified. On
// Windows (goos "windows") names match case-insensitively, as the OS treats them.
func stripEnv(goos string, base []string, names ...string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, n := range names {
			if k == n || (goos == "windows" && strings.EqualFold(k, n)) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// Stdio is the terminal handed to an interactive command (AuthLogin).
type Stdio struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// VersionInfo is `claude --version`.
type VersionInfo struct {
	Path    string // binary path used
	Version string // "2.1.296"
	Raw     string // "2.1.296 (Claude Code)"
}

// AuthStatus is `claude auth status --json`.
type AuthStatus struct {
	LoggedIn bool            `json:"loggedIn"`
	Email    string          `json:"email,omitempty"`
	Org      string          `json:"orgName,omitempty"`
	Plan     string          `json:"subscriptionType,omitempty"`
	Method   string          `json:"authMethod,omitempty"`
	Raw      json.RawMessage `json:"-"` // the full JSON line, for diagnostics (never printed by wy)
}

// Result is the parsed `--json` outcome line of a plugin command.
//
// Claude Code 2.1.296 prints one line per command, for example
//
//	{"command":"install","outcome":"ok","plugin":"p@m","pluginId":"p@m","scope":"user","message":"..."}
//	{"command":"marketplace-add","outcome":"ok","marketplace":"m","message":"..."}
//
// and, when the command changed nothing because the goal already holds, exits 1 with
//
//	{"command":"enable","outcome":"failed","plugin":"p@m","message":"Plugin \"p@m\" is already enabled",
//	 "failureCode":"already_in_goal_state","alreadyInGoalState":true}
//
// (enable and disable; other failureCodes seen: not_installed for uninstall, not_found for
// install, not_configured for `marketplace remove` of an unknown name). Exec turns the
// already-in-goal answers into a nil error with AlreadyInGoalState set, so every caller is
// idempotent without looking at exit codes.
type Result struct {
	Outcome            string          // "ok" on success; "failed" with AlreadyInGoalState
	Message            string          // human message from the JSON line, if any
	Command            string          // "install", "enable", "marketplace-add", ... (if printed)
	FailureCode        string          // "already_in_goal_state", "not_installed", ... (if printed)
	AlreadyInGoalState bool            // nothing to do: the command's goal already held
	Raw                json.RawMessage // the JSON line
}

// OK reports whether the command succeeded or had nothing to do (idempotent success).
func (r Result) OK() bool { return r.Outcome == "ok" || r.AlreadyInGoalState }

// PluginInfo is one row of `claude plugin list --json`.
type PluginInfo struct {
	ID      string `json:"id"` // "watchyard@wy-local"
	Version string `json:"version,omitempty"`
	Enabled bool   `json:"enabled"`
	// Scope is where this install lives: "user" (or empty), "project" or "local". A plugin
	// installed in two scopes is two rows.
	Scope string `json:"scope,omitempty"`
	// ProjectPath is the project a "project" or "local" install belongs to; Claude Code only
	// uninstalls it with that folder as the working directory.
	ProjectPath string `json:"projectPath,omitempty"`
	InstallPath string `json:"installPath,omitempty"`
}

// UserScope reports whether the install is in the user scope (`claude plugin uninstall`'s
// default); an empty scope is the user's.
func (p PluginInfo) UserScope() bool { return p.Scope == "" || p.Scope == "user" }

// MarketplaceInfo is one row of `claude plugin marketplace list --json`.
type MarketplaceInfo struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"` // directory path or repo
}

// Agent is one row of `claude agents --json` (a live or recent session).
type Agent struct {
	ID     string          `json:"id,omitempty"`
	Status string          `json:"status,omitempty"`
	Raw    json.RawMessage `json:"-"`
}

// Error is a failed claude call.
type Error struct {
	Args     []string // arguments after "claude"
	ExitCode int
	Outcome  string // JSON outcome if one was printed
	Message  string // JSON message or the first stderr line
	// FailureCode is the JSON failureCode if one was printed ("not_found", "not_installed", ...).
	FailureCode string
}

func (e *Error) Error() string {
	return fmt.Sprintf("claude %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, e.Message)
}

// Runner is the `claude` command line. All methods are safe for concurrent use.
type Runner interface {
	// Version runs `claude --version` (CLAUDE_CONFIG_DIR as inherited).
	Version(ctx context.Context) (VersionInfo, error)
	// AuthStatus runs `claude auth status --json` as folder f.
	AuthStatus(ctx context.Context, f Folder) (AuthStatus, error)
	// AuthLogin runs `claude auth login --claudeai` as f with the terminal inherited (8.2).
	// It returns when the command exits; callers confirm with AuthStatus.
	AuthLogin(ctx context.Context, f Folder, tty Stdio) error

	// PluginMarketplaceAdd adds a marketplace from source (a local directory) as f.
	PluginMarketplaceAdd(ctx context.Context, f Folder, source string) (Result, error)
	PluginMarketplaceRemove(ctx context.Context, f Folder, name string) (Result, error)
	PluginMarketplaceUpdate(ctx context.Context, f Folder, name string) (Result, error)
	PluginMarketplaceList(ctx context.Context, f Folder) ([]MarketplaceInfo, error)

	// PluginInstall installs id ("name@marketplace") as f. Uninstall removes the record for
	// every folder sharing f's plugin root. Enable/Disable change enabledPlugins in f's
	// settings target only.
	PluginInstall(ctx context.Context, f Folder, id string) (Result, error)
	// PluginUpdate updates an installed plugin to its marketplace's current version as f
	// (`wy update`'s post-update step, setup.md 5.2).
	PluginUpdate(ctx context.Context, f Folder, id string) (Result, error)
	PluginUninstall(ctx context.Context, f Folder, id string) (Result, error)
	// PluginUninstallScoped removes the install of id that PluginList reported with scope and
	// projectPath: `claude plugin uninstall id --scope <scope>`, run with projectPath as the
	// working directory for the "project" and "local" scopes (Claude Code finds those installs
	// by the folder). An empty or "user" scope is PluginUninstall.
	PluginUninstallScoped(ctx context.Context, f Folder, id, scope, projectPath string) (Result, error)
	PluginEnable(ctx context.Context, f Folder, id string) (Result, error)
	PluginDisable(ctx context.Context, f Folder, id string) (Result, error)
	PluginList(ctx context.Context, f Folder) ([]PluginInfo, error)

	// Agents runs `claude agents --json` as f.
	Agents(ctx context.Context, f Folder) ([]Agent, error)
}
