package main

// `sy uninstall [--yes] [--dry-run] [--json] [--purge]` (docs/spec/setup.md 9.5): planned
// from the ledger and applied with a backup set (so `sy setup --undo` reverses it). Kept:
// every config folder, login, link and copy, labels, the backups,
// config.toml, the outbox and the device token; `--purge` also removes config.toml and the
// state folder, after a confirmation. The timer and the hub service are in the plan shown
// before the confirmation; nothing is removed without --yes or a terminal. The ledger keeps
// the record of everything that was not removed.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/service"
	"github.com/mraj602/switchyard/internal/setup/apply"
	"github.com/mraj602/switchyard/internal/setup/launcher"
	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/plan"
	"github.com/mraj602/switchyard/internal/setup/render"
	"github.com/mraj602/switchyard/internal/setup/statuswrap"
	"github.com/mraj602/switchyard/internal/share"
	"github.com/mraj602/switchyard/internal/timer"
)

func runUninstall(args []string, stdout, stderr io.Writer) error {
	e, err := newSetupEnv(stdout, stderr)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f accountFlags
	fs.BoolVar(&f.yes, "yes", false, "apply without asking")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the plan, change nothing")
	fs.BoolVar(&f.asJSON, "json", false, "machine output on stdout")
	purge := fs.Bool("purge", false, "also remove config.toml and the state folder (outbox, token, machine id, backups)")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "sy uninstall: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	return uninstall(context.Background(), e, f, *purge)
}

// Ledger records an uninstall action removes (uninstallPlan's tags).
const (
	recPlugin   = "plugin"
	recStatus   = "status:"   // + settings file
	recEnv      = "env:"      // + settings file
	recLauncher = "launcher:" // + launcher path
	recMCP      = "mcp"
	recTerm     = "term:" // + ledger.TermPiece
)

// uninstallPlan builds the plan of 9.5 steps 1, 2, 4 and 5 from the ledger and the state. tags
// maps each action id to the ledger record it removes; left lists the records the plan leaves
// as is (changed since, root unused): the ledger keeps them.
func uninstallPlan(e *setupEnv, st model.State, led model.Ledger) (p model.Plan, warns []string, tags map[string]string, left map[string]bool) {
	var acts []model.Action
	var atags []string
	left = map[string]bool{}
	add := func(tag string, as ...model.Action) {
		for _, a := range as {
			acts = append(acts, a)
			atags = append(atags, tag)
		}
	}
	target := func(path string) *model.SettingsTarget {
		for i := range st.SettingsTargets {
			if st.SettingsTargets[i].Path == path {
				return &st.SettingsTargets[i]
			}
		}
		return nil
	}
	folderOf := func(tilde string) *model.Folder {
		for i := range st.Folders {
			if st.Folders[i].Folder == tilde {
				return &st.Folders[i]
			}
		}
		return nil
	}
	env := func(fo *model.Folder) map[string]*string {
		if fo.Default {
			return map[string]*string{"CLAUDE_CONFIG_DIR": nil}
		}
		d := fo.Dir
		return map[string]*string{"CLAUDE_CONFIG_DIR": &d}
	}
	fileExists := func(p string) []model.Precondition {
		if exists(p) {
			return []model.Precondition{{Path: p, State: model.PathState{Type: model.TypeFile}}}
		}
		return nil
	}

	// 0. Terminal pieces (terminal.md T10), first: the pane comes from sy-local, which step 1
	// removes.
	tacts, twarns := uninstallTerminal(e, st, led, left)
	warns = append(warns, twarns...)
	for _, a := range tacts {
		add(recTerm+ledger.TermPiece(a), a)
	}

	// 1. Plugin.
	if pl := led.Plugin; pl != nil && !pl.Adopted {
		done := map[string]bool{}
		for _, root := range pl.Roots {
			var owner *model.Folder
			for _, r := range st.PluginRoots {
				if r.Root == root && r.Owner != "" {
					owner = folderOf(r.Owner)
				}
			}
			if owner == nil {
				for i := range st.Folders {
					if st.Folders[i].PluginRoot == root {
						owner = &st.Folders[i]
						break
					}
				}
			}
			if owner == nil {
				warns = append(warns, "no folder uses the plugin root "+e.tilde(root)+" any more; left as is")
				left[recPlugin] = true
				continue
			}
			t := owner.SettingsTarget
			done[t] = true
			if e.claude == "" {
				continue // handled below with jsonedit
			}
			add(recPlugin,
				model.Action{Step: model.StepPlugin, Op: model.OpRun, Argv: []string{"claude", "plugin", "uninstall", plan.PluginID, "--json"},
					Env: env(owner), Touches: []string{t, filepath.Join(root, "installed_plugins.json")}, Watch: []string{root}, Pre: fileExists(t)},
				model.Action{Step: model.StepPlugin, Op: model.OpRun, Argv: []string{"claude", "plugin", "marketplace", "remove", plan.MarketplaceName, "--json"},
					Env: env(owner), Touches: []string{t, filepath.Join(root, "known_marketplaces.json")}, Watch: []string{root}})
		}
		for _, t := range pl.Targets {
			if done[t] && e.claude != "" {
				continue
			}
			ts := target(t)
			if ts == nil || !ts.Exists {
				continue
			}
			if e.claude != "" {
				if !ts.EnabledPlugins[plan.PluginID] || len(ts.Folders) == 0 {
					continue
				}
				fo := folderOf(ts.Folders[0])
				if fo == nil {
					continue
				}
				add(recPlugin, model.Action{Step: model.StepPlugin, Op: model.OpRun,
					Argv: []string{"claude", "plugin", "disable", plan.PluginID, "--json"}, Env: env(fo), Touches: []string{t}, Pre: fileExists(t)})
				continue
			}
			// claude is missing: remove the same keys with the JSON editor.
			if _, ok := ts.EnabledPlugins[plan.PluginID]; ok {
				add(recPlugin, model.Action{Step: model.StepPlugin, Op: model.OpJSONDelete, File: t,
					Pointer: "/enabledPlugins/" + plan.PluginID, Pre: fileExists(t)})
			}
			for _, m := range ts.ExtraMarketplaces {
				if m == plan.MarketplaceName {
					add(recPlugin, model.Action{Step: model.StepPlugin, Op: model.OpJSONDelete, File: t,
						Pointer: "/extraKnownMarketplaces/" + plan.MarketplaceName, Pre: fileExists(t)})
				}
			}
		}
	}

	// 2. Status line unwrapped, env key removed.
	for t, rec := range led.StatusLine {
		ts := target(t)
		if ts == nil || ts.StatusLine == nil {
			continue
		}
		cur := ts.StatusLine.Command
		if !statuswrap.IsOurs(cur) {
			warns = append(warns, "status line in "+e.tilde(t)+" was changed since; left as is")
			left[recStatus+t] = true
			continue
		}
		before, _ := json.Marshal(cur)
		switch orig, ok := statuswrap.Unwrap(cur); {
		case ok && orig != "":
			v, _ := json.Marshal(orig)
			add(recStatus+t, model.Action{Step: model.StepStatusLine, Op: model.OpJSONSet, File: t, Pointer: "/statusLine/command",
				Before: before, Value: v, Covers: ts.Folders, Pre: fileExists(t)})
		case rec.Created:
			add(recStatus+t, model.Action{Step: model.StepStatusLine, Op: model.OpJSONDelete, File: t, Pointer: "/statusLine",
				Covers: ts.Folders, Pre: fileExists(t)})
		case rec.Original != "":
			v, _ := json.Marshal(rec.Original)
			add(recStatus+t, model.Action{Step: model.StepStatusLine, Op: model.OpJSONSet, File: t, Pointer: "/statusLine/command",
				Before: before, Value: v, Covers: ts.Folders, Pre: fileExists(t)})
		}
	}
	for _, t := range led.EnvKeys {
		if ts := target(t); ts == nil || !ts.Exists {
			continue
		}
		for _, r := range st.PluginRoots {
			if r.FromEnv && len(r.Plugins) > 0 {
				var others []string
				for _, id := range r.Plugins {
					if id != plan.PluginID {
						others = append(others, id)
					}
				}
				if len(others) > 0 {
					warns = append(warns, "plugins you installed into the shared root ("+strings.Join(others, ", ")+
						") stay there; folders will see their own plugins/ again")
				}
			}
		}
		add(recEnv+t, model.Action{Step: model.StepSharing, Op: model.OpJSONDelete, File: t,
			Pointer: "/env/CLAUDE_CODE_PLUGIN_CACHE_DIR", Pre: fileExists(t)})
	}

	// 4. Launchers sy wrote that still have sy's hash: moved into the state folder (undoable).
	for _, l := range led.Launchers {
		b, err := os.ReadFile(l.Path)
		if err != nil {
			continue
		}
		if !launcher.StillOurs(b, l.SHA256) {
			warns = append(warns, "launcher "+e.tilde(l.Path)+" was changed by you; left as is")
			left[recLauncher+l.Path] = true
			continue
		}
		to := filepath.Join(e.dirs.State.Path, "setup", "removed-launchers", filepath.Base(l.Path))
		add(recLauncher+l.Path, model.Action{Step: model.StepLaunchers, Op: model.OpMove, From: l.Path, To: to,
			Note: "launcher " + filepath.Base(l.Path),
			Pre: []model.Precondition{{Path: l.Path, State: model.PathState{Type: model.TypeFile, SHA256: l.SHA256}},
				{Path: to, State: model.PathState{Type: model.TypeAbsent}}}})
	}

	// 5. MCP sync stops.
	if st.Share.MCP == "true" {
		cp := e.configPath()
		add(recMCP, model.Action{Step: model.StepConfig, Op: model.OpConfigSet, Path: cp,
			Value: json.RawMessage(`{"share.mcp":false}`), Pre: fileExists(cp)})
	}

	// Order: the terminal pieces first (as planned), then by step.
	var ordered []model.Action
	var otags []string
	for i := range tacts {
		ordered = append(ordered, acts[i])
		otags = append(otags, atags[i])
	}
	acts, atags = acts[len(tacts):], atags[len(tacts):]
	for _, s := range model.StepOrder {
		for i, a := range acts {
			if a.Step == s {
				ordered = append(ordered, a)
				otags = append(otags, atags[i])
			}
		}
	}
	p = subPlan(model.Plan{Keep: []model.Keep{}, Conflicts: []model.Conflict{}, Needs: []model.Need{}, Warnings: warns, Actions: ordered},
		func(a model.Action) (model.Action, bool) { return a, true })
	tags = map[string]string{}
	for i, a := range p.Actions {
		tags[a.ID] = otags[i]
	}
	return p, warns, tags, left
}

// uninstallTerminal is the reverse of every terminal piece in the ledger (`sy term off`):
// plan.Terminal with every piece and Off, from Ledger.Terminal. Without claude the plugin
// pieces cannot be removed: titles and tabs are, and the pane and Warp's plugin stay in the
// ledger (left) with the plan's warning. The hooks refresh of a plan is never part of it,
// nor a pane `marketplace remove sy-local` in a root the switchyard plugin's own removal
// (step 1) removes it from.
func uninstallTerminal(e *setupEnv, st model.State, led model.Ledger, left map[string]bool) ([]model.Action, []string) {
	lt := led.Terminal
	if lt == nil || lt.Empty() {
		return nil, nil
	}
	c := model.TermChoices{Pane: true, WarpPlugin: true, Titles: true, WarpTabs: true, Off: true}
	if lt.Warp != nil {
		c.Channel = lt.Warp.Channel
	}
	st.Ledger = &led
	facts, err := e.termFacts(st, c)
	if err != nil {
		for _, piece := range []string{ledger.PiecePane, ledger.PieceWarpPlugin, ledger.PieceTitles, ledger.PieceTabs} {
			left[recTerm+piece] = true
		}
		return nil, []string{"terminal pieces: " + err.Error() + "; left as is (sy term off removes them)"}
	}
	p, err := plan.Terminal(st, c, facts)
	if err != nil {
		for _, piece := range []string{ledger.PiecePane, ledger.PieceWarpPlugin, ledger.PieceTitles, ledger.PieceTabs} {
			left[recTerm+piece] = true
		}
		return nil, []string{"terminal pieces: " + err.Error() + "; left as is"}
	}
	if st.Claude.Path == "" {
		// plan.Terminal planned nothing for the plugin pieces (and warned): keep their records.
		if lt.Pane != nil {
			left[recTerm+ledger.PiecePane] = true
		}
		if lt.WarpPlugin != nil {
			left[recTerm+ledger.PieceWarpPlugin] = true
		}
	}
	step1 := map[string]bool{}
	if pl := led.Plugin; pl != nil && !pl.Adopted && e.claude != "" {
		for _, r := range pl.Roots {
			step1[r] = true
		}
	}
	var out []model.Action
	for _, a := range p.Actions {
		if a.Op == model.OpRun && ledger.TermPiece(a) == "" {
			continue // a hooks refresh: not for an uninstall
		}
		if a.Op == model.OpWriteTree {
			continue
		}
		if a.Op == model.OpRun && ledger.TermPiece(a) == ledger.PiecePane && len(a.Argv) > 3 && a.Argv[2] == "marketplace" &&
			len(a.Watch) > 0 && step1[a.Watch[0]] {
			continue // step 1 removes sy-local there
		}
		out = append(out, a)
	}
	return out, p.Warnings
}

func uninstall(ctx context.Context, e *setupEnv, f accountFlags, purge bool) error {
	if purge {
		if err := purgeable(e); err != nil {
			return exitWith(exitUsage, err)
		}
	}
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
	led, err := ledger.Read(e.dirs.State.Path)
	if err != nil {
		return fmt.Errorf("read the ledger: %w", err)
	}
	p, _, tags, left := uninstallPlan(e, st, led)
	steps := &model.UninstallSteps{Timer: led.Timer, Copies: led.Copies, Purge: purge}
	if led.HubService != nil {
		steps.HubService = led.HubService.Name
	}
	if _, err := os.Stat(service.UnitPath(e.serviceEnv(), service.AgentSpec())); err == nil {
		steps.AgentService = service.AgentName
	}
	others := steps.Timer || steps.HubService != "" || steps.AgentService != "" || len(steps.Copies) > 0
	if p.Empty() && !others && !purge {
		if f.asJSON {
			return writeJSON(e.stdout, map[string]any{"v": 1, "mode": "uninstall", "status": "nothing-installed"})
		}
		fmt.Fprintln(e.stdout, "Nothing installed by sy here.")
		return nil
	}

	in := render.Input{Mode: model.ModeDryRun, State: &st, Plan: p, Uninstall: steps}
	for _, fo := range st.Folders {
		if fo.Primary {
			in.Primary = fo.Folder
		}
	}
	show := func() error {
		if f.asJSON {
			b, err := render.JSON(in)
			if err != nil {
				return err
			}
			_, err = e.stdout.Write(append(b, '\n'))
			return err
		}
		if !p.Empty() {
			fmt.Fprint(e.stdout, render.Text(in))
		}
		for _, l := range uninstallLines(e, steps) {
			fmt.Fprintln(e.stdout, "  "+l)
		}
		return nil
	}
	if f.dryRun {
		return show()
	}
	if !f.yes {
		if err := show(); err != nil {
			return err
		}
		if !e.tty || f.asJSON {
			return exitWith(exitNeedsPerson, errors.New("no terminal: rerun with --yes to uninstall"))
		}
		n := p.Summary.Changes + len(uninstallLines(e, steps))
		if !e.confirm(fmt.Sprintf("Apply these %d changes?", n), true) {
			fmt.Fprintln(e.stdout, "Nothing changed.")
			return nil
		}
	}

	var res apply.Result
	if !p.Empty() {
		opts, err := e.applyOptions(st, "sy uninstall")
		if err != nil {
			return err
		}
		var aerr error
		res, aerr = apply.Run(ctx, p, opts, e.applyDeps(st, p))
		in.Mode, in.Backup, in.Applied = model.ModeApplied, res.Backup, res.Applied
		if res.Status == model.StatusRolledBack {
			in.Mode = model.ModeRolledBack
		}
		if aerr != nil {
			// Rolled back (or refused): nothing was removed, the ledger stays as it is.
			if err := show(); err != nil {
				return err
			}
			code := res.ExitCode
			if code == 0 {
				code = apply.ExitFailed
			}
			return exitWith(code, aerr)
		}
	}
	in.Mode = model.ModeApplied
	// 3. Timer, hub service, synced copies (not journaled: undo re-runs sy connect / sy setup).
	if steps.Timer {
		if _, err := timer.Uninstall(e.timerEnv()); err != nil {
			fmt.Fprintf(e.stderr, "sy uninstall: timer: %v\n", err)
			steps.Failed = append(steps.Failed, "timer")
		}
	}
	if steps.HubService != "" {
		if err := service.New(e.serviceEnv()).Uninstall(ctx, steps.HubService); err != nil {
			fmt.Fprintf(e.stderr, "sy uninstall: hub service: %v\n", err)
			steps.Failed = append(steps.Failed, "hub_service")
		}
	}
	if steps.AgentService != "" {
		if err := service.New(e.serviceEnv()).Uninstall(ctx, steps.AgentService); err != nil {
			fmt.Fprintf(e.stderr, "sy uninstall: agent service: %v\n", err)
			steps.Failed = append(steps.Failed, "agent_service") // the unit file stays, so a rerun finds it
		}
	}
	if len(steps.Copies) > 0 {
		if _, err := share.ForgetCopies(e.dirs.State.Path, func(string) bool { return true }); err != nil {
			fmt.Fprintf(e.stderr, "sy uninstall: synced copies: %v\n", err)
			steps.Failed = append(steps.Failed, "copies")
		}
	}
	if err := show(); err != nil {
		return err
	}
	if res.Backup != "" && !f.asJSON {
		fmt.Fprintf(e.stdout, "Backup %s (undo with: sy setup --undo %s)\n", res.Backup, res.Backup)
	}
	if err := ledger.Write(e.dirs.State.Path, uninstallLedger(led, res, tags, left, steps)); err != nil {
		return err
	}
	if len(steps.Failed) > 0 {
		return exitWith(apply.ExitFailed, fmt.Errorf("not removed: %s (kept in the ledger; run sy uninstall again)", strings.Join(steps.Failed, ", ")))
	}
	if purge {
		return purgeAll(e, f)
	}
	return nil
}

// uninstallLines is the text of the steps outside the plan.
func uninstallLines(e *setupEnv, s *model.UninstallSteps) []string {
	var out []string
	if s.Timer {
		out = append(out, "− flush timer (sy tick every minute)")
	}
	if s.HubService != "" {
		out = append(out, "− hub service "+s.HubService+" (the hub database stays)")
	}
	if s.AgentService != "" {
		out = append(out, "− sy agent service "+s.AgentService+" (peers.enabled stays; keys stay)")
	}
	for _, c := range s.Copies {
		out = append(out, "~ "+e.tilde(c)+"   synced copy → independent file (no longer synced)")
	}
	if s.Purge {
		out = append(out, "− config.toml, config.toml.bak, the marketplace folder and the state folder (--purge)")
	}
	return out
}

// uninstallLedger is what is installed after the run: the records of what was not removed
// (actions not applied, items left as is, steps that failed) stay, with what uninstall keeps
// on purpose.
func uninstallLedger(led model.Ledger, res apply.Result, tags map[string]string, left map[string]bool, steps *model.UninstallSteps) model.Ledger {
	applied := map[string]bool{}
	for _, id := range res.Applied {
		applied[id] = true
	}
	pending := map[string]bool{} // records with an action that did not run
	for id, tag := range tags {
		if !applied[id] {
			pending[tag] = true
		}
	}
	keep := func(tag string) bool { return pending[tag] || left[tag] }
	failed := map[string]bool{}
	for _, s := range steps.Failed {
		failed[s] = true
	}
	out := model.Ledger{V: 1}
	if led.Plugin != nil && !led.Plugin.Adopted && keep(recPlugin) {
		out.Plugin = led.Plugin
	}
	for t, rec := range led.StatusLine {
		if keep(recStatus + t) {
			if out.StatusLine == nil {
				out.StatusLine = map[string]model.LedgerStatus{}
			}
			out.StatusLine[t] = rec
		}
	}
	for _, t := range led.EnvKeys {
		if keep(recEnv + t) {
			out.EnvKeys = append(out.EnvKeys, t)
		}
	}
	for _, l := range led.Launchers {
		if keep(recLauncher + l.Path) {
			out.Launchers = append(out.Launchers, l)
		}
	}
	if keep(recMCP) {
		out.MCPSync = led.MCPSync
	}
	if lt := led.Terminal; lt != nil {
		t := &model.LedgerTerminal{}
		if keep(recTerm + ledger.PiecePane) {
			t.Pane = lt.Pane
		}
		if keep(recTerm + ledger.PieceWarpPlugin) {
			t.WarpPlugin = lt.WarpPlugin
		}
		if keep(recTerm + ledger.PieceTitles) {
			t.Titles, t.TitlePrior = lt.Titles, lt.TitlePrior
		}
		if keep(recTerm + ledger.PieceTabs) {
			t.Tabs, t.Warp = lt.Tabs, lt.Warp
		}
		if !t.Empty() {
			out.Terminal = t
		}
	}
	if failed["timer"] {
		out.Timer = led.Timer
	}
	if failed["hub_service"] {
		out.HubService = led.HubService
	}
	if failed["copies"] {
		out.Copies = led.Copies
	}
	return out
}

// purgeable refuses --purge on a state folder that is not sy's: it must hold sy's marker
// files (machine.json or setup/ledger.json) and must not be the home folder, an ancestor of
// it, the file system root or the config folder (SY_STATE_DIR may point anywhere).
func purgeable(e *setupEnv) error {
	dir := filepath.Clean(e.dirs.State.Path)
	bad := func(why string) error {
		return fmt.Errorf("--purge: refusing to delete %s: %s", dir, why)
	}
	if !filepath.IsAbs(dir) || filepath.Dir(dir) == dir {
		return bad("not an absolute folder below the root")
	}
	home := filepath.Clean(e.home)
	if dir == home || under(home, dir) {
		return bad("it holds the home folder")
	}
	cfg := filepath.Clean(e.dirs.Config.Path)
	if dir == cfg || under(cfg, dir) {
		return bad("it holds the config folder")
	}
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing to delete
	}
	if err != nil {
		return bad(err.Error())
	}
	if !fi.IsDir() {
		return bad("not a folder")
	}
	for _, m := range []string{"machine.json", filepath.Join("setup", "ledger.json")} {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return nil
		}
	}
	return bad("it has no machine.json or setup/ledger.json, so it is not sy's state folder")
}

// purgeAll is --purge: config.toml, config.toml.bak, the state folder (never the config
// folder itself: paths.md 2). The sy binary stays.
func purgeAll(e *setupEnv, f accountFlags) error {
	unsent := "no"
	if c, err := outbox.New(filepath.Join(e.dirs.State.Path, "outbox")).Stat(); err == nil && c.UnflushedBytes > 0 {
		unsent = humanBytes(c.UnflushedBytes) + " of"
	}
	if !f.yes {
		if !e.tty || f.asJSON {
			return exitWith(exitNeedsPerson, errors.New("--purge without --yes needs a terminal"))
		}
		if !e.confirm(fmt.Sprintf("This deletes %s unsent records and every backup. Continue?", unsent), false) {
			return nil
		}
	}
	for _, p := range []string{e.configPath(), e.configPath() + ".bak"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.RemoveAll(e.dirs.State.Path); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Purged config.toml and %s. sy itself stays: rm %s\n", e.tilde(e.dirs.State.Path), e.tilde(e.sy))
	return nil
}
