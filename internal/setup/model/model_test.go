package model

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "setup", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameJSON fails unless a and b are the same JSON value.
func sameJSON(t *testing.T, what string, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: got is not JSON: %v\n%s", what, err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: golden is not JSON: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		gb, _ := json.MarshalIndent(g, "", " ")
		wb, _ := json.MarshalIndent(w, "", " ")
		t.Fatalf("%s differs from golden\n got: %s\nwant: %s", what, gb, wb)
	}
}

// roundTrip decodes the golden strictly into v, re-marshals and compares: no field of the
// golden is unknown to the type and nothing is lost or invented.
func roundTrip(t *testing.T, name string, v any) {
	t.Helper()
	g := golden(t, name)
	dec := json.NewDecoder(bytes.NewReader(g))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, name+" round trip", out, g)
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReportGolden(t *testing.T) { // setup.md 6.3
	statusCmd := json.RawMessage(`"/home/u/.local/bin/wy statusline --wrap 'bash ~/.claude/statusline-command.sh'"`)
	r := Report{
		V: 1, Mode: ModeDryRun, Primary: "~/.claude",
		State: &State{
			Claude: ClaudeInfo{Path: "~/.local/bin/claude", Version: "2.1.296"},
			Folders: []Folder{{
				Folder: "~/.claude-2", LoggedIn: true, Email: "you@work.example", Org: "Work Inc",
				Plan: "team", Label: "work", Colour: "#f59e0b",
				Launcher: &FolderLauncher{Name: "claude-2", Kind: LauncherFunction},
				Items: map[string]ItemState{
					"settings.json": {State: ItemLinked, Target: "~/.claude/settings.json"},
				},
			}},
			Hub: HubState{},
		},
		Plan: Plan{
			PlanID: "p_9c41e0a7d2b3",
			Keep:   []Keep{{What: "link", Path: "~/.claude-2/settings.json", Target: "~/.claude/settings.json"}},
			Actions: []Action{{
				ID: "a3", Step: StepStatusLine, Op: OpJSONSet, File: "~/.claude/settings.json",
				Via: []string{"~/.claude-2/settings.json"}, Pointer: "/statusLine/command",
				Before: json.RawMessage(`"bash ~/.claude/statusline-command.sh"`), Value: statusCmd,
				Covers: []string{"~/.claude", "~/.claude-2"},
			}},
			Conflicts: []Conflict{{Folder: "~/.claude-3", Item: "CLAUDE.md", Resolution: ResolveKeep,
				Choices: []string{"keep", "primary", "folder"}}},
			Needs: []Need{{Kind: "login", Folder: "~/.claude-4",
				Command: "CLAUDE_CONFIG_DIR=~/.claude-4 claude auth login --claudeai"}},
			Summary: Summary{Changes: 4, BackedUp: 3},
		},
	}
	sameJSON(t, "report", marshal(t, r), golden(t, "report.json"))
	roundTrip(t, "report.json", &Report{})
}

func TestManifestGolden(t *testing.T) { // setup.md 7.2
	m := Manifest{
		V: 1, ID: "20261012T173000Z", Command: "wy setup --yes --hub later", SyVersion: "0.3.0",
		Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", PlanID: "p_9c41e0a7d2b3",
		StartedAt: "2026-10-12T17:30:00.120Z", FinishedAt: "2026-10-12T17:30:04.880Z",
		Status: StatusApplied,
		Entries: []Entry{{
			Seq: 3, Action: "a3", Op: OpJSONSet, Phase: PhaseDone,
			Paths: []PathEntry{{
				Path: "/home/u/.claude/settings.json",
				Via:  []string{"/home/u/.claude-2/settings.json"},
				Before: PathState{Type: TypeFile, Mode: "0644", Size: 2317,
					MtimeNS: 1760290000000000000, SHA256: "aaaa", Saved: "files/000003"},
				After: &PathState{Type: TypeFile, Mode: "0644", Size: 2401, SHA256: "bbbb"},
				Keys: []KeyChange{{Pointer: "/statusLine/command", BeforePresent: true,
					Saved: "keys/000003.json", AfterSHA256: "cccc"}},
			}},
			Created: []string{},
		}},
	}
	sameJSON(t, "manifest", marshal(t, m), golden(t, "manifest.json"))
	roundTrip(t, "manifest.json", &Manifest{})
}

func TestLedgerGolden(t *testing.T) { // setup.md 9.3
	l := Ledger{
		V: 1,
		Plugin: &LedgerPlugin{Marketplace: "/home/u/.local/state/watchyard/plugin/marketplace",
			ID: "watchyard@wy-local", Roots: []string{"/home/u/.claude/plugins"},
			Targets: []string{"/home/u/.claude/settings.json"}},
		StatusLine: map[string]LedgerStatus{
			"/home/u/.claude/settings.json": {Original: "bash /home/u/.claude/statusline-command.sh"}},
		EnvKeys:   []string{"/home/u/.claude/settings.json"},
		Launchers: []LedgerLauncher{{Path: "/home/u/.local/bin/claude-8", SHA256: "dddd"}},
		MCPSync:   []string{"/home/u/.claude.json"},
		HubService: &LedgerService{Name: "watchyard-hub",
			Unit: "/home/u/.config/systemd/user/watchyard-hub.service"},
		Timer: true,
	}
	sameJSON(t, "ledger", marshal(t, l), golden(t, "ledger.json"))
	roundTrip(t, "ledger.json", &Ledger{})
}

func TestActionRunGolden(t *testing.T) { // setup.md 6.1 (run, with preconditions)
	cfg := "/home/u/.claude/plugins"
	a := Action{
		ID: "a2", Step: StepPlugin, Op: OpRun,
		Argv:    []string{"claude", "plugin", "install", "watchyard@wy-local", "--json"},
		Env:     map[string]*string{"CLAUDE_CONFIG_DIR": nil, "CLAUDE_CODE_PLUGIN_CACHE_DIR": &cfg},
		Touches: []string{"/home/u/.claude/settings.json", "/home/u/.claude/plugins/installed_plugins.json"},
		Watch:   []string{"/home/u/.claude/plugins"},
		Pre: []Precondition{
			{Path: "/home/u/.claude/settings.json", State: PathState{Type: TypeFile, Mode: "0644", Size: 2317, SHA256: "aaaa"}},
			{Path: "/home/u/.claude-2/skills", State: PathState{Type: TypeSymlink, Target: "/home/u/.claude/skills"}},
			{Path: "/home/u/.claude-9", State: PathState{Type: TypeAbsent}},
		},
	}
	sameJSON(t, "action", marshal(t, a), golden(t, "action_run.json"))
	roundTrip(t, "action_run.json", &Action{})
}

func TestEveryOpAndStepRoundTrips(t *testing.T) {
	for _, op := range []Op{OpMkdir, OpLink, OpReplaceWithLink, OpMove, OpMergeLines, OpJSONSet,
		OpJSONDelete, OpMCPSync, OpWriteFile, OpWriteTree, OpRun, OpService, OpConnect, OpConfigSet, OpLogin} {
		var back Action
		if err := json.Unmarshal(marshal(t, Action{ID: "a1", Step: StepSharing, Op: op}), &back); err != nil || back.Op != op {
			t.Fatalf("op %s: %v %+v", op, err, back)
		}
	}
	if !StepFolders.Local() || StepHub.Local() || StepLogin.Local() {
		t.Fatal("Local() classifies steps wrongly")
	}
	if len(StepOrder) != 10 || !StepTerminal.Local() {
		t.Fatalf("StepOrder = %v", StepOrder)
	}
}

func TestStateExtrasRoundTrip(t *testing.T) {
	url := "https://hub.example"
	st := State{
		Claude: ClaudeInfo{Path: "~/.local/bin/claude", Version: "2.1.296"},
		Hub:    HubState{URL: &url, Healthy: true},
		Home:   "/home/u", Sy: "/home/u/.local/bin/wy",
		SettingsTargets: []SettingsTarget{{Path: "/home/u/.claude/settings.json", Exists: true,
			Folders: []string{"~/.claude"}, EnvKeys: []string{"FOO"},
			StatusLine: &StatusLine{Type: "command", Command: "x"}}},
		PluginRoots: []PluginRoot{{Root: "/home/u/.claude/plugins", Folders: []string{"~/.claude"}}},
		MCP:         MCPState{HandMade: true, Script: "~/.claude/sync-mcp-to-2.sh"},
		Links:       []LinkRecord{{Path: "/home/u/.claude-2/skills", Raw: "/home/u/.claude/skills"}},
	}
	var back State
	if err := json.Unmarshal(marshal(t, st), &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st, back) {
		t.Fatalf("round trip lost data:\n%+v\n%+v", st, back)
	}
	// A zero MCPState is omitted (omitzero).
	if bytes.Contains(marshal(t, State{}), []byte(`"mcp"`)) {
		t.Fatal("zero MCPState not omitted")
	}
}

func TestModeAndTime(t *testing.T) {
	for _, s := range []string{"0644", "0600", "0755", "0700", "0000"} {
		m, err := ParseMode(s)
		if err != nil || ModeString(m) != s {
			t.Fatalf("%s -> %v %v -> %s", s, m, err, ModeString(m))
		}
	}
	ts := time.Date(2026, 10, 12, 17, 30, 0, 120_000_000, time.UTC)
	if TimeString(ts) != "2026-10-12T17:30:00.120Z" || BackupID(ts) != "20261012T173000Z" {
		t.Fatal(TimeString(ts), BackupID(ts))
	}
}

func TestResolveShare(t *testing.T) {
	names := func(s ShareSet) []string {
		var out []string
		for _, sw := range AllSwitches {
			if s.On[sw] {
				out = append(out, string(sw))
			}
		}
		return out
	}
	cases := []struct {
		in    []string
		hand  bool
		want  []string
		off   []Switch
		isErr bool
	}{
		{nil, false, []string{"settings", "instructions", "addons", "plugins", "mcp", "history", "prompts"}, nil, false},
		{[]string{"defaults"}, true, []string{"settings", "instructions", "addons", "plugins", "history", "prompts"}, nil, false},
		{[]string{"none"}, false, nil, nil, false},
		{[]string{"mcp"}, false, []string{"mcp"}, nil, false},
		{[]string{"settings"}, false, []string{"settings", "plugins"}, nil, false}, // plugins forced on
		{[]string{"defaults", "-history"}, false, []string{"settings", "instructions", "addons", "plugins", "mcp", "prompts"}, []Switch{SwitchHistory}, false},
		{[]string{"instructions", "addons"}, false, []string{"instructions", "addons"}, nil, false},
		{[]string{"bogus"}, false, nil, nil, true},
		{[]string{"-bogus"}, false, nil, nil, true},
	}
	for _, c := range cases {
		got, err := ResolveShare(c.in, c.hand)
		if (err != nil) != c.isErr {
			t.Fatalf("%v: err %v", c.in, err)
		}
		if c.isErr {
			continue
		}
		if !reflect.DeepEqual(names(got), c.want) {
			t.Errorf("%v hand=%v: got %v want %v", c.in, c.hand, names(got), c.want)
		}
		for _, sw := range c.off {
			if !got.ExplicitOff[sw] {
				t.Errorf("%v: %s not marked explicit off", c.in, sw)
			}
		}
	}
}

func TestLabelsAndColours(t *testing.T) {
	for _, s := range []string{"work", "a", "client-2", "0x"} {
		if !ValidLabel(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "-a", "Work", "a_b", "abcdefghijklmnopqrstuvwxyz"} {
		if ValidLabel(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
	if h, ok := ResolveColour("amber"); !ok || h != "#f59e0b" {
		t.Fatal(h, ok)
	}
	if h, ok := ResolveColour("#ABCDEF"); !ok || h != "#abcdef" {
		t.Fatal(h, ok)
	}
	if _, ok := ResolveColour("blurple"); ok {
		t.Fatal("blurple accepted")
	}
}

func TestItemTable(t *testing.T) {
	if len(ShareItems) != 11 || len(ItemsFor(SwitchAddons)) != 4 || len(ItemsFor(SwitchPrompts)) != 2 {
		t.Fatalf("item table changed: %d items", len(ShareItems))
	}
	for _, it := range ShareItems {
		for _, n := range NeverShared {
			if it.Name == n {
				t.Errorf("%s is both shared and never shared", n)
			}
		}
	}
}

func TestUnimplementedStubs(t *testing.T) {
	if _, err := (UnimplementedSharer{}).Snapshot("x"); err != ErrNotImplemented {
		t.Fatal(err)
	}
	if err := (UnimplementedService{}).Uninstall(t.Context(), "x"); err != ErrNotImplemented {
		t.Fatal(err)
	}
	if err := (UnimplementedPluginInstaller{}).WriteMarketplace("a", "b", "c"); err != ErrNotImplemented {
		t.Fatal(err)
	}
	if _, err := (UnimplementedConnector{}).Connect(t.Context(), ConnectRequest{}); err != ErrNotImplemented {
		t.Fatal(err)
	}
}
