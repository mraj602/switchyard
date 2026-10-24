package main

// Wiring shared by `wy setup`, `wy account`, `wy uninstall` and `wy tick`: the machine's
// folders, the claude runner, the detect deps (with the hub, timer and service facts the
// command layer adds), and the collaborators internal/setup/apply needs (Connector,
// Contents, ConfigSet, Exec, RemoveTimer). Logic lives in the internal packages; this file
// only builds them from the process environment.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/hub/cli"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/service"
	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/detect"
	"github.com/mraj602/watchyard/internal/setup/launcher"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/pluginmkt"
	"github.com/mraj602/watchyard/internal/share"
	"github.com/mraj602/watchyard/internal/timer"
	"github.com/mraj602/watchyard/internal/version"
)

// exitError makes dispatch exit with a specific code (setup.md 4 exit codes). A nil err
// prints nothing.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

func exitWith(code int, err error) error { return &exitError{code: code, err: err} }

// Exit codes of setup.md 4 not defined by apply/backup.
const (
	exitUsage       = 2
	exitNeedsPerson = 3
	exitPlanChanged = 4
	exitPartlyUndo  = 5
	exitNoClaude    = 7
)

// setupEnv is the machine as the setup commands see it.
type setupEnv struct {
	home   string
	dirs   paths.Dirs
	wy     string // absolute path of this wy binary (baked into hooks and the status line)
	goos   string
	getenv func(string) string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	// stdinFile is os.Stdin when it is a terminal (raw mode, echo-off token).
	stdinFile *os.File
	tty       bool
	claude    string // claude binary; "" when not found
	runner    claudecli.Runner
	noTimer   bool
	// lockHeld says this command holds <state>/setup.lock (lock); apply.Run then does not
	// take it again.
	lockHeld bool
	// phonePair runs `wy phone pair`; nil means cli.PhonePair. http is the client for the
	// --code claim; nil means a default one. Tests inject both.
	phonePair func(args []string, stdout, stderr io.Writer) error
	http      *http.Client
}

func newSetupEnv(stdout, stderr io.Writer) (*setupEnv, error) {
	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return nil, err
	}
	home := pe.Home
	if home == "" {
		return nil, errors.New("cannot find the home folder")
	}
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	wy, err := os.Executable()
	if err == nil {
		if r, rerr := filepath.EvalSymlinks(wy); rerr == nil {
			wy = r
		}
	} else {
		wy = "wy"
	}
	e := &setupEnv{home: home, dirs: d, wy: wy, goos: runtime.GOOS, getenv: os.Getenv,
		stdin: os.Stdin, stdout: stdout, stderr: stderr}
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		e.tty, e.stdinFile = true, os.Stdin
	}
	if p, err := claudecli.Locate(home); err == nil {
		e.claude = p
		x := claudecli.NewExec(p)
		x.Dir = os.TempDir() // never $HOME: <cwd>/.claude/settings.json counts as project settings
		e.runner = x
	} else {
		e.runner = missingClaude{}
	}
	return e, nil
}

func (e *setupEnv) configPath() string { return filepath.Join(e.dirs.Config.Path, config.FileName) }

func (e *setupEnv) tilde(p string) string { return identity.Tilde(e.home, p) }

func (e *setupEnv) expand(s string) string {
	switch {
	case s == "~":
		return e.home
	case strings.HasPrefix(s, "~/"):
		return filepath.Join(e.home, filepath.FromSlash(s[2:]))
	}
	if filepath.IsAbs(s) {
		return filepath.Clean(s)
	}
	return s
}

// tildeArg normalises a folder argument to the "~" form ("~/.claude-2", "/home/u/.claude-2",
// ".claude-2" and "claude-2" all give "~/.claude-2").
func (e *setupEnv) tildeArg(s string) string {
	s = strings.TrimSuffix(s, "/")
	switch {
	case strings.HasPrefix(s, "~"), filepath.IsAbs(s):
		return e.tilde(e.expand(s))
	case strings.HasPrefix(s, ".claude"):
		return "~/" + s
	case strings.HasPrefix(s, "claude"):
		return "~/." + s
	}
	return s
}

func (e *setupEnv) timerEnv() timer.Env {
	uname := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	return timer.Env{GOOS: e.goos, Home: e.home, UID: os.Getuid(), User: uname, SyPath: e.wy,
		StateDir: e.dirs.State.Path, Getenv: e.getenv, Runner: timer.ExecRunner{}, Out: e.stderr}
}

func (e *setupEnv) serviceEnv() service.Env {
	t := e.timerEnv()
	return service.Env{GOOS: t.GOOS, Home: t.Home, UID: t.UID, User: t.User, SyPath: t.SyPath,
		StateDir: t.StateDir, Getenv: t.Getenv, Runner: t.Runner, Out: e.stderr}
}

// missingClaude is the Runner when claude is not installed: every call fails with
// ErrNotFound, so detect still shows what it can and the command exits 7.
type missingClaude struct{}

var errNoClaude = claudecli.ErrNotFound

func (missingClaude) Version(context.Context) (claudecli.VersionInfo, error) {
	return claudecli.VersionInfo{}, errNoClaude
}
func (missingClaude) AuthStatus(context.Context, claudecli.Folder) (claudecli.AuthStatus, error) {
	return claudecli.AuthStatus{}, errNoClaude
}
func (missingClaude) AuthLogin(context.Context, claudecli.Folder, claudecli.Stdio) error {
	return errNoClaude
}
func (missingClaude) PluginUpdate(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return claudecli.Result{}, errNoClaude
}
func (missingClaude) PluginMarketplaceAdd(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return claudecli.Result{}, errNoClaude
}
func (missingClaude) PluginMarketplaceRemove(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return claudecli.Result{}, errNoClaude
}
func (missingClaude) PluginMarketplaceUpdate(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return claudecli.Result{}, errNoClaude
}
func (missingClaude) PluginMarketplaceList(context.Context, claudecli.Folder) ([]claudecli.MarketplaceInfo, error) {
	return nil, errNoClaude
}
func (missingClaude) PluginInstall(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return claudecli.Result{}, errNoClaude
}
func (missingClaude) PluginUninstall(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return claudecli.Result{}, errNoClaude
}
func (missingClaude) PluginEnable(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return claudecli.Result{}, errNoClaude
}
func (missingClaude) PluginDisable(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return claudecli.Result{}, errNoClaude
}
func (missingClaude) PluginList(context.Context, claudecli.Folder) ([]claudecli.PluginInfo, error) {
	return nil, errNoClaude
}
func (missingClaude) Agents(context.Context, claudecli.Folder) ([]claudecli.Agent, error) {
	return nil, errNoClaude
}

// ---- detect -------------------------------------------------------------------------------

func (e *setupEnv) detectDeps(primary string) detect.Deps {
	return detect.Deps{
		Home: e.home, GOOS: e.goos, Getenv: e.getenv, Claude: e.runner,
		ConfigDir: e.dirs.Config.Path, StateDir: e.dirs.State.Path, Sy: e.wy, Primary: primary,
		HubProbe: e.hubProbe,
	}
}

func (e *setupEnv) detect(ctx context.Context, primary string) (model.State, error) {
	return detect.Detect(ctx, e.detectDeps(primary))
}

// hubProbe is detect's HubProbe plus the facts only the command layer knows: whether the
// flush timer and the hub service are installed (by their unit files, so nothing runs in a
// home that has none) and active, and Linux linger.
func (e *setupEnv) hubProbe(ctx context.Context, url string) model.HubState {
	var h model.HubState
	if url != "" { // "" for a pulled machine: only the timer is asked about
		h.Healthy = e.hubHealth(ctx, url) == nil
	}
	te := e.timerEnv()
	switch e.goos {
	case "linux":
		h.TimerActive = exists(filepath.Join(timer.SystemdDir(te), timer.SystemdName+".timer"))
	case "darwin":
		h.TimerActive = exists(timer.LaunchAgentPath(te))
	}
	se := e.serviceEnv()
	unit := service.UnitPath(se, model.ServiceSpec{Name: service.DefaultName})
	if unit != "" && exists(unit) {
		h.ServiceInstalled = true
		if st, err := service.New(se).Status(ctx, service.DefaultName); err == nil {
			h.ServiceActive, h.LingerOff = st.Active, st.LingerOff
		}
	}
	return h
}

// hubHealth asks /health the way flush will reach the hub: through hub.dial when set.
func (e *setupEnv) hubHealth(ctx context.Context, url string) error {
	if f, err := config.Load(e.dirs.Config.Path); err == nil {
		if cfg, err := f.Typed(); err == nil && cfg.Hub.Dial != "" {
			hc, err := client.HTTPClient(cfg.Hub.Dial, 2*time.Second)
			if err != nil {
				return err
			}
			_, err = (&client.Client{BaseURL: url, HTTP: hc}).Health(ctx)
			return err
		}
	}
	return healthCheck(ctx, url, 2*time.Second)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func healthCheck(ctx context.Context, url string, d time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimRight(url, "/")+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/health answered %d", resp.StatusCode)
	}
	return nil
}

// ---- apply collaborators ------------------------------------------------------------------

// syConnector implements model.Connector on `wy connect` (connect.go) and `wy hub token`.
type syConnector struct{ e *setupEnv }

func (c syConnector) Connect(ctx context.Context, req model.ConnectRequest) (model.ConnectResult, error) {
	if req.Pull {
		return c.connectPull()
	}
	deps := connectDeps{timer: c.e.timerEnv(), stdin: strings.NewReader(req.Token + "\n"), dirs: c.e.dirs}
	args := []string{req.URL, "--token-stdin"}
	if req.InsecureLAN {
		args = append(args, "--insecure-lan")
	}
	if req.Dial != "" {
		args = append(args, "--dial", req.Dial)
	}
	if req.Reach != "" {
		args = append(args, "--reach", req.Reach)
	}
	if c.e.noTimer {
		args = append(args, "--no-timer")
	}
	var out bytes.Buffer
	err := connect(ctx, deps, args, &out, c.e.stderr)
	io.Copy(c.e.stderr, &out) // human text: stderr, so --json stdout stays one object
	if err != nil {
		return model.ConnectResult{}, err
	}
	te := deps.timer
	timerOn := !c.e.noTimer && (exists(filepath.Join(timer.SystemdDir(te), timer.SystemdName+".timer")) ||
		exists(timer.LaunchAgentPath(te)) || exists(filepath.Join(te.StateDir, timer.WindowsXMLFile)))
	return model.ConnectResult{Healthy: true, Timer: timerOn, Message: "connected to " + req.URL}, nil
}

// connectPull is `wy setup --hub pull` (machines.md 3.4): hub.reach = "pull", no hub.url
// (records leave only through `wy outbox drain --stdio` when the hub calls), no token, and
// the timer for the local work (scan, agents, share sync).
func (c syConnector) connectPull() (model.ConnectResult, error) {
	f, err := config.Load(c.e.dirs.Config.Path)
	if err != nil {
		return model.ConnectResult{}, err
	}
	for _, kv := range [][2]string{{"hub.reach", config.ReachPull}, {"hub.url", ""}, {"hub.dial", ""}} {
		if err := f.Set(kv[0], []string{kv[1]}); err != nil {
			return model.ConnectResult{}, err
		}
	}
	if err := f.Save(); err != nil {
		return model.ConnectResult{}, err
	}
	fmt.Fprintf(c.e.stderr, "Set hub.reach = \"pull\" in %s: the hub pulls this machine over SSH.\n", f.Path())
	timerOn := false
	if !c.e.noTimer {
		te := c.e.timerEnv()
		te.Out = c.e.stderr
		if _, terr := timer.Install(te); terr != nil {
			fmt.Fprintf(c.e.stderr, "wy setup: timer not fully installed: %v\n", terr)
		} else {
			timerOn = true
		}
	}
	return model.ConnectResult{Healthy: true, Timer: timerOn, Message: "pulled over SSH"}, nil
}

func (c syConnector) MintMachineToken(ctx context.Context, machine string) (string, error) {
	host, _ := os.Hostname()
	if host == "" {
		host = "this-machine"
	}
	var out bytes.Buffer
	if err := cli.Run([]string{"token", "create", "--kind", "machine", "--name", host, "--machine", machine, "--json"}, &out, c.e.stderr); err != nil {
		return "", fmt.Errorf("mint a machine token: %w", err)
	}
	var r struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Token == "" {
		return "", errors.New("mint a machine token: no token in the answer")
	}
	return r.Token, nil
}

func (c syConnector) WaitHealthy(ctx context.Context, url string, seconds int) error {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		err := healthCheck(ctx, url, 2*time.Second)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the hub at %s did not answer /health within %d s: %w", url, seconds, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// configSet implements apply.Deps.ConfigSet: the planner emits one config_set whose value
// is an object of dotted keys ("share.primary", "slots.labels.<folder>", ...); a non-empty
// key sets that one key.
func (e *setupEnv) configSet(key string, value json.RawMessage) error {
	f, err := config.Load(e.dirs.Config.Path)
	if err != nil {
		return err
	}
	if key != "" {
		if err := f.SetJSON(key, value); err != nil {
			return err
		}
		return f.Save()
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return fmt.Errorf("config_set: want an object of keys: %w", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := f.SetJSON(k, m[k]); err != nil {
			return err
		}
	}
	return f.Save()
}

// contentsFor returns apply.Deps.Contents for a plan: a write_file action is a launcher
// (setup.md 8.3). Its content is rebuilt from the folder and label and matched by the
// plan's content hash, so the planner and the writer can never disagree.
func (e *setupEnv) contentsFor(st model.State, p model.Plan) func(a model.Action) ([]byte, error) {
	labels := map[string]string{}
	var folders []string
	for _, f := range st.Folders {
		folders = append(folders, f.Folder)
		l := f.Label
		if l == "" {
			l = strings.TrimPrefix(filepath.Base(f.Dir), ".")
		}
		labels[f.Folder] = l
	}
	for _, a := range p.Actions {
		switch a.Op {
		case model.OpMkdir:
			t := e.tilde(a.Path)
			folders = append(folders, t)
			labels[t] = a.Note
		case model.OpConfigSet:
			var m map[string]json.RawMessage
			if json.Unmarshal(a.Value, &m) == nil {
				for k, v := range m {
					if f, ok := strings.CutPrefix(k, "slots.labels."); ok {
						var s string
						if json.Unmarshal(v, &s) == nil {
							labels[f] = s
						}
					}
				}
			}
		}
	}
	return func(a model.Action) ([]byte, error) {
		for _, f := range folders {
			body := launcher.Render(st.OS, launcher.Spec{Slot: f, Label: labels[f]})
			if launcher.Hash(body) == a.ContentSHA256 {
				return body, nil
			}
		}
		return nil, fmt.Errorf("no launcher content matches %s (%s)", a.Path, a.Note)
	}
}

func (e *setupEnv) removeTimer(ctx context.Context) error {
	_, err := timer.Uninstall(e.timerEnv())
	return err
}

func (e *setupEnv) applyDeps(st model.State, p model.Plan) apply.Deps {
	return apply.Deps{
		Sharer:      share.New(e.dirs.State.Path),
		Plugins:     pluginmkt.New(e.runner),
		Service:     service.New(e.serviceEnv()),
		Connector:   syConnector{e: e},
		Exec:        apply.OSExec{Dir: e.dirs.State.Path, Claude: e.claude},
		Contents:    e.contentsFor(st, p),
		ConfigSet:   e.configSet,
		RemoveTimer: e.removeTimer,
	}
}

func (e *setupEnv) undoDeps() backup.Deps {
	return backup.Deps{Sharer: share.New(e.dirs.State.Path), Service: service.New(e.serviceEnv()), RemoveTimer: e.removeTimer}
}

// mcpFiles is the MCP sync set of a state (apply.Options.MCPFiles).
func mcpFiles(st model.State) []model.MCPFile {
	var out []model.MCPFile
	for _, f := range st.Folders {
		if f.StateFile == "" {
			continue
		}
		out = append(out, model.MCPFile{Folder: f.Folder, StateFile: f.StateFile, Dir: f.Dir, Primary: f.Primary})
	}
	return out
}

func (e *setupEnv) applyOptions(st model.State, command string) (apply.Options, error) {
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return apply.Options{}, err
	}
	return apply.Options{
		StateDir: e.dirs.State.Path, Command: command, SyVersion: version.Version, Machine: m.ID,
		SyPath: e.wy, Links: st.Links, MCPFiles: mcpFiles(st), ConfigPath: e.configPath(),
		LockHeld: e.lockHeld,
	}, nil
}

// lock takes <state>/setup.lock for a command that changes the setup or its backup sets
// (setup.md 7.6): wy setup with its recovery of an interrupted run, --undo, account remove,
// uninstall. Exit 2 when another such command holds it.
func (e *setupEnv) lock() (func(), error) {
	unlock, err := apply.Lock(e.dirs.State.Path)
	if errors.Is(err, apply.ErrLocked) {
		return nil, exitWith(apply.ExitLocked, err)
	}
	if err != nil {
		return nil, err
	}
	e.lockHeld = true
	return func() { e.lockHeld = false; unlock() }, nil
}

// liveSets drops the in-progress sets from st when another process holds setup.lock: they
// belong to a run still going, not an interrupted one, and must not be undone or reported.
func (e *setupEnv) liveSets(st *model.State) {
	if e.lockHeld || len(st.IncompleteSets) == 0 {
		return
	}
	unlock, err := apply.Lock(e.dirs.State.Path)
	if err != nil {
		st.IncompleteSets = nil
		return
	}
	unlock()
}

// readSecretLine reads one line with echo off when stdin is a terminal, else one line.
func (e *setupEnv) readSecretLine(prompt string) (string, error) {
	if e.stdinFile != nil {
		fmt.Fprint(e.stderr, prompt)
		b, err := term.ReadPassword(int(e.stdinFile.Fd()))
		fmt.Fprintln(e.stderr)
		return strings.TrimSpace(string(b)), err
	}
	return readLine(e.stdin)
}

// readLine reads the first line of r byte by byte (nothing after it is consumed).
func readLine(r io.Reader) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			b = append(b, one[0])
		}
		if err != nil {
			if len(b) == 0 {
				return "", err
			}
			break
		}
	}
	return strings.TrimSpace(string(b)), nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
