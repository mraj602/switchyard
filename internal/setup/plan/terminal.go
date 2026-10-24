package plan

// The terminal planner of `wy term setup`, `wy term titles|warp on|off`, `wy term off` and
// `wy setup --terminal` (docs/spec/terminal.md T10): pure and deterministic like Plan, with
// the same plan_id rule. The command layer reads the few facts detect does not know into
// TermFacts (the value of the title env key, config terminal.titles, whether wy's
// marketplace is current, Warp's folders and the tab configs in them, jq).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/mraj602/watchyard/internal/setup/launcher"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/pluginmkt"
	"github.com/mraj602/watchyard/internal/version"
	"github.com/mraj602/watchyard/internal/warp"
)

// Terminal plugin ids and sources (T10).
const (
	PaneID = pluginmkt.PaneID // watchyard-pane@wy-local
	// PaneMinClaude is the Claude Code version the pane mod was checked on (T5).
	PaneMinClaude = "2.1.296"

	WarpMarketplace = "claude-code-warp"
	WarpPluginID    = "warp@" + WarpMarketplace
	// WarpSource is what `claude plugin marketplace add` gets: a branch, because a commit
	// cannot be pinned (terminal.md "Claude Code facts" 4).
	WarpSource = "warpdotdev/claude-code-warp#" + ledger.WarpRef
)

// ErrTermRefused wraps the reasons plan.Terminal refuses a piece (an old Claude Code, no
// Warp, no wy setup yet). The command layer exits 4 with the message.
var ErrTermRefused = errors.New("cannot plan this terminal piece")

// TermFacts are what the command layer read from disk for the terminal planner.
type TermFacts struct {
	// TitleEnv maps a settings target (resolved path) to the raw JSON value of
	// env.CLAUDE_CODE_DISABLE_TERMINAL_TITLE in it; a missing key is absent from the map.
	TitleEnv map[string]string `json:"title_env,omitempty"`
	// TitlesOn is config.toml's terminal.titles.
	TitlesOn bool `json:"titles_on,omitempty"`
	// MarketExists: <state>/plugin/marketplace (wy's wy-local) is there. MarketCurrent: it
	// holds exactly what this wy writes (so neither the title hooks nor the pane need a
	// refresh).
	MarketExists  bool `json:"market_exists,omitempty"`
	MarketCurrent bool `json:"market_current,omitempty"`
	// Warp is detection for the chosen channel; Tabs the watchyard-*.toml files found in its
	// tab_configs folder and in the folders of the ledger's tabs.
	Warp warp.Install `json:"warp"`
	Tabs []warp.Found `json:"tabs,omitempty"`
	// JQ: jq is on PATH (Warp's plugin needs it).
	JQ bool `json:"jq,omitempty"`
}

// Terminal plans the chosen terminal pieces with the running binary's version.
func Terminal(st model.State, c model.TermChoices, f TermFacts) (model.Plan, error) {
	return TerminalWith(st, c, f, Options{Version: version.Version})
}

type tplanner struct {
	*planner
	c   model.TermChoices
	f   TermFacts
	led model.LedgerTerminal
	// ordered StepTerminal actions by piece (apply order of T10's table)
	hooks, pane, warpPlugin, titles, tabs []model.Action
	refresh                               bool
}

// TerminalWith is Terminal with explicit options.
func TerminalWith(st model.State, c model.TermChoices, f TermFacts, opt Options) (model.Plan, error) {
	if opt.Version == "" {
		opt.Version = version.Version
	}
	if !warp.ValidChannel(c.Channel) {
		return model.Plan{}, fmt.Errorf("unknown Warp channel %q (stable or preview)", c.Channel)
	}
	tp := &tplanner{planner: &planner{st: st, opt: opt, home: cleanPath(st.Home),
		steps: map[model.Step][]model.Action{}, warned: map[string]bool{}}, c: c, f: f}
	tp.win = st.OS == "windows"
	if st.Ledger != nil && st.Ledger.Terminal != nil {
		tp.led = *st.Ledger.Terminal
	}
	steps := []func() error{tp.planPane, tp.planWarpPlugin, tp.planTitles, tp.planTabs}
	for _, s := range steps {
		if err := s(); err != nil {
			return model.Plan{}, err
		}
	}
	if tp.refresh {
		tp.addRefresh()
	}
	for _, group := range [][]model.Action{tp.hooks, tp.pane, tp.warpPlugin, tp.titles, tp.tabs} {
		for _, a := range group {
			tp.add(model.StepTerminal, a)
		}
	}
	return tp.finish(), nil
}

func refused(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrTermRefused, fmt.Sprintf(format, a...))
}

func (tp *tplanner) keep(what, path, note string) {
	if path != "" {
		path = tp.tilde(path)
	}
	tp.out.Keep = append(tp.out.Keep, model.Keep{What: what, Path: path, Note: note})
}

// ---- folders, targets, roots -------------------------------------------------------------

func (tp *tplanner) folder(tilde string) *model.Folder {
	for i := range tp.st.Folders {
		if tp.st.Folders[i].Folder == tilde {
			return &tp.st.Folders[i]
		}
	}
	return nil
}

func targetOf(f model.Folder) string {
	if f.SettingsTarget != "" {
		return f.SettingsTarget
	}
	return filepath.Join(f.Dir, "settings.json")
}

func rootOf(f model.Folder) string {
	if f.PluginRoot != "" {
		return f.PluginRoot
	}
	return filepath.Join(f.Dir, "plugins")
}

func envOf(f model.Folder) map[string]*string {
	if f.Default {
		return map[string]*string{"CLAUDE_CONFIG_DIR": nil}
	}
	d := f.Dir
	return map[string]*string{"CLAUDE_CONFIG_DIR": &d}
}

// ttarget is one settings target with the folders that use it.
type ttarget struct {
	path    string
	folders []model.Folder
	state   *model.SettingsTarget
}

func (t ttarget) exists() bool { return t.state != nil && t.state.Exists }

func (t ttarget) enabled(id string) bool { return t.state != nil && t.state.EnabledPlugins[id] }

func (t ttarget) listed(id string) bool {
	if t.state == nil {
		return false
	}
	_, ok := t.state.EnabledPlugins[id]
	return ok
}

func (t ttarget) via() []string {
	var out []string
	for _, f := range t.folders {
		if sp := filepath.Join(f.Dir, "settings.json"); sp != filepath.Clean(t.path) {
			out = append(out, sp)
		}
	}
	return out
}

func (t ttarget) covers() []string {
	var out []string
	for _, f := range t.folders {
		out = append(out, f.Folder)
	}
	return out
}

// targets lists the settings targets in folder order.
func (tp *tplanner) targets(folders []model.Folder) []ttarget {
	var out []ttarget
	idx := map[string]int{}
	for _, f := range folders {
		p := targetOf(f)
		if i, ok := idx[p]; ok {
			out[i].folders = append(out[i].folders, f)
			continue
		}
		t := ttarget{path: p, folders: []model.Folder{f}}
		for i := range tp.st.SettingsTargets {
			if tp.st.SettingsTargets[i].Path == p {
				t.state = &tp.st.SettingsTargets[i]
			}
		}
		idx[p] = len(out)
		out = append(out, t)
	}
	return out
}

// troot is one plugin root with its folders and the folder that owns it (setup.md 5.2).
type troot struct {
	root    string
	folders []model.Folder
	owner   model.Folder
	state   *model.PluginRoot
}

func (r troot) has(id string) bool      { return r.state != nil && contains(r.state.Plugins, id) }
func (r troot) knows(mkt string) bool   { return r.state != nil && contains(r.state.Markets, mkt) }
func (r troot) file(name string) string { return filepath.Join(r.root, name) }

func (tp *tplanner) roots() []troot {
	var out []troot
	idx := map[string]int{}
	for _, f := range tp.st.Folders {
		root := rootOf(f)
		if i, ok := idx[root]; ok {
			out[i].folders = append(out[i].folders, f)
			continue
		}
		r := troot{root: root, folders: []model.Folder{f}}
		for i := range tp.st.PluginRoots {
			if tp.st.PluginRoots[i].Root == root {
				r.state = &tp.st.PluginRoots[i]
			}
		}
		idx[root] = len(out)
		out = append(out, r)
	}
	for i := range out {
		r := &out[i]
		r.owner = r.folders[0]
		found := false
		if r.state != nil && r.state.Owner != "" {
			if f := tp.folder(r.state.Owner); f != nil {
				r.owner, found = *f, true
			}
		}
		if !found {
			for _, f := range r.folders {
				if filepath.Join(f.Dir, "plugins") == filepath.Clean(r.root) {
					r.owner = f
					break
				}
			}
		}
	}
	return out
}

// run is a `claude plugin …` run as folder as, with the journal mark of piece and verb added
// to its touches (ledger.JournalMark). Only settings files get a precondition (the first
// action that touches one states what it expects).
func (tp *tplanner) run(argv []string, as model.Folder, t ttarget, registry, watch, piece, verb, note string) model.Action {
	a := model.Action{Op: model.OpRun, Argv: argv, Env: envOf(as), Covers: t.covers(), Note: note}
	a.Touches = []string{t.path}
	a.Pre = tp.touchPre(t.path, t.exists())
	if registry != "" {
		a.Touches = append(a.Touches, registry)
	}
	a.Touches = append(a.Touches, ledger.JournalMark(tp.st.StateDir, piece, verb))
	if watch != "" {
		a.Watch = []string{watch}
	}
	return a
}

// ---- the marketplace refresh (StepPlugin) ------------------------------------------------

func (tp *tplanner) marketDir() string { return filepath.Join(tp.st.StateDir, "plugin", "marketplace") }

func (tp *tplanner) needMarket(what string) error {
	if !tp.f.MarketExists {
		return refused("%s needs the watchyard plugin from wy's marketplace (%s); run wy setup first", what, tp.tilde(tp.marketDir()))
	}
	if !tp.f.MarketCurrent {
		tp.refresh = true
	}
	return nil
}

// addRefresh rewrites wy-local (the existing write_tree of setup.md 5.2): the watchyard
// plugin gets this wy's hooks (the title entries, T6) and the pane is written next to it.
func (tp *tplanner) addRefresh() {
	dir := tp.marketDir()
	sum := sha256.Sum256([]byte(MarketplaceName + "\x00" + tp.opt.Version + "\x00" + tp.st.Wy))
	tp.add(model.StepPlugin, model.Action{Op: model.OpWriteTree, Path: dir, ContentSHA256: hex.EncodeToString(sum[:]),
		Note: fmt.Sprintf("watchyard %s (%s): refresh", tp.opt.Version, MarketplaceName),
		Pre:  pres(preType(dir, model.TypeDir))})
	if tp.c.Off {
		return
	}
	// Claude Code runs an installed plugin from its cache, not from the marketplace folder:
	// every root that has the watchyard plugin updates wy-local and then the plugin, so the
	// new hooks (the title entries) are active (as `wy postupdate` does, pluginmkt.Update).
	for _, r := range tp.roots() {
		if !r.has(PluginID) {
			continue
		}
		ownerT := tp.targets([]model.Folder{r.owner})[0]
		tp.hooks = append(tp.hooks,
			tp.run([]string{"claude", "plugin", "marketplace", "update", MarketplaceName, "--json"}, r.owner, ownerT,
				r.file("known_marketplaces.json"), r.root, ledger.PieceHooks, ledger.VerbMarketplaceUpdate, "watchyard hooks: refresh wy-local"),
			tp.run([]string{"claude", "plugin", "update", PluginID, "--json"}, r.owner, ownerT,
				r.file("installed_plugins.json"), r.root, ledger.PieceHooks, ledger.VerbUpdate, "watchyard hooks: update the plugin"))
	}
}

// ---- pane --------------------------------------------------------------------------------

func (tp *tplanner) planPane() error {
	if !tp.c.Pane {
		return nil
	}
	if tp.c.Off {
		if p := tp.led.Pane; p != nil {
			return tp.pluginOff(ledger.PiecePane, PaneID, MarketplaceName, &offRecord{targets: p.Targets, installed: p.Installed, markets: p.Markets})
		}
		return tp.pluginOff(ledger.PiecePane, PaneID, MarketplaceName, nil)
	}
	if tp.st.Claude.Path == "" {
		return refused("the pane needs Claude Code (claude not found)")
	}
	if versionLess(tp.st.Claude.Version, PaneMinClaude) {
		return refused("the pane needs Claude Code %s or newer (found %s)", PaneMinClaude, tp.st.Claude.Version)
	}
	if err := tp.needMarket("the pane"); err != nil {
		return err
	}
	tp.pluginOn(ledger.PiecePane, PaneID, MarketplaceName, tp.marketDir())
	return nil
}

// pluginOn installs id (from marketplace mkt, added from source when a root lacks it) once
// per plugin root as its owner, then enables it in every other settings target.
func (tp *tplanner) pluginOn(piece, id, mkt, source string) {
	acts := &tp.pane
	if piece == ledger.PieceWarpPlugin {
		acts = &tp.warpPlugin
	}
	for _, r := range tp.roots() {
		ts := tp.targets(r.folders)
		var need []ttarget
		for _, t := range ts {
			if t.enabled(id) {
				tp.keep(piece, t.path, id+" enabled")
				continue
			}
			need = append(need, t)
		}
		if len(need) == 0 {
			continue
		}
		ownerT := tp.targets([]model.Folder{r.owner})[0]
		for _, t := range ts {
			if t.path == ownerT.path {
				ownerT = t
			}
		}
		enabledBy := ""
		if !r.has(id) {
			if !r.knows(mkt) {
				*acts = append(*acts, tp.run([]string{"claude", "plugin", "marketplace", "add", source, "--json"}, r.owner, ownerT,
					r.file("known_marketplaces.json"), r.root, piece, ledger.VerbMarketplaceAdd, ""))
			}
			note := ""
			if len(r.folders) > 1 {
				note = "shared plugin root"
			}
			*acts = append(*acts, tp.run([]string{"claude", "plugin", "install", id, "--json"}, r.owner, ownerT,
				r.file("installed_plugins.json"), r.root, piece, ledger.VerbInstall, note))
			enabledBy = ownerT.path
		}
		for _, t := range need {
			if t.path == enabledBy {
				continue
			}
			*acts = append(*acts, tp.run([]string{"claude", "plugin", "enable", id, "--json"}, t.folders[0], t,
				"", "", piece, ledger.VerbEnable, ""))
		}
	}
}

// offRecord is what the ledger says wy did for a plugin piece: the settings targets it
// enabled the plugin in, the plugin roots it installed it in, and the roots it added the
// marketplace to.
type offRecord struct {
	targets, installed, markets []string
}

// Plan Keep notes of a plugin piece's `off` that make the command layer drop the path from
// the piece's ledger record (termAfterApply): there is nothing left of wy's there.
const (
	KeepAlreadyOff = "already off"
	KeepForSwitch  = "wy-local stays: the watchyard plugin uses it"
)

// pluginOff takes back what rec says wy did, and nothing else: it disables id only in the
// settings targets wy enabled it in, uninstalls it (as the root's owner) only in the roots
// wy installed it in, and removes mkt only in the roots wy added it to (wy-local stays
// where the watchyard plugin still is). A copy the person installed, enabled or added
// themselves stays. Nothing at all without a record (rec nil). Without claude nothing is
// planned for the piece and a warning says so (its record stays in the ledger).
func (tp *tplanner) pluginOff(piece, id, mkt string, rec *offRecord) error {
	if rec == nil {
		tp.keep(piece, "", "not installed by wy; nothing to remove")
		return nil
	}
	if tp.st.Claude.Path == "" {
		tp.warn("removing %s needs Claude Code (claude not found): left as is, wy keeps its record (run wy term off again once claude is back)", id)
		return nil
	}
	acts := &tp.pane
	if piece == ledger.PieceWarpPlugin {
		acts = &tp.warpPlugin
	}
	targets, installed, markets := setOf(rec.targets), setOf(rec.installed), setOf(rec.markets)
	for _, r := range tp.roots() {
		ts := tp.targets(r.folders)
		var ownerT ttarget
		for _, t := range ts {
			if t.path == targetOf(r.owner) {
				ownerT = t
			}
		}
		if ownerT.path == "" {
			ownerT = tp.targets([]model.Folder{r.owner})[0]
		}
		uninstall := installed[r.root] && r.has(id)
		if installed[r.root] && !r.has(id) {
			tp.keep(piece, r.root, KeepAlreadyOff)
		}
		for _, t := range ts {
			if !targets[t.path] {
				continue
			}
			if !t.enabled(id) {
				if !(uninstall && t.path == ownerT.path) {
					tp.keep(piece, t.path, KeepAlreadyOff)
				}
				continue
			}
			if uninstall && t.path == ownerT.path {
				continue
			}
			*acts = append(*acts, tp.run([]string{"claude", "plugin", "disable", id, "--json"}, t.folders[0], t,
				"", "", piece, ledger.VerbDisable, ""))
		}
		if uninstall {
			*acts = append(*acts, tp.run([]string{"claude", "plugin", "uninstall", id, "--json"}, r.owner, ownerT,
				r.file("installed_plugins.json"), r.root, piece, ledger.VerbUninstall, ""))
		}
		if !markets[r.root] {
			continue
		}
		switch {
		case !r.knows(mkt):
			tp.keep(piece, r.root, KeepAlreadyOff)
		case piece == ledger.PiecePane && r.has(PluginID):
			tp.keep(piece, r.root, KeepForSwitch)
		case r.has(id) && !uninstall:
			// The plugin is still there and not ours to remove: its marketplace stays too.
			tp.warn("%s: %s is installed but not by wy, so the %s marketplace it comes from stays", tp.tilde(r.root), id, mkt)
		default:
			*acts = append(*acts, tp.run([]string{"claude", "plugin", "marketplace", "remove", mkt, "--json"}, r.owner, ownerT,
				r.file("known_marketplaces.json"), r.root, piece, ledger.VerbMarketplaceRemove, ""))
		}
	}
	return nil
}

func setOf(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// ---- Warp's plugin -----------------------------------------------------------------------

func (tp *tplanner) planWarpPlugin() error {
	if !tp.c.WarpPlugin {
		return nil
	}
	if tp.c.Off {
		if w := tp.led.WarpPlugin; w != nil {
			return tp.pluginOff(ledger.PieceWarpPlugin, WarpPluginID, WarpMarketplace, &offRecord{targets: w.Targets, installed: w.Installed, markets: w.Roots})
		}
		return tp.pluginOff(ledger.PieceWarpPlugin, WarpPluginID, WarpMarketplace, nil)
	}
	if tp.st.Claude.Path == "" {
		return refused("Warp's plugin needs Claude Code (claude not found)")
	}
	tp.pluginOn(ledger.PieceWarpPlugin, WarpPluginID, WarpMarketplace, WarpSource)
	if len(tp.warpPlugin) > 0 {
		tp.warn("Warp's plugin comes from github.com/warpdotdev/claude-code-warp at branch %s (a commit cannot be pinned); wy records the commit it got and wy doctor warns when it moves", ledger.WarpRef)
		if !tp.f.JQ {
			tp.warn("jq is not on PATH: Warp's plugin needs it")
		}
	}
	return nil
}

// ---- titles ------------------------------------------------------------------------------

// TitleSideEffects is printed with every plan that turns titles on (T6).
const TitleSideEffects = "Claude Code's own tab title goes off (CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1); this also skips its background session-title request (/resume shows no generated title) and its OSC 7501 status reports to the terminal"

func (tp *tplanner) planTitles() error {
	if !tp.c.Titles {
		return nil
	}
	cfg := filepath.Join(tp.st.ConfigDir, "config.toml")
	cfgPre := preAbsent(cfg)
	if tp.st.ConfigPresent {
		cfgPre = preType(cfg, model.TypeFile)
	}
	one := string(jstr("1"))
	if tp.c.Off {
		var targets []string
		if tp.led.Titles != nil {
			targets = tp.led.Titles.Targets
		}
		for _, path := range targets {
			t := tp.targetByPath(path)
			cur, ok := tp.f.TitleEnv[path]
			switch {
			case !ok:
				tp.keep("titles", path, "env key already gone")
				continue
			case cur != one:
				tp.warn("%s: %s was changed since wy set it; left as is", tp.tilde(path), ledger.TitleEnvKey)
				continue
			}
			if prior, ok := tp.led.TitlePrior[path]; ok && prior != "" && prior != one {
				// The person had a value before `titles on`: put it back (as wy setup --undo
				// would), not delete it.
				tp.titles = append(tp.titles, model.Action{Op: model.OpJSONSet, File: path, Via: t.via(), Covers: t.covers(),
					Pointer: ledger.TitleEnvPointer, Value: json.RawMessage(prior), Before: json.RawMessage(cur),
					Note: "the value from before wy term titles on", Pre: tp.touchPre(path, true)})
				continue
			}
			tp.titles = append(tp.titles, model.Action{Op: model.OpJSONDelete, File: path, Via: t.via(), Covers: t.covers(),
				Pointer: ledger.TitleEnvPointer, Before: json.RawMessage(cur), Note: "Claude Code's own title back on",
				Pre: tp.touchPre(path, true)})
		}
		if tp.f.TitlesOn {
			tp.add(model.StepConfig, model.Action{Op: model.OpConfigSet, Path: cfg, Key: ledger.TitlesConfigKey,
				Value: json.RawMessage("false"), Pre: pres(cfgPre)})
		}
		return nil
	}
	if err := tp.needMarket("titles"); err != nil {
		return err
	}
	for _, t := range tp.targets(tp.st.Folders) {
		cur, ok := tp.f.TitleEnv[t.path]
		switch {
		case ok && cur == one:
			tp.keep("titles", t.path, ledger.TitleEnvKey+" already 1")
			continue
		case !t.exists():
			tp.warn("%s does not exist yet: Claude Code's own title stays on for %s", tp.tilde(t.path), strings.Join(t.covers(), ", "))
			continue
		}
		a := model.Action{Op: model.OpJSONSet, File: t.path, Via: t.via(), Covers: t.covers(), Pointer: ledger.TitleEnvPointer,
			Value: jstr("1"), Note: "Claude Code's own title off", Pre: tp.touchPre(t.path, true)}
		if ok {
			a.Before = json.RawMessage(cur)
		}
		tp.titles = append(tp.titles, a)
	}
	if !tp.f.TitlesOn {
		tp.add(model.StepConfig, model.Action{Op: model.OpConfigSet, Path: cfg, Key: ledger.TitlesConfigKey,
			Value: json.RawMessage("true"), Pre: pres(cfgPre)})
	}
	if len(tp.titles) > 0 || !tp.f.TitlesOn {
		tp.warn("%s", TitleSideEffects)
	}
	return nil
}

func (tp *tplanner) targetByPath(path string) ttarget {
	for _, t := range tp.targets(tp.st.Folders) {
		if t.path == path {
			return t
		}
	}
	return ttarget{path: path}
}

// ---- tab configs -------------------------------------------------------------------------

// FolderLabel is the label a folder goes by: its config label, else its name without the
// dot, as wy setup labels it ("~/.claude-2" is "claude-2").
func FolderLabel(f model.Folder) string {
	if f.Label != "" && model.ValidLabel(f.Label) {
		return f.Label
	}
	return defaultLabel(f.Dir)
}

// TabSpec is one account's tab config as the planner renders it.
type TabSpec struct {
	Folder string // "~" form
	Path   string // the file
	Spec   warp.Spec
}

// TabSpecs lists the tab config of every account that has a launcher file, in folder
// order, for the tab_configs folder of f.Warp; the warnings name the accounts left out.
func TabSpecs(st model.State, f TermFacts) (specs []TabSpec, warnings []string) {
	p := &planner{st: st, home: cleanPath(st.Home)}
	for i, fo := range st.Folders {
		label := FolderLabel(fo)
		colour := fo.Colour
		if colour == "" {
			colour = model.Palette[i%len(model.Palette)].Hex
		}
		cmd := launcherOf(p, st, fo)
		if cmd == "" {
			warnings = append(warnings, fmt.Sprintf("%s has no launcher file (%s): no tab config for %s", fo.Folder, launcherNameOf(fo), label))
			continue
		}
		s := warp.Spec{Slot: fo.Folder, Label: label, Colour: warp.Nearest(colour), Command: cmd}
		if err := warp.Check(s); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: no tab config: %v", fo.Folder, err))
			continue
		}
		specs = append(specs, TabSpec{Folder: fo.Folder, Path: warp.TabPath(f.Warp.Folder, label), Spec: s})
	}
	return specs, warnings
}

// TabContents returns the bytes of a tab config write_file action of a terminal plan made
// from the same State and TermFacts (apply.Deps.Contents): rendered again and matched by
// the plan's content hash.
func TabContents(st model.State, f TermFacts) func(a model.Action) ([]byte, error) {
	specs, _ := TabSpecs(st, f)
	return func(a model.Action) ([]byte, error) {
		for _, s := range specs {
			if b := warp.Render(s.Spec); warp.Hash(b) == a.ContentSHA256 && s.Path == a.Path {
				return b, nil
			}
		}
		return nil, fmt.Errorf("no tab config content matches %s", a.Path)
	}
}

func launcherNameOf(fo model.Folder) string {
	if fo.Default {
		return "claude"
	}
	return launcher.Name(fo.Folder)
}

// launcherOf is the absolute path of the account's launcher file: claude itself for the
// default folder, else a claude-N file found by detect or written by wy (a .cmd on
// Windows). A shell function or alias cannot be run by a tab config: "".
func launcherOf(p *planner, st model.State, fo model.Folder) string {
	if fo.Default {
		if st.Claude.Path == "" {
			return ""
		}
		return p.expand(st.Claude.Path)
	}
	name := launcher.Name(fo.Folder)
	var best string
	for _, l := range st.Launchers {
		if l.Kind != model.LauncherFile || l.Path == "" || l.Name != name {
			continue
		}
		if l.Ours && l.Folder != "" && l.Folder != fo.Folder {
			continue
		}
		if best == "" || l.Ours {
			best = p.expand(l.Path)
		}
	}
	if best == "" && st.Ledger != nil {
		file := launcher.FileName(st.OS, name)
		for _, l := range st.Ledger.Launchers {
			if path.Base(filepath.ToSlash(l.Path)) == file {
				best = l.Path
			}
		}
	}
	return best
}

func (tp *tplanner) found(path string) *warp.Found {
	for i := range tp.f.Tabs {
		if tp.f.Tabs[i].Path == path {
			return &tp.f.Tabs[i]
		}
	}
	return nil
}

func (tp *tplanner) ledgerTab(path string) *model.LedgerTab {
	for i := range tp.led.Tabs {
		if tp.led.Tabs[i].Path == path {
			return &tp.led.Tabs[i]
		}
	}
	return nil
}

func (tp *tplanner) planTabs() error {
	if !tp.c.WarpTabs {
		return nil
	}
	if tp.c.Off {
		for _, lt := range tp.led.Tabs {
			cur := tp.found(lt.Path)
			switch {
			case cur == nil:
				tp.keep("tab", lt.Path, "already gone")
			case cur.SHA256 != lt.SHA256:
				tp.warn("%s was edited since wy wrote it: it is yours now and stays", tp.tilde(lt.Path))
				tp.keep("tab", lt.Path, "yours now")
			default:
				tp.tabs = append(tp.tabs, model.Action{Op: model.OpMoveToBackup, Path: lt.Path, Note: "tab config " + lt.Label,
					Pre: pres(model.Precondition{Path: lt.Path, State: model.PathState{Type: model.TypeFile, SHA256: lt.SHA256}})})
			}
		}
		return nil
	}
	w := tp.f.Warp
	if !w.Detected() || w.Folder == "" {
		return refused("Warp not found (no %s and TERM_PROGRAM is not %s)", tp.tilde(w.Data), warp.TermProgram)
	}
	if w.Both && tp.c.Channel == "" {
		tp.warn("both Warp and Warp Preview are installed: writing for stable (--channel preview for the other)")
	}
	specs, warns := TabSpecs(tp.st, tp.f)
	for _, s := range warns {
		tp.warn("%s", s)
	}
	var writes []model.Action
	for _, s := range specs {
		body := warp.Render(s.Spec)
		sum := warp.Hash(body)
		note := fmt.Sprintf("%s · %s · %s", s.Spec.Label, s.Spec.Colour, tp.tilde(s.Spec.Command))
		a := model.Action{Op: model.OpWriteFile, Path: s.Path, Mode: "0644", ContentSHA256: sum, Note: note}
		cur := tp.found(s.Path)
		switch {
		case cur == nil:
			a.Pre = pres(preAbsent(s.Path))
		case cur.SHA256 == sum:
			tp.keep("tab", s.Path, "up to date")
			continue
		case !cur.Marker:
			tp.warn("%s is not wy's (no %q line): left alone", tp.tilde(s.Path), strings.TrimSpace(warp.Marker))
			continue
		case tp.ledgerTab(s.Path) != nil && tp.ledgerTab(s.Path).SHA256 == cur.SHA256:
			a.Pre = pres(model.Precondition{Path: s.Path, State: model.PathState{Type: model.TypeFile, SHA256: cur.SHA256}})
		default:
			tp.warn("%s was edited since wy wrote it: it is yours now and stays", tp.tilde(s.Path))
			tp.keep("tab", s.Path, "yours now")
			continue
		}
		writes = append(writes, a)
	}
	if len(writes) > 0 && !w.FolderExists {
		tp.tabs = append(tp.tabs, model.Action{Op: model.OpMkdir, Path: w.Folder, Mode: "0755", Note: "Warp's tab_configs folder",
			Pre: pres(preAbsent(w.Folder))})
	}
	tp.tabs = append(tp.tabs, writes...)
	tp.legacyTabs()
	return nil
}

// legacyTabs moves away the tab configs wy wrote under the name from before a rename
// (docs/spec/rename.md 2.8): the new ones replace them. A file edited since is the person's.
func (tp *tplanner) legacyTabs() {
	for _, lt := range tp.led.Tabs {
		if !warp.IsLegacyTab(lt.Path) {
			continue
		}
		cur := tp.found(lt.Path)
		switch {
		case cur == nil:
		case cur.SHA256 != lt.SHA256:
			tp.warn("%s (from before the rename) was edited since wy wrote it: it is yours now and stays", tp.tilde(lt.Path))
		default:
			tp.tabs = append(tp.tabs, model.Action{Op: model.OpMoveToBackup, Path: lt.Path, Note: "tab config " + lt.Label + " from before the rename",
				Pre: pres(model.Precondition{Path: lt.Path, State: model.PathState{Type: model.TypeFile, SHA256: lt.SHA256}})})
		}
	}
}

// ---- helpers -----------------------------------------------------------------------------

// versionLess compares the leading "major.minor.patch" numbers of two versions ("2.1.296
// (Claude Code)" reads as 2.1.296). An unknown version is less than any known one.
func versionLess(a, b string) bool {
	pa, pb := vparts(a), vparts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func vparts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if f := strings.Fields(v); len(f) > 0 {
		v = f[0]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}

// VersionLess is versionLess for the command layer (wy term status, wy doctor).
func VersionLess(a, b string) bool { return versionLess(a, b) }
