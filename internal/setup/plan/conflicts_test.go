package plan_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/model"
)

// conflictHome has one folder for each row of the 5.3 table.
func conflictHome(t *testing.T) (*detecttest.Home, model.State) {
	h := detecttest.Fresh(t)
	b := h.B
	b.File(".claude/skills/a/SKILL.md", "a\n", 0o644)
	b.File(".claude/skills/b/SKILL.md", "b\n", 0o644)
	b.File(".claude/history.jsonl", "{\"timestamp\":2}\n", 0o600)
	b.File(".elsewhere/CLAUDE.md", "# elsewhere\n", 0o644)
	// -2: everything absent (link).
	b.File(".claude-2/.claude.json", `{}`, 0o600)
	// -3: differs.
	b.File(".claude-3/.claude.json", `{}`, 0o600)
	b.File(".claude-3/CLAUDE.md", "# other\n", 0o644)
	b.File(".claude-3/history.jsonl", "{\"timestamp\":1}\n", 0o600)
	b.File(".claude-3/skills/a/SKILL.md", "a\n", 0o644)
	b.File(".claude-3/skills/b/SKILL.md", "DIFF\n", 0o644)
	b.File(".claude-3/skills/c/SKILL.md", "c\n", 0o644)
	// -4: identical.
	b.File(".claude-4/.claude.json", `{}`, 0o600)
	b.File(".claude-4/CLAUDE.md", "# me\n", 0o644)
	b.File(".claude-4/skills/a/SKILL.md", "a\n", 0o644)
	b.File(".claude-4/skills/b/SKILL.md", "b\n", 0o644)
	// -5: link elsewhere; -6: only an extra skill (merge links it).
	b.File(".claude-5/.claude.json", `{}`, 0o600)
	b.Symlink(".claude-5/CLAUDE.md", "$ROOT/.elsewhere/CLAUDE.md")
	b.File(".claude-6/.claude.json", `{}`, 0o600)
	b.File(".claude-6/skills/a/SKILL.md", "a\n", 0o644)
	b.File(".claude-6/skills/b/SKILL.md", "b\n", 0o644)
	b.File(".claude-6/skills/z/SKILL.md", "z\n", 0o644)
	return h, h.State()
}

func actionsOn(p model.Plan, h *detecttest.Home, rel string) []model.Action {
	var out []model.Action
	for _, a := range p.Actions {
		if a.Path == h.Abs(rel) || a.From == h.Abs(rel) || strings.HasPrefix(a.From, h.Abs(rel)+string(filepath.Separator)) {
			out = append(out, a)
		}
	}
	return out
}

func opNames(as []model.Action) string {
	var s []string
	for _, a := range as {
		s = append(s, string(a.Op))
	}
	return strings.Join(s, ",")
}

func conflictOf(p model.Plan, folder, item string) *model.Conflict {
	for i := range p.Conflicts {
		if p.Conflicts[i].Folder == folder && p.Conflicts[i].Item == item {
			return &p.Conflicts[i]
		}
	}
	return nil
}

func TestConflictTableRows(t *testing.T) {
	h, st := conflictHome(t)
	p := mustPlan(t, st, model.Choices{})

	// absent -> link
	if got := opNames(actionsOn(p, h, ".claude-2/CLAUDE.md")); got != "link" {
		t.Errorf("absent: %s", got)
	}
	// identical -> replaced by a link
	if got := opNames(actionsOn(p, h, ".claude-4/CLAUDE.md")); got != "replace_with_link" {
		t.Errorf("identical file: %s", got)
	}
	if got := opNames(actionsOn(p, h, ".claude-4/skills")); got != "replace_with_link" {
		t.Errorf("identical dir: %s", got)
	}
	// link elsewhere -> untouched, kept as "shared elsewhere"
	if got := actionsOn(p, h, ".claude-5/CLAUDE.md"); len(got) != 0 {
		t.Errorf("elsewhere touched: %+v", got)
	}
	found := false
	for _, k := range p.Keep {
		if k.What == "link-elsewhere" && k.Path == "~/.claude-5/CLAUDE.md" {
			found = true
		}
	}
	if !found {
		t.Error("elsewhere link not in keep")
	}
	// differs -> conflict, default keep, nothing planned
	c := conflictOf(p, "~/.claude-3", "CLAUDE.md")
	if c == nil || c.Resolution != "keep" || strings.Join(c.Choices, ",") != "keep,primary,folder" {
		t.Errorf("conflict: %+v", c)
	}
	if got := actionsOn(p, h, ".claude-3/CLAUDE.md"); len(got) != 0 {
		t.Errorf("keep planned something: %+v", got)
	}
	for _, it := range []string{"skills", "history.jsonl"} {
		if c := conflictOf(p, "~/.claude-3", it); c == nil || c.Resolution != "keep" || c.Choices[len(c.Choices)-1] != "merge" {
			t.Errorf("%s conflict: %+v", it, c)
		}
	}
}

func TestConflictResolutions(t *testing.T) {
	h, st := conflictHome(t)
	res := func(key, v string) model.Choices {
		return model.Choices{Resolve: map[string]string{key: v}}
	}

	// file: primary
	p := mustPlan(t, st, res("~/.claude-3:CLAUDE.md", "primary"))
	a := actionsOn(p, h, ".claude-3/CLAUDE.md")
	if opNames(a) != "replace_with_link" || a[0].Target != h.Abs(".claude/CLAUDE.md") || a[0].Pre[0].State.SHA256 == "" {
		t.Errorf("primary: %+v", a)
	}
	// file: folder (primary's moved to the backup set by the move, then linked)
	p = mustPlan(t, st, res("~/.claude-3:CLAUDE.md", "folder"))
	var move, link *model.Action
	for i, x := range p.Actions {
		if x.Op == model.OpMove && x.From == h.Abs(".claude-3/CLAUDE.md") {
			move = &p.Actions[i]
		}
		if x.Op == model.OpLink && x.Path == h.Abs(".claude-3/CLAUDE.md") {
			link = &p.Actions[i]
		}
	}
	if move == nil || link == nil || move.To != h.Abs(".claude/CLAUDE.md") || len(move.Pre) != 2 || link.Pre[0].State.Type != model.TypeAbsent {
		t.Errorf("folder: %+v %+v", move, link)
	}
	// file: merge is refused for CLAUDE.md (kept, with a warning)
	p = mustPlan(t, st, res("~/.claude-3:CLAUDE.md", "merge"))
	if len(actionsOn(p, h, ".claude-3/CLAUDE.md")) != 0 || len(p.Warnings) == 0 || conflictOf(p, "~/.claude-3", "CLAUDE.md").Resolution != "keep" {
		t.Errorf("merge refused: %+v %v", p.Conflicts, p.Warnings)
	}
	// history.jsonl: merge
	p = mustPlan(t, st, res("~/.claude-3:history.jsonl", "merge"))
	a = actionsOn(p, h, ".claude/history.jsonl")
	if opNames(a) != "merge_lines" || a[0].From != h.Abs(".claude-3/history.jsonl") {
		t.Errorf("merge_lines: %+v", a)
	}
	if got := opNames(actionsOn(p, h, ".claude-3/history.jsonl")); got != "merge_lines,replace_with_link" {
		t.Errorf("after merge: %s", got)
	}
	// dir: merge with a differing entry: extra skill moved, not linked, warning lists it
	p = mustPlan(t, st, res("~/.claude-3:skills", "merge"))
	var moves []string
	linked := false
	for _, x := range p.Actions {
		if x.Op == model.OpMove && strings.HasPrefix(x.From, h.Abs(".claude-3/skills")) {
			moves = append(moves, filepath.ToSlash(strings.TrimPrefix(x.To, h.Abs(".claude")+string(filepath.Separator))))
		}
		if x.Op == model.OpReplaceWithLink && x.Path == h.Abs(".claude-3/skills") {
			linked = true
		}
	}
	if strings.Join(moves, ",") != "skills/c" || linked {
		t.Errorf("dir merge: moves %v linked %v", moves, linked)
	}
	if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, "b differ") {
		t.Errorf("warnings: %v", p.Warnings)
	}
	// dir: merge with only missing entries: moved, then linked
	p = mustPlan(t, st, res("~/.claude-6:skills", "merge"))
	if opNames(actionsOn(p, h, ".claude-6/skills")) != "move,replace_with_link" {
		t.Errorf("clean merge: %v", opNames(actionsOn(p, h, ".claude-6/skills")))
	}
	hasMove := false
	for _, x := range p.Actions {
		if x.Op == model.OpMove && x.From == h.Abs(".claude-6/skills/z") && x.To == h.Abs(".claude/skills/z") {
			hasMove = true
		}
	}
	if !hasMove {
		t.Error("extra skill not moved to the primary")
	}
	// dir: primary / folder
	p = mustPlan(t, st, res("~/.claude-3:skills", "primary"))
	if opNames(actionsOn(p, h, ".claude-3/skills")) != "replace_with_link" {
		t.Error("dir primary")
	}
	p = mustPlan(t, st, res("~/.claude-3:skills", "folder"))
	if opNames(actionsOn(p, h, ".claude-3/skills")) != "move,link" {
		t.Errorf("dir folder: %s", opNames(actionsOn(p, h, ".claude-3/skills")))
	}
	// --conflicts primary applies to every differing item; merge falls back to keep for CLAUDE.md
	p = mustPlan(t, st, model.Choices{Conflicts: "primary"})
	for _, it := range []string{"CLAUDE.md", "skills", "history.jsonl"} {
		if c := conflictOf(p, "~/.claude-3", it); c.Resolution != "primary" || opNames(actionsOn(p, h, ".claude-3/"+it)) != "replace_with_link" {
			t.Errorf("--conflicts primary %s: %+v", it, c)
		}
	}
	p = mustPlan(t, st, model.Choices{Conflicts: "merge"})
	if c := conflictOf(p, "~/.claude-3", "CLAUDE.md"); c.Resolution != "keep" {
		t.Errorf("global merge on a file: %+v", c)
	}
	if c := conflictOf(p, "~/.claude-3", "history.jsonl"); c.Resolution != "merge" {
		t.Errorf("global merge on history: %+v", c)
	}
}

// `folder` (5.3): the primary's copy goes into the backup set first, then the folder's copy
// is moved in and linked, so the move never lands on an existing path.
func TestResolveFolderPlansSaveMoveLink(t *testing.T) {
	h, st := conflictHome(t)
	for _, it := range []string{"CLAUDE.md", "skills"} {
		p := mustPlan(t, st, model.Choices{Resolve: map[string]string{"~/.claude-3:" + it: "folder"}})
		pp, fp := h.Abs(".claude/"+it), h.Abs(".claude-3/"+it)
		i := -1
		for k, a := range p.Actions {
			if a.Op == model.OpMoveToBackup && a.Path == pp {
				i = k
			}
		}
		if i < 0 || i+2 >= len(p.Actions) {
			t.Fatalf("%s: no move_to_backup of %s: %s", it, pp, dump(p))
		}
		save, move, link := p.Actions[i], p.Actions[i+1], p.Actions[i+2]
		wantType := model.TypeFile
		if it == "skills" {
			wantType = model.TypeDir
		}
		if len(save.Pre) != 1 || save.Pre[0].Path != pp || save.Pre[0].State.Type != wantType ||
			(it == "CLAUDE.md" && save.Pre[0].State.SHA256 == "") {
			t.Errorf("%s save: %+v", it, save)
		}
		if move.Op != model.OpMove || move.From != fp || move.To != pp || len(move.Pre) != 2 ||
			move.Pre[0].Path != fp || move.Pre[0].State.Type != wantType ||
			move.Pre[1].Path != pp || move.Pre[1].State.Type != model.TypeAbsent {
			t.Errorf("%s move: %+v", it, move)
		}
		if link.Op != model.OpLink || link.Path != fp || link.Target != pp || link.Pre[0].State.Type != model.TypeAbsent {
			t.Errorf("%s link: %+v", it, link)
		}
		if c := conflictOf(p, "~/.claude-3", it); c == nil || c.Resolution != "folder" {
			t.Errorf("%s conflict: %+v", it, c)
		}
	}
}

func TestPromoteToPrimary(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	h.B.File(".claude-3/.claude.json", `{}`, 0o600)
	h.B.File(".claude-2/agents/x.md", "x\n", 0o644)
	h.B.File(".claude-3/agents/x.md", "x\n", 0o644)
	p := mustPlan(t, h.State(), model.Choices{})
	if got := opNames(actionsOn(p, h, ".claude-2/agents")); got != "move,link" {
		t.Errorf("promote: %s", got)
	}
	if got := opNames(actionsOn(p, h, ".claude-3/agents")); got != "replace_with_link" {
		t.Errorf("rest: %s", got)
	}
	// Different copies: no promote, a warning.
	h.B.File(".claude-3/agents/y.md", "y\n", 0o644)
	p = mustPlan(t, h.State(), model.Choices{})
	if len(actionsOn(p, h, ".claude-2/agents")) != 0 || len(p.Warnings) == 0 {
		t.Errorf("differing copies: %v", p.Warnings)
	}
	// Nothing anywhere: nothing to share, nothing created.
	nothing := false
	for _, k := range p.Keep {
		if k.What == "nothing-to-share" && k.Path == "rules" {
			nothing = true
		}
	}
	if !nothing {
		t.Error("rules not reported as nothing to share")
	}
	for _, a := range p.Actions {
		if strings.HasSuffix(a.Path, "/rules") {
			t.Errorf("rules created: %+v", a)
		}
	}
}

func TestSharingOffAndExplicitOff(t *testing.T) {
	h, st := conflictHome(t)
	p := mustPlan(t, st, model.Choices{Share: []string{"none"}})
	for _, a := range p.Actions {
		if a.Step == model.StepSharing {
			t.Errorf("share none planned %+v", a)
		}
	}
	// A linked item with its switch off stays linked; only an explicit -name warns.
	lh := detecttest.Laptop(t)
	ls := lh.State()
	p = mustPlan(t, ls, model.Choices{Share: []string{"defaults", "-history"}})
	if len(p.Warnings) == 0 {
		t.Error("explicit off of a linked item gave no warning")
	}
	_ = filepath.Join
	_ = h
}

func TestHubPrototypeMCPLaunchers(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	// join with a token source: connect action; prototype removal allowed, three entries.
	c := model.Choices{Hub: model.HubChoice{Mode: model.HubJoin, URL: "https://hub.example"}, TokenStdin: true, Prototype: "remove"}
	p := mustPlan(t, st, c)
	var del, conn int
	for _, a := range p.Actions {
		if a.Op == model.OpJSONDelete {
			del++
			if !strings.HasPrefix(a.Pointer, "/hooks/") || strings.Count(a.Pointer, "/") != 2 {
				t.Errorf("event pointer: %s", a.Pointer)
			}
		}
		if a.Op == model.OpConnect && a.URL == "https://hub.example" {
			conn++
		}
	}
	if del != 3 || conn != 1 {
		t.Errorf("delete %d connect %d", del, conn)
	}
	// join without a token source: a need, no connect.
	c.TokenStdin, c.Prototype = false, ""
	p = mustPlan(t, st, c)
	if len(p.Needs) != 5 || p.Needs[0].Kind != "hub-token" {
		t.Errorf("needs %+v", p.Needs)
	}
	// here: service + connect.
	p = mustPlan(t, st, model.Choices{Hub: model.HubChoice{Mode: model.HubHere}})
	got := ops(p)
	if got[len(got)-2] != "service" || got[len(got)-1] != "connect" {
		t.Errorf("here: %v", got)
	}
	// Named launchers are written next to claude-N only for folders without one.
	fh := detecttest.Fresh(t)
	fh.B.File(".claude-2/.claude.json", `{}`, 0o600)
	fs := fh.State()
	p = mustPlan(t, fs, model.Choices{NamedLaunchers: true, Labels: map[string]model.LabelColour{"~/.claude-2": {Label: "work"}}})
	var names []string
	for _, a := range p.Actions {
		if a.Op == model.OpWriteFile && strings.Contains(filepath.ToSlash(a.Path), ".local/bin/") {
			names = append(names, filepath.Base(a.Path))
		}
	}
	if strings.Join(names, ",") != "claude-2,claude-work" {
		t.Errorf("launchers %v", names)
	}
	p = mustPlan(t, fs, model.Choices{NoLaunchers: true, NoPlugin: true, NoStatusLine: true})
	for _, a := range p.Actions {
		if a.Step == model.StepLaunchers || a.Step == model.StepPlugin || a.Step == model.StepStatusLine {
			t.Errorf("disabled step planned: %+v", a)
		}
	}
}

func TestMCPSync(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude.json", `{"oauthAccount":{"emailAddress":"a"},"mcpServers":{"alpha":{"command":"a"},"same":{"command":"s"}}}`, 0o600)
	h.B.File(".claude-2/.claude.json", `{"oauthAccount":{"emailAddress":"b"},"mcpServers":{"beta":{"command":"b"},"same":{"command":"DIFFERENT"}}}`, 0o600)
	h.Fake.SetLogin(h.Abs(".claude-2"), "b@example.com", "B", "max")
	p := mustPlan(t, h.State(), model.Choices{NoPlugin: true, NoStatusLine: true})
	var sync *model.Action
	for i, a := range p.Actions {
		if a.Op == model.OpMCPSync {
			sync = &p.Actions[i]
		}
	}
	if sync == nil || len(sync.Files) != 2 || strings.Join(sync.Names.Added, ",") != "alpha,beta" || strings.Join(sync.Names.Changed, ",") != "same" {
		t.Fatalf("mcp_sync: %+v\n%s", sync, dump(p))
	}
	// Nothing differs: no action.
	h.B.File(".claude-2/.claude.json", `{"oauthAccount":{"emailAddress":"b"},"mcpServers":{"alpha":{"command":"a"},"same":{"command":"s"}}}`, 0o600)
	h2 := h.State()
	for i := range h2.Folders {
		h2.Folders[i].MCPNames = map[string]string{"alpha": "1", "same": "2"}
	}
	p = mustPlan(t, h2, model.Choices{NoPlugin: true, NoStatusLine: true})
	for _, a := range p.Actions {
		if a.Op == model.OpMCPSync {
			t.Errorf("sync planned with nothing to do: %+v", a)
		}
	}
}
