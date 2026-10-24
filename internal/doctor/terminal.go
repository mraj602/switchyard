package doctor

// The terminal checks of docs/spec/terminal.md T11. Each one runs only when its
// piece is on (a ledger entry, a token file, config terminal.titles, Warp found), so a
// machine with no terminal pieces gets no extra line. Nothing here writes outside a temp
// folder (the title fixture) and nothing prints the board token.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/hook"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/jsonedit"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
	"github.com/mraj602/watchyard/internal/status"
	"github.com/mraj602/watchyard/internal/termctl"
	"github.com/mraj602/watchyard/internal/termenv"
	"github.com/mraj602/watchyard/internal/warp"
	"github.com/mraj602/watchyard/plugin"
)

// BoardMe is what GET /v1/me tells about the board token's device.
type BoardMe struct {
	Kind    string // device.kind
	Machine string // device.machine ("" when null)
}

// ErrBoardRejected: the hub answered 401 or 403 to the board token.
var ErrBoardRejected = errors.New("the hub rejected the board token")

// boardProbeTimeout bounds the one network call the terminal checks make.
const boardProbeTimeout = 3 * time.Second

// probeBoard is the real Options.BoardProbe: GET <hub>/v1/me with the token, 3 s, through
// the hub.dial transport, never following a redirect.
func probeBoard(ctx context.Context, hub, dial, token string) (BoardMe, error) {
	tr, err := client.DialTransport(dial)
	if err != nil {
		return BoardMe{}, err
	}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, boardProbeTimeout)
	defer cancel()
	hc := &http.Client{Transport: tr, Timeout: boardProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(hub, "/")+"/v1/me", nil)
	if err != nil {
		return BoardMe{}, errors.New(scrub(err.Error(), token))
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := hc.Do(req)
	if err != nil {
		return BoardMe{}, errors.New(scrub(err.Error(), token))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return BoardMe{}, ErrBoardRejected
	case resp.StatusCode != http.StatusOK:
		return BoardMe{}, fmt.Errorf("the hub answered %d", resp.StatusCode)
	}
	var me struct {
		Device struct {
			Kind    string  `json:"kind"`
			Machine *string `json:"machine"`
		} `json:"device"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&me); err != nil {
		return BoardMe{}, errors.New("the answer of /v1/me is not JSON")
	}
	out := BoardMe{Kind: me.Device.Kind}
	if me.Device.Machine != nil {
		out.Machine = *me.Device.Machine
	}
	return out, nil
}

func scrub(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "<token>")
	}
	return s
}

// checkTerminal adds the T11 lines. claudeVersion is what checkClaude found ("" unknown).
func checkTerminal(ctx context.Context, rep *Report, o Options, cfg config.Config, claudeVersion string) {
	st := o.Dirs.State.Path
	if st == "" {
		return
	}
	checkBoardToken(ctx, rep, o, cfg)

	led, err := ledger.Read(st) // an unreadable ledger is already reported by checkSetup
	var term *model.LedgerTerminal
	if err == nil {
		term = led.Terminal
	}
	if cfg.Terminal.Titles {
		checkTitles(rep, o, led, term)
	}
	if term != nil {
		if term.Pane != nil {
			checkPane(rep, o, term.Pane.Targets, claudeVersion)
		}
		if w := term.WarpPlugin; w != nil {
			checkWarpPlugin(rep, o, w)
		}
		checkTabs(rep, o, term.Tabs)
	}
	checkWarpDetect(rep, o, term)
	checkAcceptEnv(rep, o)
}

func orNone(s string) string {
	if s == "" {
		return "not set"
	}
	return s
}

func (o Options) tilde(p string) string { return identity.Tilde(o.Home, p) }

// readMachineID reads <state>/machine.json without creating it.
func readMachineID(state string) (string, error) {
	b, err := os.ReadFile(filepath.Join(state, "machine.json"))
	if err != nil {
		return "", err
	}
	var m identity.Machine
	if err := json.Unmarshal(b, &m); err != nil || m.ID == "" {
		return "", errors.New("no machine id")
	}
	return m.ID, nil
}

// checkBoardToken: token.json mode and hub URL, then /v1/me (kind desktop, bound to this
// machine). An unreachable hub is a warning, never a failure.
func checkBoardToken(ctx context.Context, rep *Report, o Options, cfg config.Config) {
	tok, ok := board.PeekToken(o.Dirs.State.Path)
	if !ok {
		return
	}
	file := board.TokenFile(o.Dirs.State.Path)
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(file); err == nil && fi.Mode().Perm()&0o077 != 0 {
			rep.add(Check{Name: "board token", Level: Warn,
				Detail: fmt.Sprintf("%s is mode %04o: other people on this machine can read it", file, fi.Mode().Perm()),
				Fix:    "chmod 600 " + file})
			return
		}
	}
	if !board.SameHub(tok.URL, cfg.Hub.URL) {
		rep.add(Check{Name: "board token", Level: Warn,
			Detail: "the token was issued for " + tok.URL + " but hub.url is " + orNone(cfg.Hub.URL) + ": the board ignores it",
			Fix:    "run: wy board connect"})
		return
	}
	probe := o.BoardProbe
	if probe == nil {
		probe = probeBoard
	}
	me, err := probe(ctx, cfg.Hub.URL, cfg.Hub.Dial, tok.Token)
	switch {
	case errors.Is(err, ErrBoardRejected):
		rep.add(Check{Name: "board token", Level: Warn, Detail: "the hub rejected the token of device " + tok.Device,
			Fix: "run: wy board connect"})
		return
	case err != nil:
		rep.add(Check{Name: "board token", Level: Warn,
			Detail: "the hub did not confirm the token (" + scrub(err.Error(), tok.Token) + "): the board shows this machine's sessions only",
			Fix:    "check the hub is reachable (see the hub lines above), then run wy doctor again"})
		return
	case me.Kind != "desktop":
		rep.add(Check{Name: "board token", Level: Warn, Detail: "the token's device is a " + orNone(me.Kind) + ", not a desktop device",
			Fix: "run: wy board disconnect, then wy board connect"})
		return
	}
	if m, err := readMachineID(o.Dirs.State.Path); err == nil && me.Machine != m {
		rep.add(Check{Name: "board token", Level: Warn, Detail: "device " + tok.Device + " is not bound to this machine",
			Fix: "run: wy board disconnect, then wy board connect"})
		return
	}
	rep.add(Check{Name: "board token", Level: OK, Detail: "device " + tok.Device + " (desktop, this machine), mode 0600"})
}

// titleFixtureEvent is the hook payload the title check feeds to the in-process title code.
const titleFixtureEvent = `{"session_id":"doctor-fixture","cwd":"/tmp/doctor-fixture","hook_event_name":"Stop"}`

// runTitleFixture runs `wy hook --title Stop` in process against a throwaway state folder
// (never the real one) and returns what it printed.
func runTitleFixture() (string, error) {
	tmp, err := os.MkdirTemp("", "wy-doctor-title-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	slotDir := filepath.Join(tmp, "slot")
	cfgDir := filepath.Join(tmp, "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return "", err
	}
	name := identity.Tilde(paths.FromOS().Home, slotDir) // the key RunTitle will look up
	toml := "[terminal]\ntitles = true\n[slots.labels]\n" + fmt.Sprintf("%q", name) + " = \"doctor\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(toml), 0o600); err != nil {
		return "", err
	}
	env := map[string]string{
		"WY_CONFIG_DIR":     cfgDir,
		"WY_STATE_DIR":      filepath.Join(tmp, "state"),
		"WY_DATA_DIR":       filepath.Join(tmp, "data"),
		"CLAUDE_CONFIG_DIR": slotDir,
	}
	var out bytes.Buffer
	hook.RunTitle([]string{"Stop"}, strings.NewReader(titleFixtureEvent), &out, func(k string) string { return env[k] })
	return out.String(), nil
}

// validTitleOutput: exactly one JSON object whose only key is a terminalSequence string that
// starts with ESC ] 2 ;. The caller feeds a fixture the hook must answer (titles on, the slot
// labelled, a Stop event), so no output at all is a failure, not a pass.
func validTitleOutput(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("printed nothing for a fixture that must print a title")
	}
	dec := json.NewDecoder(strings.NewReader(s))
	var m map[string]json.RawMessage
	if err := dec.Decode(&m); err != nil {
		return errors.New("not JSON")
	}
	if dec.More() {
		return errors.New("more than one JSON value")
	}
	raw, ok := m["terminalSequence"]
	if !ok || len(m) != 1 {
		return errors.New("not exactly one terminalSequence field")
	}
	var seq string
	if json.Unmarshal(raw, &seq) != nil || !strings.HasPrefix(seq, "\x1b]2;") {
		return errors.New("terminalSequence does not start with ESC ]2;")
	}
	return nil
}

// checkTitles: the env key in every recorded target, the --title entries in the hooks
// plugin, and the title hook's output for a fixture.
func checkTitles(rep *Report, o Options, led model.Ledger, term *model.LedgerTerminal) {
	var targets []string
	if term != nil && term.Titles != nil {
		targets = term.Titles.Targets
	}
	if len(targets) == 0 {
		rep.add(Check{Name: "titles", Level: Warn, Detail: "terminal.titles is on but no settings target is recorded",
			Fix: "run: wy term titles on"})
	} else {
		var missing []string
		for _, t := range targets {
			raw, ok, err := jsonedit.Read(t, ledger.TitleEnvPointer)
			var v string
			if err != nil || !ok || json.Unmarshal(raw, &v) != nil || v != "1" {
				missing = append(missing, o.tilde(t))
			}
		}
		if len(missing) > 0 {
			rep.add(Check{Name: "titles", Level: Warn,
				Detail: fmt.Sprintf("%s is not \"1\" in %d of %d settings targets (%s): Claude Code's own title will fight ours",
					ledger.TitleEnvKey, len(missing), len(targets), strings.Join(missing, ", ")),
				Fix: "run: wy term titles on"})
		} else {
			rep.add(Check{Name: "titles", Level: OK, Detail: fmt.Sprintf("%s set in %d settings target(s)", ledger.TitleEnvKey, len(targets))})
		}
	}

	hooks, where := titleHooksFile(led)
	if n := strings.Count(hooks, "hook "+hook.TitleFlag); n == 0 {
		rep.add(Check{Name: "title hooks", Level: Warn, Detail: "the watchyard plugin (" + where + ") has no `wy hook --title` entries",
			Fix: "run: wy setup --yes (rewrites the plugin)"})
	} else {
		rep.add(Check{Name: "title hooks", Level: OK, Detail: fmt.Sprintf("%d title entries in the watchyard plugin (%s)", n, where)})
	}

	out, err := runTitleFixture()
	if err == nil {
		err = validTitleOutput(out)
	}
	if err != nil {
		rep.add(Check{Name: "title output", Level: Warn, Detail: "wy hook --title printed invalid output for a Stop fixture: " + err.Error(),
			Fix: "update wy (wy update), or run: wy term titles off"})
	} else {
		rep.add(Check{Name: "title output", Level: OK, Detail: "wy hook --title prints valid JSON for a Stop fixture"})
	}
}

// titleHooksFile is the hooks.json text of the installed watchyard plugin when wy-local has
// it, else the one embedded in this wy, and where it came from.
func titleHooksFile(led model.Ledger) (string, string) {
	if led.Plugin != nil && !led.Plugin.Adopted && led.Plugin.Marketplace != "" {
		p := filepath.Join(led.Plugin.Marketplace, "plugins", "watchyard", "hooks", "hooks.json")
		if b, err := os.ReadFile(p); err == nil {
			return string(b), "installed"
		}
	}
	b, _ := fs.ReadFile(plugin.FS(), "hooks/hooks.json")
	return string(b), "embedded in this wy"
}

// versionOnly turns "2.1.296 (Claude Code)" into "2.1.296".
func versionOnly(v string) string {
	f := strings.Fields(v)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// enabledIn counts the targets whose enabledPlugins lists id as true.
func enabledIn(o Options, targets []string, id string) (n int) {
	for _, t := range targets {
		if s, err := readSettings(o.FS, t); err == nil && s.exists && s.plugins[id] {
			n++
		}
	}
	return n
}

// checkPane: the mod is enabled in every recorded target and claude is new enough.
func checkPane(rep *Report, o Options, targets []string, claudeVersion string) {
	if n := enabledIn(o, targets, plan.PaneID); n < len(targets) {
		rep.add(Check{Name: "pane", Level: Warn, Detail: fmt.Sprintf("%s is enabled in %d of %d settings targets", plan.PaneID, n, len(targets)),
			Fix: "run: wy term setup --pane"})
		return
	}
	v := versionOnly(claudeVersion)
	if v != "" && plan.VersionLess(v, plan.PaneMinClaude) {
		rep.add(Check{Name: "pane", Level: Warn, Detail: fmt.Sprintf("Claude Code %s is older than %s, which the pane mod needs", v, plan.PaneMinClaude),
			Fix: "update Claude Code: claude update"})
		return
	}
	d := fmt.Sprintf("enabled in %d of %d settings targets", len(targets), len(targets))
	if v != "" {
		d += ", Claude Code " + v
	}
	rep.add(Check{Name: "pane", Level: OK, Detail: d})
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

// checkWarpPlugin: enabled in N of M targets, jq on PATH, and the marketplace checkout still
// at the commit the ledger recorded (risk 7: `main` cannot be pinned).
func checkWarpPlugin(rep *Report, o Options, w *model.LedgerWarpPlugin) {
	n := enabledIn(o, w.Targets, plan.WarpPluginID)
	if n < len(w.Targets) {
		rep.add(Check{Name: "warp plugin", Level: Warn, Detail: fmt.Sprintf("%s is enabled in %d of %d settings targets", plan.WarpPluginID, n, len(w.Targets)),
			Fix: "run: wy term setup --warp-plugin"})
	} else {
		rep.add(Check{Name: "warp plugin", Level: OK, Detail: fmt.Sprintf("enabled in %d of %d settings targets", n, len(w.Targets))})
	}
	if _, err := lookup(o)("jq"); err != nil {
		rep.add(Check{Name: "warp plugin jq", Level: Warn, Detail: "jq is not on PATH: Warp's plugin hooks need it",
			Fix: "install jq with your package manager"})
	}
	if w.Commit == "" {
		return
	}
	cur := ""
	for _, root := range append(append([]string{}, w.Roots...), w.Installed...) {
		if c := warp.PluginCommit(root, plan.WarpMarketplace); c != "" {
			cur = c
			break
		}
	}
	switch {
	case cur == "":
		// Unknown checkout (not cloned yet, or a layout this wy does not read): nothing to compare.
	case cur != w.Commit:
		rep.add(Check{Name: "warp plugin commit", Level: Warn,
			Detail: fmt.Sprintf("the checkout moved: wy recorded %s, the marketplace is now at %s (code from github.com/warpdotdev/claude-code-warp runs in every session)", shortSHA(w.Commit), shortSHA(cur)),
			Fix:    "read what changed, then accept it with: wy term setup --warp-plugin --accept-update (or remove it: wy term off)"})
	default:
		rep.add(Check{Name: "warp plugin commit", Level: OK, Detail: "marketplace checkout at " + shortSHA(cur) + ", as recorded"})
	}
}

// checkTabs: each tab config wy wrote still has wy's hash (else it is the person's now, shown
// as information) and the launcher it runs still exists.
func checkTabs(rep *Report, o Options, tabs []model.LedgerTab) {
	for _, t := range tabs {
		name := "tab config " + t.Label
		b, err := os.ReadFile(t.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			rep.add(Check{Name: name, Level: OK, Detail: o.tilde(t.Path) + " is gone (wy term warp on writes it again)"})
			continue
		case err != nil:
			rep.add(Check{Name: name, Level: Warn, Detail: "cannot read " + o.tilde(t.Path) + ": " + err.Error(),
				Fix: "make the file readable, or remove it: wy term warp off"})
			continue
		case warp.Hash(b) != t.SHA256:
			rep.add(Check{Name: name, Level: OK, Detail: o.tilde(t.Path) + " is yours now (edited since wy wrote it; wy leaves it alone)"})
			continue
		}
		cmd := warp.TabCommand(b)
		if cmd == "" {
			rep.add(Check{Name: name, Level: Warn, Detail: o.tilde(t.Path) + " runs no command",
				Fix: "run: wy term warp on"})
		} else if _, err := os.Stat(cmd); err != nil {
			rep.add(Check{Name: name, Level: Warn, Detail: "it runs " + cmd + ", which does not exist",
				Fix: "run: wy term warp on (writes the tab configs again), or wy setup --yes to restore the launcher"})
		} else {
			rep.add(Check{Name: name, Level: OK, Detail: o.tilde(t.Path) + " runs " + cmd})
		}
	}
}

// checkWarpDetect: the channel and tab_configs folder when Warp is found, and inside Warp
// whether WARP_FOCUS_URL still matches the allow-list (the drift signal: Warp changed its URL scheme).
func checkWarpDetect(rep *Report, o Options, term *model.LedgerTerminal) {
	env := termenv.Env{Home: o.Home, XDGDataHome: o.Getenv("XDG_DATA_HOME"),
		AppData: o.Getenv("APPDATA"), LocalAppData: o.Getenv("LOCALAPPDATA"), Getenv: o.Getenv, GOOS: runtime.GOOS}
	channel := ""
	if term != nil && term.Warp != nil {
		channel = term.Warp.Channel
	}
	in := warp.Detect(env, channel)
	if !in.Detected() {
		return
	}
	d := "channel " + in.Channel + ", tab configs in " + o.tilde(in.Folder)
	if in.InWarp {
		d += ", this shell runs inside Warp"
	}
	rep.add(Check{Name: "warp", Level: OK, Detail: d})
	if !in.InWarp {
		return
	}
	u := o.Getenv("WARP_FOCUS_URL")
	switch {
	case u == "" && o.Getenv("SSH_CONNECTION") != "":
		// Remote shell: the laptop's tab arrives as LC_SY_FOCUS instead (see checkAcceptEnv).
	case !termctl.ValidFocusURL(u):
		rep.add(Check{Name: "warp focus url", Level: Warn,
			Detail: "WARP_FOCUS_URL is " + focusShown(u) + ", not warp://session/<32 hex>: Warp may have changed its focus URL format",
			Fix:    "wy still records sessions, but focusing a tab will not work until wy is updated: wy update"})
	}
}

func focusShown(u string) string {
	if u == "" {
		return "unset"
	}
	if len(u) > 60 {
		u = u[:60] + "…"
	}
	return fmt.Sprintf("%q", u)
}

// checkAcceptEnv is risk 8 (report only, never written): on Fedora and RHEL sshd lists the
// LC_ variables it accepts, so LC_SY_FOCUS and LC_SY_HOST sent by `wy ssh` are dropped. We
// see it as a session of the last day, recorded in this outbox from Warp, that carries
// neither term.focus_url nor term.host, while this shell is itself an SSH session (so the
// check is silent on a laptop, and costs nothing there).
func checkAcceptEnv(rep *Report, o Options) {
	if o.Getenv("SSH_CONNECTION") == "" {
		return
	}
	recs, err := status.Load(filepath.Join(o.Dirs.State.Path, "outbox"), o.now().Add(-24*time.Hour))
	if err != nil {
		return
	}
	n := 0
	for _, r := range recs {
		if r.Term != nil && strings.EqualFold(r.Term.App, "warp") && r.Term.FocusURL == "" && r.Term.Host == "" {
			n++
		}
	}
	if n == 0 {
		return
	}
	rep.add(Check{Name: "ssh env", Level: Warn,
		Detail: fmt.Sprintf("%d record(s) in the last day came from Warp over SSH without a focus URL or host: this server's sshd drops LC_SY_FOCUS and LC_SY_HOST", n),
		Fix:    "add `AcceptEnv LC_SY_*` to /etc/ssh/sshd_config on this server (wy never writes it)"})
}

func lookup(o Options) func(string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath
	}
	return lookPath
}
