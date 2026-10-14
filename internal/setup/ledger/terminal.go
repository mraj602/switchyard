package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/warp"
)

// The ledger's `terminal` block (docs/spec/terminal.md T10, model.LedgerTerminal): what the
// terminal pieces have installed now. Update records it from the plan's actions; Revert, which
// has no plan, from the manifest entries alone.
//
// The `claude plugin …` runs of the pane and of Warp's plugin cannot be told from the
// switchyard plugin's own runs by their paths (all touch settings.json and the plugin
// registries). So plan.Terminal adds one more path to every such run's touches, a journal
// mark `<state>/term/journal/<piece>.<verb>` ("pane.install", "warp-plugin.uninstall", …).
// The run never creates it: it is recorded absent before and after, and undo has nothing to
// do for it, but the manifest entry then names the piece and the verb.

// Terminal constants shared with plan.Terminal.
const (
	// TitleEnvKey is the variable `sy term titles on` sets to "1" in every settings target.
	TitleEnvKey = "CLAUDE_CODE_DISABLE_TERMINAL_TITLE"
	// TitleEnvPointer is its pointer in a settings file.
	TitleEnvPointer = "/env/" + TitleEnvKey
	// TitlesConfigKey is the config.toml key of the titles piece.
	TitlesConfigKey = "terminal.titles"

	// PiecePane and PieceWarpPlugin name the pieces in journal marks.
	PiecePane       = "pane"
	PieceWarpPlugin = "warp-plugin"
	// PieceHooks marks the switchyard plugin's refresh (marketplace update, plugin update)
	// that makes new hooks active; it changes no ledger piece.
	PieceHooks = "hooks"

	// Verbs of journal marks.
	VerbInstall           = "install"
	VerbEnable            = "enable"
	VerbDisable           = "disable"
	VerbUninstall         = "uninstall"
	VerbMarketplaceAdd    = "marketplace-add"
	VerbMarketplaceRemove = "marketplace-remove"
	VerbMarketplaceUpdate = "marketplace-update"
	VerbUpdate            = "update"

	// WarpRef is the branch Warp's marketplace is added at (no commit can be pinned,
	// terminal.md "Claude Code facts" 4).
	WarpRef = "main"
)

// JournalMark is the journal mark path of a terminal `run` (see above).
func JournalMark(stateDir, piece, verb string) string {
	return filepath.Join(stateDir, "term", "journal", piece+"."+verb)
}

// parseMark returns the piece and verb of a journal mark path.
func parseMark(p string) (piece, verb string, ok bool) {
	dir := filepath.Dir(p)
	if filepath.Base(dir) != "journal" || filepath.Base(filepath.Dir(dir)) != "term" {
		return "", "", false
	}
	piece, verb, ok = strings.Cut(filepath.Base(p), ".")
	if !ok || (piece != PiecePane && piece != PieceWarpPlugin && piece != PieceHooks) {
		return "", "", false
	}
	return piece, verb, true
}

// markOf finds the journal mark among paths.
func markOf(paths []string) (piece, verb string, ok bool) {
	for _, p := range paths {
		if piece, verb, ok = parseMark(p); ok {
			return piece, verb, true
		}
	}
	return "", "", false
}

// runFacts are the settings targets and the plugin root a terminal run touched.
func runFacts(paths []string) (targets []string, root string) {
	for _, p := range paths {
		switch filepath.Base(p) {
		case "settings.json":
			targets = append(targets, p)
		case "installed_plugins.json", "known_marketplaces.json":
			root = filepath.Dir(p)
		}
	}
	return targets, root
}

func termOf(l *model.Ledger) *model.LedgerTerminal {
	if l.Terminal == nil {
		l.Terminal = &model.LedgerTerminal{}
	}
	return l.Terminal
}

// tidy drops empty pieces, and the block when nothing is left.
func tidy(l model.Ledger) model.Ledger {
	t := l.Terminal
	if t == nil {
		return l
	}
	if t.Pane != nil && len(t.Pane.Targets) == 0 && len(t.Pane.Installed) == 0 && len(t.Pane.Markets) == 0 {
		t.Pane = nil
	}
	if t.WarpPlugin != nil && len(t.WarpPlugin.Roots) == 0 && len(t.WarpPlugin.Targets) == 0 && len(t.WarpPlugin.Installed) == 0 {
		t.WarpPlugin = nil
	}
	if len(t.TitlePrior) == 0 {
		t.TitlePrior = nil
	}
	if len(t.Tabs) == 0 {
		t.Tabs, t.Warp = nil, nil
	}
	if t.Empty() {
		l.Terminal = nil
	}
	return l
}

// runEffect applies (on = true) or takes back (on = false) the effect of a terminal run.
func runEffect(l *model.Ledger, piece, verb string, targets []string, root string, on bool) {
	if piece == PieceHooks {
		return // a refresh of what is installed: nothing to record or take back
	}
	t := termOf(l)
	// What the verb did: adds targets or roots (install, enable, marketplace-add) or takes
	// them away (uninstall, disable, marketplace-remove). Reverting flips it.
	adds := verb == VerbInstall || verb == VerbEnable || verb == VerbMarketplaceAdd
	if !on {
		adds = !adds
	}
	set := func(xs *[]string, add []string) {
		if len(add) == 0 {
			return
		}
		if adds {
			*xs = addAll(*xs, add)
		} else {
			*xs = removeAll(*xs, add)
		}
	}
	var roots []string
	if root != "" {
		roots = []string{root}
	}
	market := verb == VerbMarketplaceAdd || verb == VerbMarketplaceRemove
	install := verb == VerbInstall || verb == VerbUninstall
	switch piece {
	case PiecePane:
		if t.Pane == nil {
			t.Pane = &model.LedgerTermTargets{}
		}
		switch {
		case market:
			// sy-local added to a root that lacked it, for the pane: `sy term off` and
			// `sy uninstall` remove it there again.
			set(&t.Pane.Markets, roots)
		case install:
			set(&t.Pane.Installed, roots)
			set(&t.Pane.Targets, targets)
		default:
			set(&t.Pane.Targets, targets)
		}
	case PieceWarpPlugin:
		if t.WarpPlugin == nil {
			t.WarpPlugin = &model.LedgerWarpPlugin{Ref: WarpRef}
		}
		w := t.WarpPlugin
		switch {
		case market:
			set(&w.Roots, roots)
		case install:
			set(&w.Installed, roots)
			set(&w.Targets, targets)
		default:
			set(&w.Targets, targets)
		}
	}
}

func setTitles(l *model.Ledger, file string, on bool) {
	t := termOf(l)
	if t.Titles == nil {
		t.Titles = &model.LedgerTermTargets{Targets: []string{}}
	}
	if on {
		t.Titles.Targets = addAll(t.Titles.Targets, []string{file})
	} else {
		t.Titles.Targets = removeAll(t.Titles.Targets, []string{file})
	}
}

func setTab(l *model.Ledger, path, sum string) {
	t := termOf(l)
	var keep []model.LedgerTab
	for _, x := range t.Tabs {
		if x.Path != path {
			keep = append(keep, x)
		}
	}
	if sum != "" {
		keep = append(keep, model.LedgerTab{Path: path, SHA256: sum, Label: warp.LabelOf(path)})
		dir := filepath.Dir(path)
		t.Warp = &model.LedgerWarp{Channel: warp.ChannelOf(dir), Folder: dir}
	}
	t.Tabs = keep
}

// updateTerminal is called by Update for every finished entry, with the plan action it ran
// (any step: StepTerminal runs and writes, the StepPlugin hooks refresh, the StepConfig
// terminal.titles config_set). It returns l with the entry's effect on l.Terminal; the
// generic code after it still runs, so it must not record what Update records already.
func updateTerminal(l model.Ledger, a model.Action, e model.Entry) model.Ledger {
	_ = e
	switch a.Op {
	case model.OpRun:
		piece, verb, ok := markOf(a.Touches)
		if !ok {
			return l
		}
		targets, root := runFacts(a.Touches)
		if root == "" && len(a.Watch) > 0 {
			root = a.Watch[0]
		}
		runEffect(&l, piece, verb, targets, root, true)
	case model.OpJSONSet, model.OpJSONDelete:
		if a.Pointer != TitleEnvPointer {
			return l
		}
		// "1" is sy's value; any other json_set is `titles off` putting the person's
		// earlier value back (TitlePrior).
		on := a.Op == model.OpJSONSet && compact(a.Value) == titleOn
		setTitles(&l, a.File, on)
		if on {
			t := termOf(&l)
			if len(a.Before) > 0 {
				if t.TitlePrior == nil {
					t.TitlePrior = map[string]string{}
				}
				t.TitlePrior[a.File] = compact(a.Before)
			} else {
				delete(t.TitlePrior, a.File)
			}
		}
	case model.OpConfigSet:
		if a.Key != TitlesConfigKey {
			return l
		}
		var on bool
		if json.Unmarshal(a.Value, &on) != nil {
			return l
		}
		if on {
			if t := termOf(&l); t.Titles == nil {
				t.Titles = &model.LedgerTermTargets{Targets: []string{}}
			}
		} else if l.Terminal != nil {
			l.Terminal.Titles = nil
		}
	case model.OpWriteFile:
		if !warp.IsTabPath(a.Path) {
			return l
		}
		setTab(&l, a.Path, a.ContentSHA256)
	case model.OpMoveToBackup:
		if !warp.IsTabPath(a.Path) {
			return l
		}
		setTab(&l, a.Path, "")
	default:
		return l
	}
	return tidy(l)
}

// revertTerminal is called by Revert for every reverted entry, before the generic code. It
// returns l with the entry's effect undone and true when the entry was a terminal one; the
// generic code is then skipped for it, because a `claude plugin install
// switchyard-pane@sy-local` run creates paths under sy-local and touches
// installed_plugins.json, which the generic code reads as the switchyard plugin going away.
// An undo has no plan, so it decides from the manifest entry alone (op, paths, keys,
// created paths).
func revertTerminal(l model.Ledger, e model.Entry) (model.Ledger, bool) {
	var paths []string
	for _, pe := range e.Paths {
		paths = append(paths, pe.Path)
	}
	switch e.Op {
	case model.OpRun:
		piece, verb, ok := markOf(paths)
		if !ok {
			return l, false
		}
		targets, root := runFacts(paths)
		runEffect(&l, piece, verb, targets, root, false)
		return tidy(l), true
	case model.OpJSONSet, model.OpJSONDelete:
		for _, pe := range e.Paths {
			for _, k := range pe.Keys {
				if k.Pointer == TitleEnvPointer {
					// Reverting `titles on` (a set to "1") takes the target away, and the
					// person's value is back in the file; reverting `titles off` (a delete,
					// or a set back to the earlier value) gives the target back.
					on := e.Op == model.OpJSONSet && (k.AfterSHA256 == "" || k.AfterSHA256 == titleOnSHA)
					setTitles(&l, pe.Path, !on)
					t := l.Terminal
					if on {
						delete(t.TitlePrior, pe.Path)
					}
					if t.Titles != nil && len(t.Titles.Targets) == 0 {
						t.Titles = nil
					}
					return tidy(l), true
				}
			}
		}
	case model.OpWriteFile, model.OpMoveToBackup:
		for _, pe := range e.Paths {
			if !warp.IsTabPath(pe.Path) {
				continue
			}
			sum := ""
			if pe.Before.Type == model.TypeFile {
				// A rewritten older tab config of ours, or a removed one, comes back.
				sum = pe.Before.SHA256
			}
			setTab(&l, pe.Path, sum)
			return tidy(l), true
		}
	case model.OpMkdir:
		for _, pe := range e.Paths {
			if filepath.Base(pe.Path) == "tab_configs" {
				return l, true
			}
		}
	case model.OpWriteTree:
		// A refresh of a marketplace that existed before (the hooks refresh of
		// `sy term titles on`, the pane's refresh): its undo puts the older tree back, and
		// the marketplace stays recorded.
		for _, pe := range e.Paths {
			if pe.After != nil && pe.Before.Type == model.TypeDir && pe.Path != "" && pe.Intended != nil {
				return l, true
			}
		}
	}
	return l, false
}

// titleOn is the value `titles on` sets (compact JSON), and titleOnSHA its hash as a
// manifest key records it (backup: SHA-256 of the compact value).
const titleOn = `"1"`

var titleOnSHA = func() string { s := sha256.Sum256([]byte(titleOn)); return hex.EncodeToString(s[:]) }()

// compact is raw JSON without insignificant space ("" for invalid JSON).
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return ""
	}
	return b.String()
}

func removeAll(xs, rm []string) []string {
	out := xs
	for _, x := range rm {
		out = remove(out, x)
	}
	if out == nil && xs != nil {
		out = []string{}
	}
	return out
}

// Terminal piece names of TermPiece (the journal mark pieces, and these two).
const (
	PieceTitles = "titles"
	PieceTabs   = "tabs"
)

// TermPiece names the terminal piece an action of a terminal plan belongs to: PiecePane or
// PieceWarpPlugin for a marked `claude plugin` run, PieceTitles for the env key and the
// terminal.titles config key, PieceTabs for a tab config; "" for anything else (the
// switchyard plugin's hooks refresh included). `sy uninstall` keeps the ledger record of a
// piece whose actions did not all run.
func TermPiece(a model.Action) string {
	switch a.Op {
	case model.OpRun:
		if piece, _, ok := markOf(a.Touches); ok && piece != PieceHooks {
			return piece
		}
	case model.OpJSONSet, model.OpJSONDelete:
		if a.Pointer == TitleEnvPointer {
			return PieceTitles
		}
	case model.OpConfigSet:
		if a.Key == TitlesConfigKey {
			return PieceTitles
		}
	case model.OpWriteFile, model.OpMoveToBackup:
		if warp.IsTabPath(a.Path) {
			return PieceTabs
		}
	}
	return ""
}
