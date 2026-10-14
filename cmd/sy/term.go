package main

// `sy term` (docs/spec/terminal.md T1, T10): the terminal pieces. setup, titles on|off,
// warp on|off and off plan with plan.Terminal and apply through internal/setup/apply, so
// every change holds setup.lock, is journaled in a backup set, updates the ledger's
// `terminal` block and is reversed by `sy setup --undo`. status only reads. `focus` is
// routed to term_focus.go (WP-F).
//
// Phase 7, WP-E. WP-I reuses termFacts, termContents and termAfterApply for
// `sy setup --terminal` and `sy uninstall`.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/jsonedit"
	"github.com/mraj602/switchyard/internal/setup/apply"
	"github.com/mraj602/switchyard/internal/setup/backup"
	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/plan"
	"github.com/mraj602/switchyard/internal/setup/pluginmkt"
	"github.com/mraj602/switchyard/internal/setup/render"
	"github.com/mraj602/switchyard/internal/termenv"
	"github.com/mraj602/switchyard/internal/version"
	"github.com/mraj602/switchyard/internal/warp"
)

const termUsage = `usage: sy term <command>

  setup [--pane] [--warp-plugin] [--titles] [--warp-tabs] [--channel stable|preview]
        [--accept-update] [--dry-run] [--yes] [--json]
  titles on|off        Switchyard tab titles (and Claude Code's own title off while on)
  warp on|off          a Warp tab config per account (sy open <label> opens one)
  off                  remove every terminal piece sy installed
  status [--json]      what is on, per folder and settings target
  focus --session <id> | --url <u>   bring that session's Warp tab forward

  setup, titles, warp and off take --dry-run, --yes and --json; every change is journaled
  and undone by sy setup --undo.

  Warp's plugin comes from a branch (main), not a fixed commit: sy records the commit it
  got and sy doctor warns when the checkout moves. Read what changed, then accept the new
  commit with: sy term setup --warp-plugin --accept-update (it implies --warp-plugin).
`

func runTerm(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, termUsage)
		return errUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stderr, termUsage)
		return nil
	case "focus":
		return runTermFocus(args[1:], stdout, stderr)
	case "setup", "off", "status":
	case "titles", "warp":
		if len(args) < 2 || (args[1] != "on" && args[1] != "off") {
			fmt.Fprintf(stderr, "sy term %s: want on or off\n", args[0])
			return errUsage
		}
	default:
		fmt.Fprintf(stderr, "sy term: unknown command %q\n", args[0])
		fmt.Fprint(stderr, termUsage)
		return errUsage
	}
	e, err := newSetupEnv(stdout, stderr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return termMain(ctx, e, args)
}

// termFlags are the mode flags of the plan commands.
type termFlags struct {
	dryRun, yes, asJSON bool
	// acceptWarp: record the commit Warp's plugin checkout is at now (--accept-update).
	acceptWarp bool
	c          model.TermChoices
}

func termMain(ctx context.Context, e *setupEnv, args []string) error {
	sub, rest := args[0], args[1:]
	what := "sy term " + sub
	if sub == "titles" || sub == "warp" {
		what += " " + rest[0]
		rest = rest[1:]
	}
	fs := flag.NewFlagSet(what, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { fmt.Fprint(e.stderr, termUsage) }
	var f termFlags
	fs.BoolVar(&f.asJSON, "json", false, "machine output on stdout")
	if sub != "status" {
		fs.BoolVar(&f.dryRun, "dry-run", false, "plan and print, change nothing")
		fs.BoolVar(&f.yes, "yes", false, "apply without asking")
	}
	if sub == "setup" {
		fs.BoolVar(&f.c.Pane, "pane", false, "the switchyard-pane mod in every settings target")
		fs.BoolVar(&f.c.WarpPlugin, "warp-plugin", false, "Warp's Claude Code plugin in every settings target")
		fs.BoolVar(&f.c.Titles, "titles", false, "Switchyard tab titles")
		fs.BoolVar(&f.c.WarpTabs, "warp-tabs", false, "a Warp tab config per account")
		fs.BoolVar(&f.acceptWarp, "accept-update", false, "record the commit Warp's plugin checkout moved to (implies --warp-plugin)")
	}
	if sub == "setup" || sub == "warp" || sub == "status" {
		fs.StringVar(&f.c.Channel, "channel", "", "Warp channel: stable (default) or preview")
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintf(e.stderr, "%s: unexpected argument %q\n", what, pos[0])
		return errUsage
	}
	if !warp.ValidChannel(f.c.Channel) {
		fmt.Fprintf(e.stderr, "%s: --channel must be stable or preview\n", what)
		return errUsage
	}
	switch {
	case sub == "status":
		return termStatus(ctx, e, f)
	case sub == "setup":
		if f.acceptWarp {
			f.c.WarpPlugin = true
		}
		if !f.c.Any() {
			fmt.Fprintf(e.stderr, "%s: choose at least one of --pane, --warp-plugin, --titles, --warp-tabs\n", what)
			return errUsage
		}
	case sub == "titles":
		f.c.Titles, f.c.Off = true, args[1] == "off"
	case sub == "warp":
		f.c.WarpTabs, f.c.Off = true, args[1] == "off"
	case sub == "off":
		f.c = model.TermChoices{Pane: true, WarpPlugin: true, Titles: true, WarpTabs: true, Off: true}
	}
	return termApply(ctx, e, f, what)
}

// termEnv is the injected environment of the terminal packages for this process.
func (e *setupEnv) termEnv() termenv.Env {
	env := termenv.FromOS()
	env.Home, env.GOOS, env.Getenv = e.home, e.goos, e.getenv
	env.XDGDataHome = e.getenv("XDG_DATA_HOME")
	return env
}

// termFacts reads what plan.Terminal needs besides the State (plan.TermFacts).
func (e *setupEnv) termFacts(st model.State, c model.TermChoices) (plan.TermFacts, error) {
	f := plan.TermFacts{TitleEnv: map[string]string{}}
	for _, t := range st.SettingsTargets {
		if !t.Exists {
			continue
		}
		raw, ok, err := jsonedit.Read(t.Path, ledger.TitleEnvPointer)
		if err != nil {
			return f, fmt.Errorf("read %s: %w", e.tilde(t.Path), err)
		}
		if ok {
			var b bytes.Buffer
			if json.Compact(&b, raw) == nil {
				raw = b.Bytes()
			}
			f.TitleEnv[t.Path] = string(raw)
		}
	}
	if cf, err := config.Load(e.dirs.Config.Path); err == nil {
		if cfg, err := cf.Typed(); err == nil {
			f.TitlesOn = cfg.Terminal.Titles
		}
	}
	mkt := filepath.Join(st.StateDir, "plugin", "marketplace")
	if fi, err := os.Stat(mkt); err == nil && fi.IsDir() {
		f.MarketExists = true
		f.MarketCurrent = e.marketCurrent(mkt)
	}
	f.Warp = warp.Detect(e.termEnv(), c.Channel)
	folders := []string{f.Warp.Folder}
	if st.Ledger != nil && st.Ledger.Terminal != nil {
		for _, t := range st.Ledger.Terminal.Tabs {
			folders = append(folders, filepath.Dir(t.Path))
		}
	}
	seen := map[string]bool{}
	for _, d := range folders {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		found, err := warp.Scan(d)
		if err != nil {
			return f, fmt.Errorf("read %s: %w", e.tilde(d), err)
		}
		f.Tabs = append(f.Tabs, found...)
	}
	_, err := exec.LookPath("jq")
	f.JQ = err == nil
	return f, nil
}

// marketCurrent reports whether the marketplace at dir holds exactly what this sy writes.
func (e *setupEnv) marketCurrent(dir string) bool {
	tmp, err := os.MkdirTemp("", "sy-market-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "m")
	if err := pluginmkt.New(e.runner).WriteMarketplace(out, e.sy, version.Version); err != nil {
		return false
	}
	want, err1 := backup.TreeHash(out)
	have, err2 := backup.TreeHash(dir)
	return err1 == nil && err2 == nil && want == have
}

// termContents is apply.Deps.Contents for a plan that may hold launchers and tab configs.
func (e *setupEnv) termContents(st model.State, p model.Plan, f plan.TermFacts) func(a model.Action) ([]byte, error) {
	tabs := plan.TabContents(st, f)
	launchers := e.contentsFor(st, p)
	return func(a model.Action) ([]byte, error) {
		if warp.IsTabPath(a.Path) {
			return tabs(a)
		}
		return launchers(a)
	}
}

// termAfterApply records what the ledger can only learn after the run, and forgets what the
// plan found is no longer sy's. It never fails the command; problems are printed.
//
//   - The commit Warp's marketplace checkout resolved to (`main` cannot be pinned, risk 7):
//     only when this plan added the marketplace or installed the plugin, when no commit is
//     recorded yet, or when the person accepted a moved checkout (acceptWarp: `sy term setup
//     --warp-plugin --accept-update`). A plain re-run never moves it, so `sy doctor` keeps
//     warning about a checkout that moved behind sy's back.
//   - Tab configs the plan found edited by the person or gone (kept by them, never by sy).
//   - Settings targets and plugin roots an `off` found with nothing of sy's left
//     (plan.KeepAlreadyOff), and a sy-local the pane added that the switchyard plugin now
//     uses (plan.KeepForSwitch).
func (e *setupEnv) termAfterApply(p model.Plan, acceptWarp bool) {
	led, err := ledger.Read(e.dirs.State.Path)
	if err != nil || led.Terminal == nil {
		if acceptWarp {
			fmt.Fprintln(e.stderr, "sy term: --accept-update: Warp's plugin is not installed by sy; nothing to accept")
		}
		return
	}
	changed := false
	if w := led.Terminal.WarpPlugin; w != nil {
		ran := false
		for _, a := range p.Actions {
			if a.Op == model.OpRun && ledger.TermPiece(a) == ledger.PieceWarpPlugin && len(a.Argv) > 3 &&
				(a.Argv[2] == "install" || (a.Argv[2] == "marketplace" && a.Argv[3] == "add")) {
				ran = true
			}
		}
		if ran || acceptWarp || w.Commit == "" {
			cur := ""
			for _, root := range append(append([]string{}, w.Roots...), w.Installed...) {
				if cur = warpCommit(root); cur != "" {
					break
				}
			}
			switch {
			case cur == "" && acceptWarp:
				fmt.Fprintln(e.stderr, "sy term: --accept-update: cannot read the commit of Warp's plugin checkout; nothing recorded")
			case cur != "" && cur != w.Commit:
				if acceptWarp {
					was := w.Commit
					if was == "" {
						was = "nothing"
					}
					fmt.Fprintf(e.stdout, "Accepted Warp's plugin checkout at %s (sy had recorded %s).\n", cur, was)
				}
				w.Commit, changed = cur, true
			case cur != "" && acceptWarp:
				fmt.Fprintf(e.stdout, "Warp's plugin checkout is at %s, as recorded.\n", cur)
			}
		}
	} else if acceptWarp {
		fmt.Fprintln(e.stderr, "sy term: --accept-update: Warp's plugin is not installed by sy; nothing to accept")
	}
	forget := map[string]bool{}
	gone := map[string]map[string]bool{ledger.PiecePane: {}, ledger.PieceWarpPlugin: {}}
	for _, k := range p.Keep {
		switch {
		case k.What == "tab" && (k.Note == "yours now" || k.Note == "already gone"):
			forget[e.expand(k.Path)] = true
		case gone[k.What] != nil && k.Path != "" && (k.Note == plan.KeepAlreadyOff || k.Note == plan.KeepForSwitch):
			gone[k.What][e.expand(k.Path)] = true
		}
	}
	drop := func(xs []string, rm map[string]bool) []string {
		var out []string
		for _, x := range xs {
			if rm[x] {
				changed = true
				continue
			}
			out = append(out, x)
		}
		if out == nil && xs != nil {
			out = []string{}
		}
		return out
	}
	t := led.Terminal
	if g := gone[ledger.PiecePane]; len(g) > 0 && t.Pane != nil {
		t.Pane.Targets, t.Pane.Installed, t.Pane.Markets = drop(t.Pane.Targets, g), drop(t.Pane.Installed, g), drop(t.Pane.Markets, g)
		if len(t.Pane.Targets) == 0 && len(t.Pane.Installed) == 0 && len(t.Pane.Markets) == 0 {
			t.Pane = nil
		}
	}
	if g := gone[ledger.PieceWarpPlugin]; len(g) > 0 && t.WarpPlugin != nil {
		w := t.WarpPlugin
		w.Targets, w.Installed, w.Roots = drop(w.Targets, g), drop(w.Installed, g), drop(w.Roots, g)
		if len(w.Targets) == 0 && len(w.Installed) == 0 && len(w.Roots) == 0 {
			t.WarpPlugin = nil
		}
	}
	if len(forget) > 0 {
		var keep []model.LedgerTab
		for _, x := range t.Tabs {
			if forget[x.Path] {
				changed = true
				continue
			}
			keep = append(keep, x)
		}
		t.Tabs = keep
		if len(keep) == 0 {
			t.Warp = nil
		}
	}
	if t.Empty() {
		led.Terminal = nil
	}
	if changed {
		if err := ledger.Write(e.dirs.State.Path, led); err != nil {
			fmt.Fprintf(e.stderr, "sy term: ledger: %v\n", err)
		}
	}
}

// warpCommit is the commit Warp's marketplace checkout in root is at ("" when unknown):
// warp.PluginCommit (installLocation from known_marketplaces.json, then its HEAD read from
// the files) and, failing that, `git -C <dir> rev-parse HEAD` (read only).
func warpCommit(root string) string {
	if c := warp.PluginCommit(root, plan.WarpMarketplace); c != "" {
		return c
	}
	dir := warp.PluginCheckout(root, plan.WarpMarketplace)
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// termApply is the plan → show → confirm → apply flow of setup, titles, warp and off.
func termApply(ctx context.Context, e *setupEnv, f termFlags, what string) error {
	if !f.dryRun {
		unlock, err := e.lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	if len(st.IncompleteSets) > 0 && !f.dryRun {
		return exitWith(exitUsage, fmt.Errorf("an earlier sy setup run was interrupted (%s): run sy setup first, which undoes it", strings.Join(st.IncompleteSets, ", ")))
	}
	facts, err := e.termFacts(st, f.c)
	if err != nil {
		return err
	}
	p, err := plan.Terminal(st, f.c, facts)
	if err != nil {
		if errors.Is(err, plan.ErrTermRefused) {
			return exitWith(apply.ExitPrecondition, err)
		}
		fmt.Fprintf(e.stderr, "%s: %v\n", what, err)
		return exitWith(exitUsage, nil)
	}
	in := render.Input{Mode: model.ModeDryRun, Plan: p}
	show := func() error {
		if f.asJSON {
			b, err := render.JSON(in)
			if err != nil {
				return err
			}
			_, err = e.stdout.Write(append(b, '\n'))
			return err
		}
		fmt.Fprint(e.stdout, termText(e, what, in))
		return nil
	}
	if f.dryRun {
		return show()
	}
	if p.Empty() {
		e.termAfterApply(p, f.acceptWarp)
		if f.asJSON {
			return show()
		}
		fmt.Fprint(e.stdout, termText(e, what, in))
		return nil
	}
	if !f.yes {
		if err := show(); err != nil {
			return err
		}
		if !e.tty || f.asJSON {
			return exitWith(exitNeedsPerson, errors.New("no terminal: rerun with --yes to apply this plan"))
		}
		if !e.confirm(fmt.Sprintf("Apply these %d changes?", p.Summary.Changes), true) {
			fmt.Fprintln(e.stdout, "Nothing changed.")
			return nil
		}
	}
	opts, err := e.applyOptions(st, what)
	if err != nil {
		return err
	}
	deps := e.applyDeps(st, p)
	deps.Contents = e.termContents(st, p, facts)
	res, aerr := apply.Run(ctx, p, opts, deps)
	if aerr == nil {
		e.termAfterApply(p, f.acceptWarp)
	}
	in.Mode, in.Backup, in.Applied = model.ModeApplied, res.Backup, res.Applied
	if res.Status == model.StatusRolledBack {
		in.Mode = model.ModeRolledBack
	}
	if err := show(); err != nil {
		return err
	}
	if aerr != nil {
		code := res.ExitCode
		if code == 0 {
			code = apply.ExitFailed
		}
		return exitWith(code, aerr)
	}
	return nil
}

// termText is the human plan of a terminal run (the setup layout, one section per step).
func termText(e *setupEnv, what string, in render.Input) string {
	p := in.Plan
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	switch in.Mode {
	case model.ModeDryRun:
		line("%s: plan %s", what, p.PlanID)
	case model.ModeApplied:
		line("%s: applied %s", what, p.PlanID)
	case model.ModeRolledBack:
		line("%s: rolled back %s (nothing changed)", what, p.PlanID)
	}
	section := ""
	for _, a := range p.Actions {
		title := map[model.Step]string{model.StepPlugin: "Plugin", model.StepTerminal: "Terminal", model.StepConfig: "Config"}[a.Step]
		if title == "" {
			title = string(a.Step)
		}
		if title != section {
			section = title
			line("")
			line("%s", title)
		}
		switch a.Op {
		case model.OpWriteTree:
			line("  + write  %s/   %s", e.tilde(a.Path), a.Note)
		case model.OpRun:
			as := "~/.claude"
			if d := a.Env["CLAUDE_CONFIG_DIR"]; d != nil {
				as = e.tilde(*d)
			}
			line("  + run    %s   as %s", strings.Join(a.Argv, " "), as)
			var edits []string
			for _, t := range a.Touches {
				if filepath.Base(filepath.Dir(t)) == "journal" {
					continue // the journal mark (ledger.JournalMark)
				}
				edits = append(edits, e.tilde(t))
			}
			if len(edits) > 0 {
				line("             edits %s", strings.Join(edits, ", "))
			}
			if a.Note != "" {
				line("             %s", a.Note)
			}
		case model.OpJSONSet:
			line("  + set    %s  %s = %s   %s", e.tilde(a.File), ledger.TitleEnvKey, a.Value, a.Note)
			if len(a.Covers) > 1 {
				line("             covers %s", strings.Join(a.Covers, ", "))
			}
		case model.OpJSONDelete:
			line("  - delete %s  %s   %s", e.tilde(a.File), ledger.TitleEnvKey, a.Note)
		case model.OpMkdir:
			line("  + mkdir  %s   %s", e.tilde(a.Path), a.Note)
		case model.OpWriteFile:
			line("  + write  %s   %s", e.tilde(a.Path), a.Note)
		case model.OpMoveToBackup:
			line("  - remove %s   %s (kept in the backup set)", e.tilde(a.Path), a.Note)
		case model.OpConfigSet:
			line("  + set    %s = %s   %s", a.Key, a.Value, e.tilde(a.Path))
		default:
			line("  + %s %s", a.Op, a.Path)
		}
	}
	if len(p.Keep) > 0 {
		line("")
		for _, k := range p.Keep {
			if k.Path != "" {
				line("  = %s %s   %s", k.What, k.Path, k.Note)
			} else {
				line("  = %s   %s", k.What, k.Note)
			}
		}
	}
	if len(p.Warnings) > 0 {
		line("")
		for _, w := range p.Warnings {
			line("  ! %s", w)
		}
	}
	line("")
	switch {
	case p.Empty():
		line("0 changes: everything is already as asked.")
	default:
		line("%d changes, %d backed up, 0 logins touched.", p.Summary.Changes, p.Summary.BackedUp)
	}
	if in.Backup != "" && in.Mode == model.ModeApplied {
		line("Backup %s (undo with: sy setup --undo %s)", in.Backup, in.Backup)
	}
	return b.String()
}

// ---- status ------------------------------------------------------------------------------

type termTargetStatus struct {
	Path       string   `json:"path"`
	Folders    []string `json:"folders"`
	Titles     bool     `json:"titles"` // CLAUDE_CODE_DISABLE_TERMINAL_TITLE = "1"
	Pane       bool     `json:"pane"`
	WarpPlugin bool     `json:"warp_plugin"`
}

type termTabStatus struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	// State: ours (sy's hash), yours (edited since), not-ours (no marker), missing.
	State string `json:"state"`
}

type termStatusReport struct {
	V      int `json:"v"`
	Titles struct {
		On          bool   `json:"on"`
		SideEffects string `json:"side_effects,omitempty"`
	} `json:"titles"`
	Pane struct {
		BySy          bool   `json:"by_sy"`
		ClaudeVersion string `json:"claude_version"`
		Minimum       string `json:"minimum"`
		VersionOK     bool   `json:"version_ok"`
	} `json:"pane"`
	WarpPlugin struct {
		BySy   bool   `json:"by_sy"`
		Ref    string `json:"ref,omitempty"`
		Commit string `json:"commit,omitempty"`
		JQ     bool   `json:"jq"`
	} `json:"warp_plugin"`
	Warp    warp.Install       `json:"warp"`
	Tabs    []termTabStatus    `json:"tabs"`
	Targets []termTargetStatus `json:"targets"`
}

func termStatus(ctx context.Context, e *setupEnv, f termFlags) error {
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	facts, err := e.termFacts(st, f.c)
	if err != nil {
		return err
	}
	var led model.LedgerTerminal
	if st.Ledger != nil && st.Ledger.Terminal != nil {
		led = *st.Ledger.Terminal
	}
	r := termStatusReport{V: 1, Warp: facts.Warp, Tabs: []termTabStatus{}, Targets: []termTargetStatus{}}
	r.Titles.On = facts.TitlesOn
	if r.Titles.On {
		r.Titles.SideEffects = plan.TitleSideEffects
	}
	r.Pane.BySy = led.Pane != nil
	r.Pane.ClaudeVersion, r.Pane.Minimum = st.Claude.Version, plan.PaneMinClaude
	r.Pane.VersionOK = st.Claude.Version != "" && !plan.VersionLess(st.Claude.Version, plan.PaneMinClaude)
	if w := led.WarpPlugin; w != nil {
		r.WarpPlugin.BySy, r.WarpPlugin.Ref, r.WarpPlugin.Commit = true, w.Ref, w.Commit
	}
	r.WarpPlugin.JQ = facts.JQ
	for _, t := range st.SettingsTargets {
		r.Targets = append(r.Targets, termTargetStatus{Path: t.Path, Folders: t.Folders,
			Titles: facts.TitleEnv[t.Path] == `"1"`, Pane: t.EnabledPlugins[plan.PaneID], WarpPlugin: t.EnabledPlugins[plan.WarpPluginID]})
	}
	recorded := map[string]string{}
	for _, t := range led.Tabs {
		recorded[t.Path] = t.SHA256
	}
	seen := map[string]bool{}
	for _, fd := range facts.Tabs {
		seen[fd.Path] = true
		s := termTabStatus{Path: fd.Path, Label: fd.Label, State: "not-ours"}
		switch sum, ok := recorded[fd.Path]; {
		case ok && sum == fd.SHA256:
			s.State = "ours"
		case ok, fd.Marker:
			s.State = "yours"
		}
		r.Tabs = append(r.Tabs, s)
	}
	for _, t := range led.Tabs {
		if !seen[t.Path] {
			r.Tabs = append(r.Tabs, termTabStatus{Path: t.Path, Label: t.Label, State: "missing"})
		}
	}
	if f.asJSON {
		return writeJSON(e.stdout, r)
	}
	w := e.stdout
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	fmt.Fprintf(w, "Titles       %s\n", onOff(r.Titles.On))
	if r.Titles.On {
		fmt.Fprintf(w, "             %s\n", plan.TitleSideEffects)
	}
	ver := r.Pane.ClaudeVersion
	if ver == "" {
		ver = "not found"
	}
	fmt.Fprintf(w, "Pane         %s (Claude Code %s, mod minimum %s)\n", onOff(r.Pane.BySy), ver, r.Pane.Minimum)
	wp := onOff(r.WarpPlugin.BySy)
	if r.WarpPlugin.BySy {
		wp += " (" + plan.WarpSource
		if r.WarpPlugin.Commit != "" {
			wp += " at " + r.WarpPlugin.Commit[:min(12, len(r.WarpPlugin.Commit))]
		}
		wp += ")"
	}
	if !r.WarpPlugin.JQ {
		wp += "; jq not on PATH"
	}
	fmt.Fprintf(w, "Warp plugin  %s\n", wp)
	switch {
	case r.Warp.Detected():
		how := "installed"
		if !r.Warp.Present {
			how = "running (TERM_PROGRAM)"
		}
		fmt.Fprintf(w, "Warp         %s, %s; tab configs in %s\n", r.Warp.Channel, how, e.tilde(r.Warp.Folder))
	default:
		fmt.Fprintln(w, "Warp         not found")
	}
	for _, t := range r.Tabs {
		fmt.Fprintf(w, "  tab %-12s %-9s %s\n", t.Label, t.State, e.tilde(t.Path))
	}
	for _, t := range r.Targets {
		fmt.Fprintf(w, "%s (%s)\n", e.tilde(t.Path), strings.Join(t.Folders, ", "))
		fmt.Fprintf(w, "  titles env %s · pane %s · warp plugin %s\n", onOff(t.Titles), onOff(t.Pane), onOff(t.WarpPlugin))
	}
	return nil
}
