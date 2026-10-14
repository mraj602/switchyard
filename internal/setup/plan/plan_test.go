package plan_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/plan"
)

var opt = plan.Options{Version: "0.3.0"}

func mustPlan(t *testing.T, st model.State, c model.Choices) model.Plan {
	t.Helper()
	p, err := plan.PlanWith(st, c, opt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ops(p model.Plan) []string {
	var out []string
	for _, a := range p.Actions {
		s := string(a.Op)
		if a.Op == model.OpRun && len(a.Argv) > 2 {
			s += ":" + a.Argv[2]
			if a.Argv[2] == "marketplace" {
				s += " " + a.Argv[3]
			}
		}
		out = append(out, s)
	}
	return out
}

func dump(p model.Plan) string {
	b, _ := json.MarshalIndent(p, "", " ")
	return string(b)
}

// (1) This laptop with defaults gives exactly the 5 actions of setup.md 11, links_changed 0.
func TestLaptopDefaults(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	p := mustPlan(t, st, model.Choices{})
	want := []string{"write_tree", "run:marketplace add", "run:install", "json_set", "config_set"}
	got := ops(p)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions %v\n%s", got, dump(p))
	}
	if p.Summary != (model.Summary{Changes: 5, BackedUp: 3, LinksChanged: 0, LoginsTouched: 0}) {
		t.Errorf("summary %+v", p.Summary)
	}
	a := p.Actions
	if a[0].Path != h.Abs(".local/state/switchyard/plugin/marketplace") || a[0].Note != "switchyard 0.3.0 (sy-local)" {
		t.Errorf("tree: %+v", a[0])
	}
	if a[1].Env["CLAUDE_CONFIG_DIR"] != nil || len(a[1].Touches) != 2 {
		t.Errorf("add: %+v", a[1])
	}
	if len(a[2].Covers) != 7 || a[2].Note != "shared settings and plugins" {
		t.Errorf("install: %+v", a[2])
	}
	sl := a[3]
	if sl.Pointer != "/statusLine/command" || sl.File != h.Abs(".claude/settings.json") || len(sl.Covers) != 7 || len(sl.Via) != 6 {
		t.Errorf("statusline: %+v", sl)
	}
	var now string
	_ = json.Unmarshal(sl.Value, &now)
	sy := h.Abs(".local/bin/sy")
	if strings.Contains(sy, `\`) { // a Windows path with backslashes is single-quoted for the shell
		sy = "'" + sy + "'"
	}
	wantNow := fmt.Sprintf("%s statusline --wrap 'bash %s'", sy, h.Abs(".claude/statusline-command.sh"))
	if now != wantNow {
		t.Errorf("statusline value %q", now)
	}
	var cfg map[string]any
	_ = json.Unmarshal(a[4].Value, &cfg)
	if cfg["share.mcp"] != "external" || cfg["share.primary"] != "~/.claude" || cfg["slots.labels.~/.claude-7"] != "claude-7" || len(cfg) != 2+7+7 {
		t.Errorf("config: %v", cfg)
	}
	if len(p.Needs) != 4 || p.Needs[0].Folder != "~/.claude-4" || p.Needs[0].Command != "CLAUDE_CONFIG_DIR=~/.claude-4 claude auth login --claudeai" {
		t.Errorf("needs: %+v", p.Needs)
	}
	links := 0
	for _, k := range p.Keep {
		if k.What == "link" {
			links++
		}
	}
	if links != 48 || len(p.Conflicts) != 0 {
		t.Errorf("keep links %d, conflicts %d", links, len(p.Conflicts))
	}
	for _, a := range p.Actions {
		if len(a.Pre) == 0 && a.Op != model.OpRun {
			t.Errorf("action %s has no precondition", a.ID)
		}
	}
	if !strings.HasPrefix(p.PlanID, "p_") || len(p.PlanID) != 14 {
		t.Errorf("plan id %q", p.PlanID)
	}
}

// afterApply edits a laptop state the way a successful apply of the default plan would.
func afterApply(st model.State, p model.Plan) model.State {
	t := &st.SettingsTargets[0]
	t.EnabledPlugins["switchyard@sy-local"] = true
	t.SwitchyardPlugins = []string{"switchyard@sy-local"}
	t.ExtraMarketplaces = append(t.ExtraMarketplaces, "sy-local")
	for _, a := range p.Actions {
		if a.Op == model.OpJSONSet && a.Pointer == "/statusLine/command" {
			var v string
			_ = json.Unmarshal(a.Value, &v)
			t.StatusLine = &model.StatusLine{Type: "command", Command: v, Ours: true}
		}
	}
	r := &st.PluginRoots[0]
	r.Plugins = append(r.Plugins, "switchyard@sy-local")
	r.Markets = append(r.Markets, "sy-local")
	st.Share = model.ShareConfig{Primary: "~/.claude", MCP: "external"}
	st.ConfigPresent = true
	for i := range st.Folders {
		st.Folders[i].Label = strings.TrimPrefix(strings.TrimPrefix(st.Folders[i].Folder, "~/."), "")
	}
	for i := range st.Folders {
		f := &st.Folders[i]
		f.Label = strings.TrimPrefix(f.Folder, "~/.")
		f.Colour = model.Palette[i].Hex
	}
	return st
}

// (2) The same state after apply gives an empty plan.
func TestLaptopRerunIsEmpty(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	p := mustPlan(t, st, model.Choices{})
	st2 := afterApply(st, p)
	p2 := mustPlan(t, st2, model.Choices{})
	if !p2.Empty() || p2.Summary.Changes != 0 {
		t.Fatalf("second plan not empty:\n%s", dump(p2))
	}
	if len(p2.Needs) != 4 {
		t.Errorf("needs still listed: %+v", p2.Needs)
	}
}

// (3) Fresh home + --add work,client.
func TestFreshAdd(t *testing.T) {
	h := detecttest.Fresh(t)
	st := h.State()
	c := model.Choices{Add: []model.LabelColour{{Label: "work", Colour: "amber"}, {Label: "client"}}}
	p := mustPlan(t, st, c)
	var mk, links []string
	for _, a := range p.Actions {
		switch a.Op {
		case model.OpMkdir:
			mk = append(mk, a.Path)
			if a.Mode != "0700" {
				t.Errorf("mkdir mode %s", a.Mode)
			}
		case model.OpLink:
			links = append(links, filepath.ToSlash(strings.TrimPrefix(a.Path, h.Root+string(filepath.Separator))))
		}
	}
	if strings.Join(mk, ",") != h.Abs(".claude-2")+","+h.Abs(".claude-3") {
		t.Errorf("mkdir: %v", mk)
	}
	// Primary has settings.json and CLAUDE.md only.
	want := ".claude-2/settings.json,.claude-3/settings.json,.claude-2/CLAUDE.md,.claude-3/CLAUDE.md"
	if strings.Join(links, ",") != want {
		t.Errorf("links: %v", links)
	}
	// env key set once (shared settings), launchers for both, config, two logins needed.
	envSets, launchers := 0, 0
	for _, a := range p.Actions {
		if a.Op == model.OpJSONSet && a.Pointer == "/env" {
			envSets++
			if len(a.Covers) != 3 || !strings.Contains(string(a.Value), h.Abs(".claude/plugins")) {
				t.Errorf("env: %+v", a)
			}
		}
		if a.Op == model.OpWriteFile && strings.Contains(filepath.ToSlash(a.Path), ".local/bin/claude-") {
			launchers++
		}
	}
	if envSets != 1 || launchers != 2 {
		t.Errorf("env sets %d launchers %d\n%s", envSets, launchers, dump(p))
	}
	if len(p.Needs) != 2 || p.Needs[0].Folder != "~/.claude-2" {
		t.Errorf("needs %+v", p.Needs)
	}
	if ops(p)[0] != "mkdir" || !strings.HasPrefix(ops(p)[len(ops(p))-1], "config_set") {
		t.Errorf("order: %v", ops(p))
	}
	var cfg map[string]any
	for _, a := range p.Actions {
		if a.Op == model.OpConfigSet {
			_ = json.Unmarshal(a.Value, &cfg)
		}
	}
	if cfg["slots.colours.~/.claude-2"] != "#f59e0b" || cfg["slots.labels.~/.claude-3"] != "client" || cfg["share.mcp"] != true {
		t.Errorf("cfg %v", cfg)
	}
	// Interactive: logins for the new folders only.
	c.Interactive = true
	p = mustPlan(t, st, c)
	n := 0
	for _, a := range p.Actions {
		if a.Op == model.OpLogin {
			n++
		}
	}
	if n != 2 || p.Summary.Changes != len(p.Actions)-2 {
		t.Errorf("login actions %d, summary %+v", n, p.Summary)
	}
}

// (5) Settings on forces Plugins on.
func TestSettingsForcesPlugins(t *testing.T) {
	h := detecttest.Fresh(t)
	st := h.State()
	c := model.Choices{Share: []string{"settings"}, Add: []model.LabelColour{{Label: "work"}}}
	p := mustPlan(t, st, c)
	found := false
	for _, a := range p.Actions {
		if a.Op == model.OpJSONSet && a.Pointer == "/env" {
			found = true
		}
	}
	if !found {
		t.Errorf("no plugin root env key:\n%s", dump(p))
	}
	// Without settings, plugins named alone gives the key in the folder's own settings: new
	// folder has none, so the plugin root is linked instead (no existing links: nothing).
	c.Share = []string{"instructions"}
	p = mustPlan(t, st, c)
	for _, a := range p.Actions {
		if a.Op == model.OpJSONSet && strings.HasPrefix(a.Pointer, "/env") {
			t.Errorf("unexpected json_set %+v", a)
		}
	}
}

// (6) A second enabled switchyard@* gives the disable action.
func TestSecondSwitchyardPlugin(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	st.SettingsTargets[0].EnabledPlugins["switchyard@sy-local"] = true
	st.SettingsTargets[0].EnabledPlugins["switchyard@github"] = true
	st.SettingsTargets[0].SwitchyardPlugins = []string{"switchyard@github", "switchyard@sy-local"}
	st.PluginRoots[0].Plugins = append(st.PluginRoots[0].Plugins, "switchyard@sy-local")
	st.PluginRoots[0].Markets = append(st.PluginRoots[0].Markets, "sy-local")
	p := mustPlan(t, st, model.Choices{NoStatusLine: true, Share: []string{"defaults"}})
	var dis []model.Action
	for _, a := range p.Actions {
		if a.Op == model.OpRun {
			dis = append(dis, a)
		}
	}
	if len(dis) != 1 || strings.Join(dis[0].Argv, " ") != "claude plugin disable switchyard@github --json" {
		t.Fatalf("runs: %+v\n%s", dis, dump(p))
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "switchyard@github") {
		t.Errorf("warnings: %v", p.Warnings)
	}
	// An adopted plugin from another marketplace: no sy-local install.
	st.SettingsTargets[0].SwitchyardPlugins = []string{"switchyard@github"}
	st.PluginRoots[0].Plugins = st.PluginRoots[0].Plugins[:3]
	p = mustPlan(t, st, model.Choices{NoStatusLine: true})
	for _, a := range p.Actions {
		if a.Op == model.OpRun || a.Op == model.OpWriteTree {
			t.Errorf("adopted plugin still installed: %+v", a)
		}
	}
}

// (7) plan_id is stable and changes with one precondition.
func TestPlanID(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	id := mustPlan(t, st, model.Choices{}).PlanID
	for i := 0; i < 5; i++ {
		if got := mustPlan(t, h.State(), model.Choices{}).PlanID; got != id {
			t.Fatalf("unstable: %s vs %s", got, id)
		}
	}
	// A different map ordering of the same data (labels given in another order).
	c1 := model.Choices{Labels: map[string]model.LabelColour{"~/.claude-2": {Label: "a"}, "~/.claude-3": {Label: "b"}, "~/.claude-4": {Label: "c"}}}
	id1 := mustPlan(t, st, c1).PlanID
	for i := 0; i < 20; i++ {
		c2 := model.Choices{Labels: map[string]model.LabelColour{"~/.claude-4": {Label: "c"}, "~/.claude-3": {Label: "b"}, "~/.claude-2": {Label: "a"}}}
		if mustPlan(t, st, c2).PlanID != id1 {
			t.Fatal("map order changes the id")
		}
	}
	if id1 == id {
		t.Error("different choices, same id")
	}
	// One precondition changes: the marketplace tree already exists.
	st2 := h.State()
	st2.Ledger = &model.Ledger{V: 1, Plugin: &model.LedgerPlugin{ID: "switchyard@sy-local", Marketplace: "/x"}}
	if mustPlan(t, st2, model.Choices{}).PlanID == id {
		t.Error("changed precondition, same id")
	}
	// And a sha of a replaced file.
	hf := detecttest.Fresh(t)
	hf.B.File(".claude-2/.claude.json", `{}`, 0o600)
	hf.B.File(".claude-2/CLAUDE.md", "# me\n", 0o644)
	s3 := hf.State()
	a := mustPlan(t, s3, model.Choices{}).PlanID
	f := &s3.Folders[1]
	it := f.Items["CLAUDE.md"]
	it.SHA256 = "00"
	f.Items["CLAUDE.md"] = it
	if mustPlan(t, s3, model.Choices{}).PlanID == a {
		t.Error("changed file hash, same id")
	}
}

func TestErrors(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	for name, c := range map[string]model.Choices{
		"bad label":     {Labels: map[string]model.LabelColour{"~/.claude-2": {Label: "Bad Label"}}},
		"dup label":     {Labels: map[string]model.LabelColour{"~/.claude-2": {Label: "x"}, "~/.claude-3": {Label: "x"}}},
		"unknown key":   {Labels: map[string]model.LabelColour{"~/.nope": {Label: "x"}}},
		"bad colour":    {Add: []model.LabelColour{{Label: "w", Colour: "nope"}}},
		"proto no hub":  {Prototype: "remove"},
		"other primary": {Primary: "~/.claude-2"},
	} {
		if _, err := plan.PlanWith(st, c, opt); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestNextFreeFolder(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	st.HomeNames = append(st.HomeNames, ".claude-8.bak")
	p := mustPlan(t, st, model.Choices{Add: []model.LabelColour{{Label: "n1"}, {Label: "n2"}}})
	var mk []string
	for _, a := range p.Actions {
		if a.Op == model.OpMkdir {
			mk = append(mk, filepath.ToSlash(strings.TrimPrefix(a.Path, h.Root+string(filepath.Separator))))
		}
	}
	// .claude-8.bak blocks 8; the laptop uses links, so new folders get all eight links.
	if strings.Join(mk, ",") != ".claude-9,.claude-10" {
		t.Errorf("mkdir %v", mk)
	}
	links := 0
	for _, a := range p.Actions {
		if a.Op == model.OpLink {
			links++
		}
	}
	if links != 16 {
		t.Errorf("links %d (want 16: match the layout in use)", links)
	}
}
