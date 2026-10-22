package main

// `sy open [<label>] [--here]` (docs/spec/terminal.md T1, T8): list accounts, or open one in
// a new Warp tab (its tab config) or in this terminal. It never picks an account for the
// person (it may suggest which account has room; you choose), and it
// writes nothing.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/record"
	"github.com/mraj602/switchyard/internal/setup/launcher"
	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/plan"
	"github.com/mraj602/switchyard/internal/status"
	"github.com/mraj602/switchyard/internal/termctl"
	"github.com/mraj602/switchyard/internal/warp"
)

// Exit codes of sy open (T1).
const exitOpenNoTab = 4

// newOpener returns the opener `sy open` hands the tab-config URI to: internal/termctl's
// OS opener (an argv, never a shell; the fakeopen seam in test builds), with its stderr in
// <state>/logs/sy.log. Tests replace it.
var newOpener = func(e *setupEnv) termctl.Opener {
	env := e.termEnv()
	log, _ := focusSyLog(env) // closed at exit: sy open ends right after the open
	return termctl.NewOpener(env, log)
}

// execLauncher replaces this process with the launcher (open_exec_*.go). Tests replace it.
var execLauncher = execFile

func runOpen(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	here := fs.Bool("here", false, "run the account's launcher in this terminal, even inside Warp")
	asJSON := fs.Bool("json", false, "with no label: the account list as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: sy open [<label>] [--here] [--json]\n\n"+
			"  no label   list accounts (label, folder, launcher, login, 5h %, reset); opens nothing\n"+
			"  <label>    in Warp: open the account's tab config (sy term warp on writes them);\n"+
			"             elsewhere, or with --here: run its launcher in this terminal")
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		fs.Usage()
		return errUsage
	}
	e, err := newSetupEnv(stdout, stderr)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return openList(context.Background(), e, *asJSON)
	}
	return openLabel(context.Background(), e, pos[0], *here)
}

// openAccount is one account as `sy open` sees it.
type openAccount struct {
	Label    string   `json:"label"`
	Folder   string   `json:"folder"`
	Launcher string   `json:"launcher,omitempty"` // absolute path, "" when it has none
	Login    string   `json:"login"`
	FiveHour *float64 `json:"five_hour_pct"`
	ResetsAt string   `json:"five_hour_resets_at,omitempty"`
	MostRoom bool     `json:"most_room,omitempty"`
}

// openFolders lists the account folders without asking claude anything (fast enough to
// sit in front of a new tab): ~/.claude, every ~/.claude-* folder, and config's
// slots.labels and slots.extra, labelled as sy setup labels them.
func openFolders(e *setupEnv) []model.Folder {
	cfg := config.Defaults()
	if cf, err := config.Load(e.dirs.Config.Path); err == nil {
		if c, err := cf.Typed(); err == nil {
			cfg = c
		}
	}
	var dirs []string
	if fi, err := os.Stat(filepath.Join(e.home, ".claude")); err == nil && fi.IsDir() {
		dirs = append(dirs, filepath.Join(e.home, ".claude"))
	}
	if ents, err := os.ReadDir(e.home); err == nil {
		for _, en := range ents {
			if strings.HasPrefix(en.Name(), ".claude-") {
				if fi, err := os.Stat(filepath.Join(e.home, en.Name())); err == nil && fi.IsDir() {
					dirs = append(dirs, filepath.Join(e.home, en.Name()))
				}
			}
		}
	}
	for k := range cfg.Slots.Labels {
		dirs = append(dirs, e.expand(k))
	}
	for _, k := range cfg.Slots.Extra {
		dirs = append(dirs, e.expand(k))
	}
	seen := map[string]bool{}
	var out []model.Folder
	for _, d := range dirs {
		d = filepath.Clean(d)
		if seen[d] {
			continue
		}
		seen[d] = true
		f := model.Folder{Folder: e.tilde(d), Dir: d, Default: d == filepath.Join(e.home, ".claude")}
		f.Label = cfg.Slots.Labels[f.Folder]
		f.Label = plan.FolderLabel(f)
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Folder < out[j].Folder
	})
	return out
}

// launcherPath is the absolute path of folder's launcher file, "" when there is none (a
// shell function cannot be run from here).
func (e *setupEnv) launcherPath(f model.Folder) string {
	if f.Default {
		return e.claude
	}
	file := launcher.FileName(e.goos, launcher.Name(f.Folder))
	cand := []string{filepath.Join(e.home, ".local", "bin", file)}
	if led, err := ledger.Read(e.dirs.State.Path); err == nil {
		for _, l := range led.Launchers {
			if filepath.Base(l.Path) == file {
				cand = append([]string{l.Path}, cand...)
			}
		}
	}
	for _, c := range cand {
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
			return c
		}
	}
	if p, err := exec.LookPath(file); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return ""
}

// limitsBySlot is the newest five-hour sample of every slot in the outbox (7 days).
func (e *setupEnv) limitsBySlot() map[string]struct {
	pct    *float64
	resets string
} {
	out := map[string]struct {
		pct    *float64
		resets string
	}{}
	recs, err := status.Load(filepath.Join(e.dirs.State.Path, "outbox"), time.Now().Add(-status.Window))
	if err != nil {
		return out
	}
	newest := map[string]string{}
	for _, r := range recs {
		if r.Kind != record.KindLimitSample || r.Slot == "" || r.TS < newest[r.Slot] {
			continue
		}
		var pct *float64
		if raw, ok := r.Derived["five_hour_pct"]; ok {
			var v float64
			if json.Unmarshal(raw, &v) == nil {
				pct = &v
			}
		}
		if pct == nil {
			continue
		}
		var resets string
		if raw, ok := r.Derived["five_hour_resets_at"]; ok {
			_ = json.Unmarshal(raw, &resets)
		}
		newest[r.Slot] = r.TS
		out[r.Slot] = struct {
			pct    *float64
			resets string
		}{pct, resets}
	}
	return out
}

func openList(ctx context.Context, e *setupEnv, asJSON bool) error {
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	limits := e.limitsBySlot()
	var accts []openAccount
	most := -1
	for _, f := range st.Folders {
		f.Label = plan.FolderLabel(f)
		a := openAccount{Label: f.Label, Folder: f.Folder, Launcher: e.launcherPath(f), Login: "not logged in"}
		if f.LoggedIn {
			a.Login = strings.TrimSpace(f.Email + " " + f.Plan)
		}
		if l, ok := limits[f.Folder]; ok {
			a.FiveHour, a.ResetsAt = l.pct, l.resets
			if f.LoggedIn && (most < 0 || *l.pct < *accts[most].FiveHour) {
				most = len(accts)
			}
		}
		accts = append(accts, a)
	}
	if most >= 0 {
		accts[most].MostRoom = true
	}
	if asJSON {
		if accts == nil {
			accts = []openAccount{}
		}
		return writeJSON(e.stdout, map[string]any{"v": 1, "accounts": accts})
	}
	w := e.stdout
	if len(accts) == 0 {
		fmt.Fprintln(w, "No accounts found (sy setup adds them).")
		return nil
	}
	for _, a := range accts {
		launch := "-"
		if a.Launcher != "" {
			launch = filepath.Base(a.Launcher)
		}
		five, reset := "-", ""
		if a.FiveHour != nil {
			five = fmt.Sprintf("%.0f%%", *a.FiveHour)
		}
		if t, err := time.Parse(time.RFC3339, a.ResetsAt); err == nil {
			reset = "resets " + t.Local().Format("15:04")
		}
		mark := ""
		if a.MostRoom {
			mark = "  ← most room"
		}
		fmt.Fprintf(w, "%-12s %-14s %-12s %-34s 5h %-5s %-12s%s\n", a.Label, a.Folder, launch, a.Login, five, reset, mark)
	}
	fmt.Fprintln(w, "\nsy open <label> opens one (a Warp tab in Warp, else here). You choose; sy never picks.")
	return nil
}

func openLabel(ctx context.Context, e *setupEnv, label string, here bool) error {
	var acct *model.Folder
	folders := openFolders(e)
	for i := range folders {
		if folders[i].Label == label {
			acct = &folders[i]
		}
	}
	if acct == nil {
		var labels []string
		for _, f := range folders {
			labels = append(labels, f.Label)
		}
		return exitWith(exitUsage, fmt.Errorf("no account labelled %q (accounts: %s)", label, strings.Join(labels, ", ")))
	}
	if !here && e.getenv("TERM_PROGRAM") == warp.TermProgram {
		uri, err := e.tabURI(label)
		if err != nil {
			return exitWith(exitOpenNoTab, err)
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := newOpener(e).Open(cctx, uri); err != nil {
			return exitWith(1, fmt.Errorf("open %s: %w", uri, err))
		}
		fmt.Fprintf(e.stdout, "Opened %s in a new Warp tab (%s).\n", label, uri)
		return nil
	}
	path := e.launcherPath(*acct)
	if path == "" {
		return exitWith(exitOpenNoTab, fmt.Errorf("%s has no launcher file to run (%s): sy setup writes one", acct.Folder, launcher.Name(acct.Folder)))
	}
	return execLauncher(path, []string{path})
}

// tabURI finds label's tab config in Warp's folders (the ledger's first, then stable, then
// Preview) and returns the URI that opens it. Only a file sy wrote (its marker line) is
// opened; anything else is "missing".
func (e *setupEnv) tabURI(label string) (string, error) {
	var folders []string
	if led, err := ledger.Read(e.dirs.State.Path); err == nil && led.Terminal != nil && led.Terminal.Warp != nil {
		folders = append(folders, led.Terminal.Warp.Folder)
	}
	env := e.termEnv()
	folders = append(folders, warp.Folder(env, warp.Stable), warp.Folder(env, warp.Preview))
	for _, d := range folders {
		if d == "" {
			continue
		}
		found, err := warp.Scan(d)
		if err != nil {
			continue
		}
		for _, f := range found {
			if f.Label == label && f.Marker {
				uri := warp.TabURI(warp.ChannelOf(d), label)
				if !warp.ValidTabURI(uri) {
					return "", fmt.Errorf("refusing %q", uri)
				}
				return uri, nil
			}
		}
	}
	return "", fmt.Errorf("no tab config for %s: run sy term warp on", label)
}
