package ledger_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/model"
)

func TestReadMissingIsEmptyAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Read(dir)
	if err != nil || l.V != 1 || l.Plugin != nil {
		t.Fatalf("%v %+v", err, l)
	}
	l.EnvKeys = []string{"/x/settings.json"}
	l.Timer = true
	if err := ledger.Write(dir, l); err != nil {
		t.Fatal(err)
	}
	got, err := ledger.Read(dir)
	if err != nil || !reflect.DeepEqual(got, l) {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestUpdateAndRevert(t *testing.T) {
	plan := model.Plan{Actions: []model.Action{
		{ID: "a1", Op: model.OpWriteTree, Step: model.StepPlugin, Path: "/s/plugin/marketplace"},
		{ID: "a2", Op: model.OpRun, Step: model.StepPlugin, Argv: []string{"claude", "plugin", "install", "switchyard@sy-local"},
			Watch: []string{"/h/.claude/plugins"}, Touches: []string{"/h/.claude/settings.json", "/h/.claude/plugins/installed_plugins.json"}},
		{ID: "a3", Op: model.OpJSONSet, Step: model.StepStatusLine, File: "/h/.claude/settings.json", Pointer: "/statusLine/command",
			Before: json.RawMessage(`"bash old.sh"`), Value: json.RawMessage(`"sy statusline --wrap 'bash old.sh'"`)},
		{ID: "a4", Op: model.OpJSONSet, Step: model.StepSharing, File: "/h/.claude-2/settings.json", Pointer: "/env/CLAUDE_CODE_PLUGIN_CACHE_DIR"},
		{ID: "a5", Op: model.OpWriteFile, Step: model.StepLaunchers, Path: "/h/bin/claude-2", ContentSHA256: "abc"},
		{ID: "a6", Op: model.OpMCPSync, Step: model.StepMCP, Files: []string{"/h/.claude.json", "/h/.claude-2/.claude.json"}},
		{ID: "a7", Op: model.OpService, Step: model.StepHub, Name: "switchyard-hub", Unit: "/u/hub.service"},
		{ID: "a8", Op: model.OpConnect, Step: model.StepHub, URL: "https://h"},
	}}
	var m model.Manifest
	for i, a := range plan.Actions {
		e := model.Entry{Seq: i + 1, Action: a.ID, Op: a.Op, Phase: model.PhaseDone}
		switch a.Op {
		case model.OpJSONSet, model.OpJSONDelete:
			e.Paths = []model.PathEntry{{Path: a.File, Keys: []model.KeyChange{{Pointer: a.Pointer}}}}
		case model.OpWriteFile:
			e.Paths = []model.PathEntry{{Path: a.Path}}
		case model.OpMCPSync:
			e.Paths = []model.PathEntry{{Path: a.Files[0]}, {Path: a.Files[1]}}
		case model.OpWriteTree:
			e.Paths = []model.PathEntry{{Path: a.Path}}
		case model.OpRun:
			e.Paths = []model.PathEntry{{Path: a.Touches[1]}}
		case model.OpService:
			e.Name = a.Name
		case model.OpConnect:
			e.Name = "timer"
		}
		m.Entries = append(m.Entries, e)
	}
	l := ledger.Update(model.Ledger{}, plan, m)
	if l.Plugin == nil || l.Plugin.Marketplace != "/s/plugin/marketplace" || l.Plugin.ID != "switchyard@sy-local" ||
		len(l.Plugin.Roots) != 1 || len(l.Plugin.Targets) != 1 {
		t.Fatalf("plugin %+v", l.Plugin)
	}
	if s := l.StatusLine["/h/.claude/settings.json"]; s.Original != "bash old.sh" || s.Created {
		t.Fatalf("status %+v", s)
	}
	if len(l.EnvKeys) != 1 || len(l.Launchers) != 1 || l.Launchers[0].SHA256 != "abc" || len(l.MCPSync) != 2 ||
		l.HubService == nil || !l.Timer {
		t.Fatalf("%+v", l)
	}
	// idempotent: a second update with the same manifest changes nothing
	if l2 := ledger.Update(l, plan, m); !reflect.DeepEqual(l, l2) {
		t.Fatalf("not idempotent:\n%+v\n%+v", l, l2)
	}
	// an undo reverts everything
	for i := range m.Entries {
		m.Entries[i].Phase = model.PhaseReverted
	}
	r := ledger.Revert(l, m)
	if r.Plugin != nil || len(r.StatusLine) != 0 || len(r.EnvKeys) != 0 || len(r.Launchers) != 0 || len(r.MCPSync) != 0 ||
		r.HubService != nil || r.Timer {
		t.Fatalf("revert left %+v", r)
	}
}

// The planner sets the whole /env object when a settings file has no env block; the ledger
// must still record the key as ours (found by S7: uninstall left the key behind).
func TestEnvObjectRecordedAndReverted(t *testing.T) {
	plan := model.Plan{Actions: []model.Action{{ID: "a1", Op: model.OpJSONSet, Step: model.StepSharing,
		File: "/h/.claude/settings.json", Pointer: "/env", Value: json.RawMessage(`{"CLAUDE_CODE_PLUGIN_CACHE_DIR":"/h/.claude/plugins"}`)}}}
	m := model.Manifest{Entries: []model.Entry{{Seq: 1, Action: "a1", Op: model.OpJSONSet, Phase: model.PhaseDone,
		Paths: []model.PathEntry{{Path: "/h/.claude/settings.json", Keys: []model.KeyChange{{Pointer: "/env"}}}}}}}
	l := ledger.Update(model.Ledger{}, plan, m)
	if len(l.EnvKeys) != 1 || l.EnvKeys[0] != "/h/.claude/settings.json" {
		t.Fatalf("env keys %v", l.EnvKeys)
	}
	m.Entries[0].Phase = model.PhaseReverted
	if r := ledger.Revert(l, m); len(r.EnvKeys) != 0 {
		t.Fatalf("not reverted: %v", r.EnvKeys)
	}
}
