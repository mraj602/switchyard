package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Call is one recorded call on a Fake.
type Call struct {
	Method string   // Runner method name, e.g. "PluginInstall"
	Folder Folder   // zero for Version
	Args   []string // the method's non-folder arguments
}

// Fake is an in-memory Runner for unit tests. It models just enough of Claude Code 2.1.296
// (setup.md 5.2): a plugin root per folder (default: the folder itself; Link makes folders
// share one), a settings target per folder (default: the folder itself; also Link-able),
// marketplaces and installs recorded per root, `enabledPlugins` per settings target, and a
// login per folder. It does not touch the file system, except to read the marketplace name
// from <source>/.claude-plugin/marketplace.json when that file exists.
//
// Semantics that matter to callers, as verified against the real CLI:
//   - Install records the plugin in the folder's root and enables it in the folder's settings target.
//   - Uninstall removes the record for every folder sharing the root; other settings targets
//     keep their enabledPlugins entry.
//   - Enable/Disable only change the folder's settings target, and fail if not installed.
//
// The zero value is not usable; call NewFake. Safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	// VersionRaw is returned by Version (default "2.1.296 (Claude Code)").
	VersionRaw string
	// DefaultLoginEmail is used by AuthLogin when the terminal input has no line.
	DefaultLoginEmail string

	calls   []Call
	fails   map[string]error
	auth    map[string]AuthStatus
	roots   map[string]string            // folder dir -> root key
	targets map[string]string            // folder dir -> settings target key
	markets map[string]map[string]string // root -> marketplace name -> source
	plugins map[string]map[string]string // root -> id -> installPath (user scope)
	scoped  map[string][]PluginInfo      // root -> installs in the project and local scopes
	enabled map[string]map[string]bool   // settings target -> id -> enabled
	extra   map[string]map[string]bool   // settings target -> extraKnownMarketplaces
	agents  map[string][]Agent
}

var _ Runner = (*Fake)(nil)

// NewFake returns an empty Fake: no folder is logged in, nothing is installed.
func NewFake() *Fake {
	return &Fake{
		VersionRaw:        "2.1.296 (Claude Code)",
		DefaultLoginEmail: "fake@example.com",
		fails:             map[string]error{},
		auth:              map[string]AuthStatus{},
		roots:             map[string]string{},
		targets:           map[string]string{},
		markets:           map[string]map[string]string{},
		plugins:           map[string]map[string]string{},
		scoped:            map[string][]PluginInfo{},
		enabled:           map[string]map[string]bool{},
		extra:             map[string]map[string]bool{},
		agents:            map[string][]Agent{},
	}
}

func key(dir string) string { return filepath.Clean(dir) }

// SetLogin marks the folder dir as logged in as email (org and plan optional).
func (f *Fake) SetLogin(dir, email, org, plan string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth[key(dir)] = AuthStatus{LoggedIn: true, Email: email, Org: org, Plan: plan, Method: "claude.ai"}
}

// LinkRoot makes folder dir use the same plugin root as owner (its `plugins/` is a link to
// the owner's, or both use CLAUDE_CODE_PLUGIN_CACHE_DIR).
func (f *Fake) LinkRoot(dir, owner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots[key(dir)] = f.rootOf(owner)
}

// LinkSettings makes folder dir share owner's settings.json (and so its enabledPlugins).
func (f *Fake) LinkSettings(dir, owner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets[key(dir)] = f.targetOf(owner)
}

// SetAgents sets what `claude agents --json` returns for dir.
func (f *Fake) SetAgents(dir string, agents []Agent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents[key(dir)] = agents
}

// FailOn makes every later call of the Runner method (e.g. "PluginInstall") return err.
// A nil err clears it.
func (f *Fake) FailOn(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fails, method)
		return
	}
	f.fails[method] = err
}

// Calls returns a copy of the recorded calls, oldest first.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CallsTo returns the recorded calls of one method.
func (f *Fake) CallsTo(method string) []Call {
	var out []Call
	for _, c := range f.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Installed returns the sorted plugin ids recorded in the plugin root of folder dir.
func (f *Fake) Installed(dir string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sortedKeys(f.plugins[f.rootOf(dir)])
}

// AddScopedInstall records an install of id in the "project" or "local" scope of projectPath
// in dir's plugin root, as Claude Code does when the plugin was installed from a project.
// Only PluginUninstallScoped with that scope removes it; the user-scope PluginUninstall fails
// as the real CLI does.
func (f *Fake) AddScopedInstall(dir, id, scope, projectPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	root := f.rootOf(dir)
	f.scoped[root] = append(f.scoped[root], PluginInfo{ID: id, Version: "fake", Enabled: true,
		Scope: scope, ProjectPath: projectPath, InstallPath: filepath.Join(dir, "plugins", "cache", id)})
}

// ScopedInstalls returns the "project" and "local" installs recorded in dir's plugin root.
func (f *Fake) ScopedInstalls(dir string) []PluginInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PluginInfo(nil), f.scoped[f.rootOf(dir)]...)
}

// Enabled reports whether id is enabled in dir's settings target.
func (f *Fake) Enabled(dir, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled[f.targetOf(dir)][id]
}

// Marketplaces returns the sorted marketplace names known in dir's plugin root.
func (f *Fake) Marketplaces(dir string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sortedKeys(f.markets[f.rootOf(dir)])
}

// InstallPath returns the installPath recorded for id in dir's root ("" if absent). Like the
// real CLI it is the path through the folder that last installed it.
func (f *Fake) InstallPath(dir, id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plugins[f.rootOf(dir)][id]
}

func (f *Fake) rootOf(dir string) string {
	if r, ok := f.roots[key(dir)]; ok {
		return r
	}
	return key(dir)
}

func (f *Fake) targetOf(dir string) string {
	if t, ok := f.targets[key(dir)]; ok {
		return t
	}
	return key(dir)
}

// begin records a call and returns an injected failure, or a context error.
func (f *Fake) begin(ctx context.Context, method string, fo Folder, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Method: method, Folder: fo, Args: args})
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.fails[method]
}

func failure(msg string, args ...string) error {
	return &Error{Args: args, ExitCode: 1, Outcome: "error", Message: msg}
}

// alreadyResult is what Exec returns when claude answers "nothing to do" (see Result).
func alreadyResult(command, code, msg string) Result {
	raw, _ := json.Marshal(map[string]any{"command": command, "outcome": "failed", "message": msg,
		"failureCode": code, "alreadyInGoalState": true})
	return Result{Outcome: "failed", Message: msg, Command: command, FailureCode: code, AlreadyInGoalState: true, Raw: raw}
}

func okResult(msg string) Result {
	raw, _ := json.Marshal(map[string]string{"outcome": "ok", "message": msg})
	return Result{Outcome: "ok", Message: msg, Raw: raw}
}

// Version implements Runner.
func (f *Fake) Version(ctx context.Context) (VersionInfo, error) {
	if err := f.begin(ctx, "Version", Folder{}); err != nil {
		return VersionInfo{}, err
	}
	raw := f.VersionRaw
	v, _, _ := strings.Cut(raw, " ")
	return VersionInfo{Path: "/fake/bin/claude", Version: v, Raw: raw}, nil
}

// AuthStatus implements Runner.
func (f *Fake) AuthStatus(ctx context.Context, fo Folder) (AuthStatus, error) {
	if err := f.begin(ctx, "AuthStatus", fo); err != nil {
		return AuthStatus{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth[key(fo.Dir)], nil
}

// AuthLogin implements Runner: it prints a prompt to tty.Out, reads one line from tty.In
// ("<email>" or "<email>:<org>"; empty or no input uses DefaultLoginEmail) and logs fo in.
func (f *Fake) AuthLogin(ctx context.Context, fo Folder, tty Stdio) error {
	if err := f.begin(ctx, "AuthLogin", fo); err != nil {
		return err
	}
	if tty.Out != nil {
		fmt.Fprint(tty.Out, "Paste code here if prompted > ")
	}
	line := ""
	if tty.In != nil {
		line, _ = bufio.NewReader(tty.In).ReadString('\n')
		line = strings.TrimSpace(line)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if line == "" {
		line = f.DefaultLoginEmail
	}
	email, org, _ := strings.Cut(line, ":")
	f.auth[key(fo.Dir)] = AuthStatus{LoggedIn: true, Email: email, Org: org, Method: "claude.ai"}
	return nil
}

// PluginMarketplaceAdd implements Runner. The marketplace name is read from
// <source>/.claude-plugin/marketplace.json, else it is the base name of source.
func (f *Fake) PluginMarketplaceAdd(ctx context.Context, fo Folder, source string) (Result, error) {
	if err := f.begin(ctx, "PluginMarketplaceAdd", fo, source); err != nil {
		return Result{}, err
	}
	name := filepath.Base(source)
	if b, err := os.ReadFile(filepath.Join(source, ".claude-plugin", "marketplace.json")); err == nil {
		var m struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &m) == nil && m.Name != "" {
			name = m.Name
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	root, target := f.rootOf(fo.Dir), f.targetOf(fo.Dir)
	if f.markets[root] == nil {
		f.markets[root] = map[string]string{}
	}
	f.markets[root][name] = source
	if f.extra[target] == nil {
		f.extra[target] = map[string]bool{}
	}
	f.extra[target][name] = true
	return okResult("added " + name), nil
}

// PluginMarketplaceRemove implements Runner.
func (f *Fake) PluginMarketplaceRemove(ctx context.Context, fo Folder, name string) (Result, error) {
	if err := f.begin(ctx, "PluginMarketplaceRemove", fo, name); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	root := f.rootOf(fo.Dir)
	if _, ok := f.markets[root][name]; !ok {
		return alreadyResult("marketplace-remove", "not_configured", "Marketplace '"+name+"' not found"), nil
	}
	delete(f.markets[root], name)
	delete(f.extra[f.targetOf(fo.Dir)], name)
	return okResult("removed " + name), nil
}

// PluginMarketplaceUpdate implements Runner.
func (f *Fake) PluginMarketplaceUpdate(ctx context.Context, fo Folder, name string) (Result, error) {
	if err := f.begin(ctx, "PluginMarketplaceUpdate", fo, name); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.markets[f.rootOf(fo.Dir)][name]; !ok {
		return Result{}, failure("marketplace not found: "+name, "plugin", "marketplace", "update", name)
	}
	return okResult("updated " + name), nil
}

// PluginMarketplaceList implements Runner.
func (f *Fake) PluginMarketplaceList(ctx context.Context, fo Folder) ([]MarketplaceInfo, error) {
	if err := f.begin(ctx, "PluginMarketplaceList", fo); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []MarketplaceInfo
	for _, n := range sortedKeys(f.markets[f.rootOf(fo.Dir)]) {
		out = append(out, MarketplaceInfo{Name: n, Source: f.markets[f.rootOf(fo.Dir)][n]})
	}
	return out, nil
}

func marketOf(id string) string {
	_, m, _ := strings.Cut(id, "@")
	return m
}

// PluginInstall implements Runner. The marketplace of id must be known in the root.
func (f *Fake) PluginInstall(ctx context.Context, fo Folder, id string) (Result, error) {
	if err := f.begin(ctx, "PluginInstall", fo, id); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	root, target := f.rootOf(fo.Dir), f.targetOf(fo.Dir)
	if _, ok := f.markets[root][marketOf(id)]; !ok {
		return Result{}, failure("marketplace not found: "+marketOf(id), "plugin", "install", id)
	}
	if f.plugins[root] == nil {
		f.plugins[root] = map[string]string{}
	}
	if _, ok := f.plugins[root][id]; ok && f.enabled[target][id] {
		// recorded and enabled for this folder: a no-op, the earlier installPath stays (2.1.296)
		return okResult("already installed " + id), nil
	}
	name, _, _ := strings.Cut(id, "@")
	f.plugins[root][id] = filepath.Join(fo.Dir, "plugins", "cache", marketOf(id), name, "fake")
	if f.enabled[target] == nil {
		f.enabled[target] = map[string]bool{}
	}
	f.enabled[target][id] = true
	return okResult("installed " + id), nil
}

// PluginUpdate implements Runner: the plugin must be recorded in f's root.
func (f *Fake) PluginUpdate(ctx context.Context, fo Folder, id string) (Result, error) {
	if err := f.begin(ctx, "PluginUpdate", fo, id); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.plugins[f.rootOf(fo.Dir)][id]; !ok {
		return Result{}, failure(fmt.Sprintf("Plugin %q not found in installed plugins", id), "plugin", "update", id)
	}
	return okResult("updated " + id), nil
}

// PluginUninstall implements Runner: the record goes for every folder sharing the root;
// enabledPlugins entries in settings targets stay.
func (f *Fake) PluginUninstall(ctx context.Context, fo Folder, id string) (Result, error) {
	if err := f.begin(ctx, "PluginUninstall", fo, id); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	root := f.rootOf(fo.Dir)
	if _, ok := f.plugins[root][id]; !ok {
		for _, p := range f.scoped[root] {
			if p.ID == id {
				return Result{}, failure(fmt.Sprintf("Plugin %q is installed in %s scope, not user. Use --scope %s to uninstall.", id, p.Scope, p.Scope),
					"plugin", "uninstall", id)
			}
		}
		return alreadyResult("uninstall", "not_installed", fmt.Sprintf("Plugin %q not found in installed plugins", id)), nil
	}
	delete(f.plugins[root], id)
	return okResult("uninstalled " + id), nil
}

// PluginUninstallScoped implements Runner: the install in that scope (and, for project and
// local, that projectPath) goes; one in another scope or project stays.
func (f *Fake) PluginUninstallScoped(ctx context.Context, fo Folder, id, scope, projectPath string) (Result, error) {
	if scope == "" || scope == "user" {
		return f.PluginUninstall(ctx, fo, id)
	}
	if err := f.begin(ctx, "PluginUninstallScoped", fo, id, scope, projectPath); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	root := f.rootOf(fo.Dir)
	for i, p := range f.scoped[root] {
		if p.ID == id && p.Scope == scope && p.ProjectPath == projectPath {
			f.scoped[root] = append(f.scoped[root][:i:i], f.scoped[root][i+1:]...)
			return okResult("uninstalled " + id), nil
		}
	}
	return alreadyResult("uninstall", "not_installed", fmt.Sprintf("Plugin %q not found in installed plugins", id)), nil
}

func (f *Fake) setEnabled(ctx context.Context, method string, fo Folder, id string, on bool) (Result, error) {
	if err := f.begin(ctx, method, fo, id); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.plugins[f.rootOf(fo.Dir)][id]; !ok {
		return Result{}, failure("plugin not installed: "+id, "plugin", strings.ToLower(strings.TrimPrefix(method, "Plugin")), id)
	}
	t := f.targetOf(fo.Dir)
	if f.enabled[t] == nil {
		f.enabled[t] = map[string]bool{}
	}
	if cur, ok := f.enabled[t][id]; ok && cur == on {
		verb := strings.ToLower(strings.TrimPrefix(method, "Plugin"))
		return alreadyResult(verb, "already_in_goal_state", fmt.Sprintf("Plugin %q is already %sd", id, verb)), nil
	}
	f.enabled[t][id] = on
	return okResult(fmt.Sprintf("%s %s", strings.ToLower(strings.TrimPrefix(method, "Plugin")), id)), nil
}

// PluginEnable implements Runner.
func (f *Fake) PluginEnable(ctx context.Context, fo Folder, id string) (Result, error) {
	return f.setEnabled(ctx, "PluginEnable", fo, id, true)
}

// PluginDisable implements Runner.
func (f *Fake) PluginDisable(ctx context.Context, fo Folder, id string) (Result, error) {
	return f.setEnabled(ctx, "PluginDisable", fo, id, false)
}

// PluginList implements Runner: installed plugins of the root, Enabled from the settings target.
func (f *Fake) PluginList(ctx context.Context, fo Folder) ([]PluginInfo, error) {
	if err := f.begin(ctx, "PluginList", fo); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	root, target := f.rootOf(fo.Dir), f.targetOf(fo.Dir)
	var out []PluginInfo
	for _, id := range sortedKeys(f.plugins[root]) {
		out = append(out, PluginInfo{ID: id, Version: "fake", Enabled: f.enabled[target][id],
			Scope: "user", InstallPath: f.plugins[root][id]})
	}
	out = append(out, f.scoped[root]...)
	return out, nil
}

// Agents implements Runner.
func (f *Fake) Agents(ctx context.Context, fo Folder) ([]Agent, error) {
	if err := f.begin(ctx, "Agents", fo); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Agent(nil), f.agents[key(fo.Dir)]...), nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
