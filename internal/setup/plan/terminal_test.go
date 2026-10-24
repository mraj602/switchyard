package plan_test

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
	"github.com/mraj602/watchyard/internal/warp"
)

var updateGoldens = flag.Bool("update", false, "rewrite testdata/terminal goldens")

// termState is a fixed laptop at /home/u: ~/.claude (default, primary, owns the plugin
// root), ~/.claude-2 sharing its settings (a link) and plugin root, and ~/.claude-3 with its
// own settings and root. claude-2 and claude-3 are launcher files wy wrote.
func termState() model.State {
	h := "/home/u"
	return model.State{
		Home: h, OS: "linux", Sy: h + "/.local/bin/wy",
		StateDir: h + "/.local/state/watchyard", ConfigDir: h + "/.config/watchyard", ConfigPresent: true,
		Claude: model.ClaudeInfo{Path: "~/.local/bin/claude", Version: "2.1.296"},
		Folders: []model.Folder{
			{Folder: "~/.claude", Dir: h + "/.claude", Default: true, Primary: true, Label: "me", Colour: "#14b8a6",
				SettingsTarget: h + "/.claude/settings.json", PluginRoot: h + "/.claude/plugins", OwnsRoot: true},
			{Folder: "~/.claude-2", Dir: h + "/.claude-2", Label: "work", Colour: "#f59e0b",
				SettingsTarget: h + "/.claude/settings.json", PluginRoot: h + "/.claude/plugins"},
			{Folder: "~/.claude-3", Dir: h + "/.claude-3",
				SettingsTarget: h + "/.claude-3/settings.json", PluginRoot: h + "/.claude-3/plugins", OwnsRoot: true},
		},
		SettingsTargets: []model.SettingsTarget{
			{Path: h + "/.claude/settings.json", Folders: []string{"~/.claude", "~/.claude-2"}, Exists: true,
				EnabledPlugins: map[string]bool{"watchyard@wy-local": true}},
			{Path: h + "/.claude-3/settings.json", Folders: []string{"~/.claude-3"}, Exists: true,
				EnabledPlugins: map[string]bool{"watchyard@wy-local": true}},
		},
		PluginRoots: []model.PluginRoot{
			{Root: h + "/.claude/plugins", Owner: "~/.claude", Folders: []string{"~/.claude", "~/.claude-2"},
				Plugins: []string{"watchyard@wy-local"}, Markets: []string{"wy-local"}},
			{Root: h + "/.claude-3/plugins", Owner: "~/.claude-3", Folders: []string{"~/.claude-3"},
				Plugins: []string{"watchyard@wy-local"}, Markets: []string{"wy-local"}},
		},
		Launchers: []model.Launcher{
			{Name: "claude-2", Kind: model.LauncherFile, Path: "~/.local/bin/claude-2", Ours: true, Folder: "~/.claude-2"},
			{Name: "claude-3", Kind: model.LauncherFile, Path: "~/.local/bin/claude-3", Ours: true, Folder: "~/.claude-3"},
		},
		Ledger: &model.Ledger{V: 1},
	}
}

func termFacts() plan.TermFacts {
	folder := "/home/u/.local/share/warp-terminal/tab_configs"
	return plan.TermFacts{
		TitleEnv: map[string]string{}, MarketExists: true, MarketCurrent: true, JQ: true,
		Warp: warp.Install{Channel: warp.Stable, Data: filepath.Dir(folder), Folder: folder, Present: true},
	}
}

func termPlan(t *testing.T, st model.State, c model.TermChoices, f plan.TermFacts) model.Plan {
	t.Helper()
	p, err := plan.TerminalWith(st, c, f, opt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func termGolden(t *testing.T, name string, p model.Plan) {
	t.Helper()
	got, _ := json.MarshalIndent(p, "", "  ")
	got = append(got, '\n')
	// The fake home is POSIX-shaped; plan joins with the host separator, JSON-escaped here.
	got = []byte(strings.ReplaceAll(string(got), `\\`, "/"))
	file := filepath.Join("testdata", "terminal", name+".json")
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (go test -run TestTerminalGoldens -update)", err)
	}
	if runtime.GOOS == "windows" {
		// plan_id hashes the host-separator paths, so it differs from the POSIX golden; the
		// rest of the plan is compared.
		id := regexp.MustCompile(`"plan_id": "p_[0-9a-f]+"`)
		got = id.ReplaceAll(got, id.Find(want))
	}
	if string(got) != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("%s differs from golden:\n%s", name, got)
	}
}

// onLedger is the ledger after every piece was turned on in termState.
func onLedger(st model.State, f plan.TermFacts) *model.LedgerTerminal {
	specs, _ := plan.TabSpecs(st, f)
	targets := []string{"/home/u/.claude-3/settings.json", "/home/u/.claude/settings.json"}
	roots := []string{"/home/u/.claude-3/plugins", "/home/u/.claude/plugins"}
	lt := &model.LedgerTerminal{
		Pane:       &model.LedgerTermTargets{Targets: targets, Installed: roots},
		WarpPlugin: &model.LedgerWarpPlugin{Roots: roots, Targets: targets, Installed: roots, Ref: "main", Commit: "abc"},
		Titles:     &model.LedgerTermTargets{Targets: []string{"/home/u/.claude-3/settings.json", "/home/u/.claude/settings.json"}},
		Warp:       &model.LedgerWarp{Channel: warp.Stable, Folder: f.Warp.Folder},
	}
	for _, s := range specs {
		lt.Tabs = append(lt.Tabs, model.LedgerTab{Path: s.Path, SHA256: warp.Hash(warp.Render(s.Spec)), Label: s.Spec.Label})
	}
	return lt
}

// onState is termState with every piece applied (plugins installed and enabled, titles set,
// tab configs written), and the facts that go with it.
func onState() (model.State, plan.TermFacts) {
	st, f := termState(), termFacts()
	for i := range st.SettingsTargets {
		st.SettingsTargets[i].EnabledPlugins[plan.PaneID] = true
		st.SettingsTargets[i].EnabledPlugins[plan.WarpPluginID] = true
		f.TitleEnv[st.SettingsTargets[i].Path] = `"1"`
	}
	for i := range st.PluginRoots {
		r := &st.PluginRoots[i]
		r.Plugins = append(r.Plugins, plan.PaneID, plan.WarpPluginID)
		r.Markets = append(r.Markets, plan.WarpMarketplace)
	}
	f.TitlesOn = true
	f.Warp.FolderExists = true
	st.Ledger.Terminal = onLedger(st, f)
	for _, lt := range st.Ledger.Terminal.Tabs {
		f.Tabs = append(f.Tabs, warp.Found{Path: lt.Path, Label: lt.Label, SHA256: lt.SHA256, Marker: true})
	}
	return st, f
}

// Each piece, on and off, is a golden; plan_id is the same on every run.
func TestTerminalGoldens(t *testing.T) {
	cases := map[string]model.TermChoices{
		"pane":        {Pane: true},
		"warp-plugin": {WarpPlugin: true},
		"titles":      {Titles: true},
		"tabs":        {WarpTabs: true},
		"all":         {Pane: true, WarpPlugin: true, Titles: true, WarpTabs: true},
	}
	for name, c := range cases {
		st, f := termState(), termFacts()
		p := termPlan(t, st, c, f)
		termGolden(t, name+"-on", p)
		if again := termPlan(t, termState(), c, termFacts()); again.PlanID != p.PlanID || !strings.HasPrefix(p.PlanID, "p_") {
			t.Errorf("%s: plan_id %s then %s", name, p.PlanID, again.PlanID)
		}
		st, f = onState()
		c.Off = true
		termGolden(t, name+"-off", termPlan(t, st, c, f))
	}
}

// A second `on` after everything is on plans nothing.
func TestTerminalOnAgainIsEmpty(t *testing.T) {
	st, f := onState()
	p := termPlan(t, st, model.TermChoices{Pane: true, WarpPlugin: true, Titles: true, WarpTabs: true}, f)
	if len(p.Actions) != 0 || p.Summary.Changes != 0 {
		t.Fatalf("second on:\n%s", dump(p))
	}
}

func TestTerminalShape(t *testing.T) {
	st, f := termState(), termFacts()
	p := termPlan(t, st, model.TermChoices{Pane: true, WarpPlugin: true, Titles: true, WarpTabs: true}, f)
	var steps []string
	for _, a := range p.Actions {
		steps = append(steps, string(a.Step))
		if a.Op == model.OpRun {
			mark := filepath.ToSlash(a.Touches[len(a.Touches)-1])
			if !strings.HasPrefix(mark, "/home/u/.local/state/watchyard/term/journal/") {
				t.Errorf("run without a journal mark: %+v", a)
			}
			if a.Argv[0] != "claude" {
				t.Errorf("argv %v", a.Argv)
			}
		}
	}
	// pane before Warp's plugin before titles before tabs; config last.
	if steps[len(steps)-1] != string(model.StepConfig) {
		t.Errorf("steps %v", steps)
	}
	var tabs []model.Action
	for _, a := range p.Actions {
		if a.Op == model.OpWriteFile {
			tabs = append(tabs, a)
		}
	}
	if len(tabs) != 3 || !strings.HasSuffix(tabs[0].Path, "watchyard-me.toml") || !strings.HasSuffix(tabs[1].Path, "watchyard-work.toml") ||
		!strings.HasSuffix(tabs[2].Path, "watchyard-claude-3.toml") {
		t.Fatalf("tabs %+v", tabs)
	}
	if !strings.Contains(tabs[0].Note, "cyan") || !strings.Contains(tabs[1].Note, "yellow") {
		t.Errorf("colours: %s / %s", tabs[0].Note, tabs[1].Note)
	}
	content := plan.TabContents(st, f)
	for _, a := range tabs {
		b, err := content(a)
		if err != nil || warp.Hash(b) != a.ContentSHA256 {
			t.Fatalf("content of %s: %v", a.Path, err)
		}
	}
	joined := strings.Join(p.Warnings, "\n")
	if !strings.Contains(joined, plan.TitleSideEffects) || !strings.Contains(joined, "branch main") {
		t.Errorf("warnings %v", p.Warnings)
	}
}

func TestTerminalRefusals(t *testing.T) {
	refuse := func(name string, st model.State, c model.TermChoices, f plan.TermFacts) {
		t.Helper()
		if _, err := plan.TerminalWith(st, c, f, opt); !errors.Is(err, plan.ErrTermRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	old := termState()
	old.Claude.Version = "2.1.200"
	refuse("old claude", old, model.TermChoices{Pane: true}, termFacts())
	none := termState()
	none.Claude.Path = ""
	refuse("no claude", none, model.TermChoices{Pane: true}, termFacts())
	refuse("no claude warp", none, model.TermChoices{WarpPlugin: true}, termFacts())
	nomkt := termFacts()
	nomkt.MarketExists = false
	refuse("no marketplace", termState(), model.TermChoices{Titles: true}, nomkt)
	nowarp := termFacts()
	nowarp.Warp = warp.Install{Channel: warp.Stable, Folder: nowarp.Warp.Folder}
	refuse("no warp", termState(), model.TermChoices{WarpTabs: true}, nowarp)
	if _, err := plan.TerminalWith(termState(), model.TermChoices{WarpTabs: true, Channel: "beta"}, termFacts(), opt); err == nil {
		t.Error("bad channel accepted")
	}
	// A stale marketplace is refreshed (write_tree in StepPlugin) before titles.
	stale := termFacts()
	stale.MarketCurrent = false
	p := termPlan(t, termState(), model.TermChoices{Titles: true}, stale)
	if p.Actions[0].Op != model.OpWriteTree || p.Actions[0].Step != model.StepPlugin {
		t.Errorf("no refresh: %s", dump(p))
	}
	// ... and every root with the watchyard plugin updates it, so the title hooks run.
	var updates []string
	for _, a := range p.Actions {
		if a.Op == model.OpRun && a.Step == model.StepTerminal && strings.Contains(strings.Join(a.Argv, " "), "update") {
			updates = append(updates, strings.Join(a.Argv[2:], " ")+"@"+a.Watch[0])
		}
	}
	wantUp := []string{"marketplace update wy-local --json@/home/u/.claude/plugins", "update watchyard@wy-local --json@/home/u/.claude/plugins",
		"marketplace update wy-local --json@/home/u/.claude-3/plugins", "update watchyard@wy-local --json@/home/u/.claude-3/plugins"}
	if strings.Join(updates, "\n") != strings.Join(wantUp, "\n") {
		t.Errorf("updates:\n%s\nwant\n%s", strings.Join(updates, "\n"), strings.Join(wantUp, "\n"))
	}
	if p := termPlan(t, termState(), model.TermChoices{Titles: true}, termFacts()); strings.Contains(dump(p), "update") {
		t.Errorf("current marketplace still updates: %s", dump(p))
	}
	// No jq: a warning, not a refusal.
	nojq := termFacts()
	nojq.JQ = false
	p = termPlan(t, termState(), model.TermChoices{WarpPlugin: true}, nojq)
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "jq") {
		t.Errorf("no jq warning: %v", p.Warnings)
	}
}

// A tab config the person edited survives `off` and is reported; one without the marker is
// never touched by `on`.
func TestTerminalTabsOwnership(t *testing.T) {
	st, f := onState()
	f.Tabs[1].SHA256 = "edited"
	p := termPlan(t, st, model.TermChoices{WarpTabs: true, Off: true}, f)
	if len(p.Actions) != 2 {
		t.Fatalf("off: %s", dump(p))
	}
	for _, a := range p.Actions {
		if a.Op != model.OpMoveToBackup || strings.HasSuffix(a.Path, "watchyard-work.toml") {
			t.Fatalf("off touched the edited file: %+v", a)
		}
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "watchyard-work.toml was edited") || !hasKeep(p, "yours now") {
		t.Errorf("edited file not reported: %v %v", p.Warnings, p.Keep)
	}

	st, f = termState(), termFacts()
	f.Warp.FolderExists = true
	f.Tabs = []warp.Found{{Path: f.Warp.Folder + "/watchyard-work.toml", Label: "work", SHA256: "theirs"}}
	p = termPlan(t, st, model.TermChoices{WarpTabs: true}, f)
	for _, a := range p.Actions {
		if strings.HasSuffix(a.Path, "watchyard-work.toml") || a.Op == model.OpMkdir {
			t.Fatalf("touched: %+v", a)
		}
	}
	if len(p.Actions) != 2 || !strings.Contains(strings.Join(p.Warnings, "\n"), "is not wy's") {
		t.Errorf("no-marker: %s", dump(p))
	}
	// A marker file whose hash matches neither this wy nor the ledger is the person's too.
	f.Tabs[0].Marker = true
	p = termPlan(t, st, model.TermChoices{WarpTabs: true}, f)
	if len(p.Actions) != 2 || !hasKeep(p, "yours now") {
		t.Errorf("edited with marker: %s", dump(p))
	}
	// One wy wrote (ledger hash) with an older body is rewritten in place.
	st.Ledger.Terminal = &model.LedgerTerminal{Tabs: []model.LedgerTab{{Path: f.Tabs[0].Path, SHA256: "theirs", Label: "work"}}}
	p = termPlan(t, st, model.TermChoices{WarpTabs: true}, f)
	if len(p.Actions) != 3 || p.Actions[1].Pre[0].State.SHA256 != "theirs" {
		t.Errorf("rewrite: %s", dump(p))
	}
}

func hasKeep(p model.Plan, note string) bool {
	for _, k := range p.Keep {
		if k.Note == note {
			return true
		}
	}
	return false
}

// Titles off leaves a value the person changed; nothing is planned off for a piece the
// ledger does not record.
func TestTerminalOffOnlyOurs(t *testing.T) {
	st, f := onState()
	f.TitleEnv["/home/u/.claude-3/settings.json"] = `"0"`
	p := termPlan(t, st, model.TermChoices{Titles: true, Off: true}, f)
	if len(p.Actions) != 2 || p.Actions[0].File != "/home/u/.claude/settings.json" || p.Actions[1].Op != model.OpConfigSet {
		t.Fatalf("titles off: %s", dump(p))
	}
	st, f = onState()
	st.Ledger.Terminal = nil
	p = termPlan(t, st, model.TermChoices{Pane: true, WarpPlugin: true, Titles: true, WarpTabs: true, Off: true}, f)
	for _, a := range p.Actions {
		if a.Op != model.OpConfigSet {
			t.Fatalf("off without a ledger: %s", dump(p))
		}
	}
}

// The Windows and macOS layouts: tab configs go to Warp's folder for that OS with the .cmd
// launcher on Windows.
func TestTerminalTabLayouts(t *testing.T) {
	st, f := termState(), termFacts()
	st.OS = "windows"
	st.Launchers[0].Path, st.Launchers[1].Path = "~/.local/bin/claude-2.cmd", "~/.local/bin/claude-3.cmd"
	st.Claude.Path = "~/.local/bin/claude.exe"
	f.Warp.Folder = `C:\Users\u\AppData\Roaming\warp\Warp\data\tab_configs`
	f.Warp.Data = `C:\Users\u\AppData\Roaming\warp\Warp\data`
	specs, warns := plan.TabSpecs(st, f)
	if len(specs) != 3 || len(warns) != 0 || specs[1].Path != f.Warp.Folder+`\watchyard-work.toml` ||
		!strings.HasSuffix(specs[1].Spec.Command, "claude-2.cmd") {
		t.Fatalf("windows specs %+v %v", specs, warns)
	}
	st, f = termState(), termFacts()
	st.OS = "darwin"
	f.Warp.Folder = "/home/u/.warp/tab_configs"
	p := termPlan(t, st, model.TermChoices{WarpTabs: true}, f)
	if p.Actions[0].Op != model.OpMkdir || p.Actions[0].Path != f.Warp.Folder || !strings.HasPrefix(p.Actions[1].Path, f.Warp.Folder+"/") {
		t.Fatalf("darwin: %s", dump(p))
	}
	// An account with no launcher file gets no tab config and a warning.
	st = termState()
	st.Launchers = st.Launchers[:1]
	specs, warns = plan.TabSpecs(st, termFacts())
	if len(specs) != 2 || len(warns) != 1 || !strings.Contains(warns[0], "claude-3") {
		t.Fatalf("missing launcher: %+v %v", specs, warns)
	}
}

func TestVersionLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{{"2.1.200", "2.1.296", true}, {"2.1.296 (Claude Code)", "2.1.296", false}, {"2.2.0", "2.1.296", false}, {"", "2.1.296", true}, {"v3", "2.1.296", false}} {
		if plan.VersionLess(c.a, c.b) != c.less {
			t.Errorf("VersionLess(%q, %q) != %v", c.a, c.b, c.less)
		}
	}
}
