package apply_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/watchyard/internal/setup/model"
	splan "github.com/mraj602/watchyard/internal/setup/plan"
	"github.com/mraj602/watchyard/internal/setup/testtree"
	"github.com/mraj602/watchyard/internal/share"
)

// itemPlan plans h with choices and keeps only the actions on the named items of the
// primary (~/.claude) and ~/.claude-3, so the run needs no claude, hub or launchers.
func itemPlan(t *testing.T, h *detecttest.Home, c model.Choices, items ...string) model.Plan {
	t.Helper()
	p, err := splan.PlanWith(h.State(), c, splan.Options{Version: "0.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	on := func(path string) bool {
		for _, it := range items {
			for _, dir := range []string{".claude", ".claude-3"} {
				base := h.Abs(dir + "/" + it)
				if path == base || strings.HasPrefix(path, base+string(filepath.Separator)) {
					return true
				}
			}
		}
		return false
	}
	var keep []model.Action
	for _, a := range p.Actions {
		if a.Step == model.StepSharing && (on(a.Path) || on(a.From)) {
			keep = append(keep, a)
		}
	}
	p.Actions = keep
	return p
}

func homeSnap(t *testing.T, h *detecttest.Home) testtree.Snap {
	return testtree.Take(t, h.Root, testtree.Opts{Ignore: func(rel string) bool {
		return rel == ".local/state" || strings.HasPrefix(rel, ".local/state/")
	}})
}

func homeOpts(h *detecttest.Home) apply.Options {
	return apply.Options{StateDir: h.Abs(".local/state/watchyard"), Command: "t", WyVersion: "0.3.0", Machine: "m"}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// folderHome: ~/.claude-3 has a CLAUDE.md and a skills/ that differ from the primary's, with
// entries only in the folder, only in the primary, in both alike, and in both different.
func folderHome(t *testing.T) *detecttest.Home {
	h := detecttest.Fresh(t)
	b := h.B
	b.File(".claude/skills/a/SKILL.md", "primary a\n", 0o644)
	b.File(".claude/skills/same/SKILL.md", "same\n", 0o644)
	b.File(".claude/skills/onlyp/SKILL.md", "only primary\n", 0o600)
	b.File(".claude-3/.claude.json", `{}`, 0o600)
	b.File(".claude-3/CLAUDE.md", "# folder\n", 0o640)
	b.File(".claude-3/skills/a/SKILL.md", "folder a\n", 0o644)
	b.File(".claude-3/skills/same/SKILL.md", "same\n", 0o644)
	b.File(".claude-3/skills/onlyf/SKILL.md", "only folder\n", 0o644)
	return h
}

// --resolve <folder>:<item>=folder, for a file and a directory, through the real planner and
// the real Sharer: P/X ends with F's content, F/X links to P/X, P's original is in the backup
// set, and undo puts both back byte for byte.
func TestResolveFolderAppliesAndUndoes(t *testing.T) {
	h := folderHome(t)
	o := homeOpts(h)
	sh := share.New(o.StateDir)
	p := itemPlan(t, h, model.Choices{Resolve: map[string]string{
		"~/.claude-3:CLAUDE.md": "folder", "~/.claude-3:skills": "folder"}}, "CLAUDE.md", "skills")
	var ops []string
	for _, a := range p.Actions {
		ops = append(ops, string(a.Op))
	}
	if got := strings.Join(ops, ","); got != "move_to_backup,move,link,move_to_backup,move,link" {
		t.Fatalf("plan: %s", got)
	}
	before := homeSnap(t, h)

	res, err := apply.Run(ctx, p, o, apply.Deps{Sharer: sh})
	if err != nil || res.Status != model.StatusApplied {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if got := readFile(t, h.Abs(".claude/CLAUDE.md")); got != "# folder\n" {
		t.Errorf("P/CLAUDE.md = %q", got)
	}
	if got := readFile(t, h.Abs(".claude/skills/a/SKILL.md")); got != "folder a\n" {
		t.Errorf("P/skills/a = %q", got)
	}
	if got := readFile(t, h.Abs(".claude/skills/onlyf/SKILL.md")); got != "only folder\n" {
		t.Errorf("P/skills/onlyf = %q", got)
	}
	if _, err := os.Lstat(h.Abs(".claude/skills/onlyp")); !os.IsNotExist(err) {
		t.Errorf("the primary's own skill is still in P/skills: %v", err)
	}
	for _, it := range []string{"CLAUDE.md", "skills"} {
		if raw, err := os.Readlink(h.Abs(".claude-3/" + it)); err != nil || raw != h.Abs(".claude/"+it) {
			t.Errorf("F/%s link: %q %v", it, raw, err)
		}
	}
	// The primary's originals are in the set.
	set, err := backup.Open(o.StateDir, res.Backup)
	if err != nil {
		t.Fatal(err)
	}
	saved := map[string]string{}
	for _, e := range set.Manifest().Entries {
		if e.Op == model.OpMoveToBackup {
			saved[filepath.Base(e.Paths[0].Path)] = set.Abs(e.Paths[0].Before.Saved)
		}
	}
	if got := readFile(t, saved["CLAUDE.md"]); got != "# me\n" {
		t.Errorf("saved P/CLAUDE.md = %q", got)
	}
	if got := readFile(t, filepath.Join(saved["skills"], "onlyp/SKILL.md")); got != "only primary\n" {
		t.Errorf("saved P/skills/onlyp = %q", got)
	}
	if got := readFile(t, filepath.Join(saved["skills"], "a/SKILL.md")); got != "primary a\n" {
		t.Errorf("saved P/skills/a = %q", got)
	}

	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: sh}, backup.Options{})
	if err != nil || len(ur.Skipped) > 0 || ur.Status != model.StatusUndone {
		t.Fatalf("undo: %v %+v", err, ur)
	}
	testtree.Equal(t, before, homeSnap(t, h))
}

// A forced failure after the folder resolution rolls it back exactly.
func TestResolveFolderRollback(t *testing.T) {
	h := folderHome(t)
	o := homeOpts(h)
	p := itemPlan(t, h, model.Choices{Resolve: map[string]string{
		"~/.claude-3:CLAUDE.md": "folder", "~/.claude-3:skills": "folder"}}, "CLAUDE.md", "skills")
	before := homeSnap(t, h)
	last := len(p.Actions)
	o.TestHook = func(ev string, seq int) error {
		if ev == "acted" && seq == last {
			return errors.New("boom")
		}
		return nil
	}
	res, err := apply.Run(ctx, p, o, apply.Deps{Sharer: share.New(o.StateDir)})
	if err == nil || res.Status != model.StatusRolledBack || len(res.Rollback.Skipped) > 0 {
		t.Fatalf("want a clean rollback: %v %+v", err, res)
	}
	testtree.Equal(t, before, homeSnap(t, h))
}

// A plain move never lands on an existing path: no merge, no overwrite, nothing changed and
// no backup set.
func TestMoveOntoExistingFailsPrecondition(t *testing.T) {
	for _, dir := range []bool{true, false} {
		b, o, d := simple(t)
		d.Sharer = share.New(o.StateDir)
		if dir {
			b.File("P/skills/a/SKILL.md", "p", 0o644)
			b.File("F/skills/a/SKILL.md", "f", 0o644)
			b.File("F/skills/b/SKILL.md", "fb", 0o644)
		} else {
			b.File("P/skills", "p", 0o644)
			b.File("F/skills", "f", 0o644)
		}
		before := snap(t, b.Root)
		p := plan(model.Action{Op: model.OpMove, From: b.Abs("F/skills"), To: b.Abs("P/skills")})
		res, err := apply.Run(ctx, p, o, d)
		var ae *apply.Error
		if !errors.As(err, &ae) || ae.Code != apply.ExitPrecondition || !errors.Is(err, apply.ErrPrecondition) {
			t.Fatalf("dir=%v: want a precondition error, got %v %+v", dir, err, res)
		}
		testtree.Equal(t, before, snap(t, b.Root))
		// The refusal happens inside the run (no Pre in this plan): the set is rolled back
		// with nothing in it.
		if res.Status != model.StatusRolledBack || len(res.Rollback.Restored) > 0 {
			t.Errorf("dir=%v: %+v", dir, res.Rollback)
		}
	}
}

// A projects merge (5.3, two levels: project, then session file) planned by the real planner
// and run with the real Sharer, then a forced failure of the last action: both folders come
// back exactly.
func TestProjectsMergeRollbackIsExact(t *testing.T) {
	h := detecttest.Fresh(t)
	b := h.B
	b.File(".claude/projects/proj/p1.jsonl", "p1\n", 0o600)
	b.File(".claude/projects/proj/same.jsonl", "same\n", 0o600)
	b.File(".claude/projects/onlyp/x.jsonl", "only p\n", 0o600)
	b.File(".claude-3/.claude.json", `{}`, 0o600)
	b.File(".claude-3/projects/proj/f1.jsonl", "f1\n", 0o600)
	b.File(".claude-3/projects/proj/f2.jsonl", "f2\n", 0o644)
	b.File(".claude-3/projects/proj/same.jsonl", "same\n", 0o600)
	b.File(".claude-3/projects/newproj/n.jsonl", "new\n", 0o600)
	o := homeOpts(h)
	sh := share.New(o.StateDir)
	p := itemPlan(t, h, model.Choices{Resolve: map[string]string{"~/.claude-3:projects": "merge"}}, "projects")
	var moves []string
	for _, a := range p.Actions {
		if a.Op == model.OpMove {
			moves = append(moves, strings.TrimPrefix(a.To, h.Abs(".claude/projects")+"/"))
		}
	}
	if got := strings.Join(moves, ","); got != "newproj,proj/f1.jsonl,proj/f2.jsonl" {
		t.Fatalf("merge moves: %s", got)
	}
	if a := p.Actions[len(p.Actions)-1]; a.Op != model.OpReplaceWithLink || a.Path != h.Abs(".claude-3/projects") {
		t.Fatalf("last action: %+v", a)
	}
	before := homeSnap(t, h)

	// Fail the final link: every move has run.
	last := len(p.Actions)
	o.TestHook = func(ev string, seq int) error {
		if ev == "acted" && seq == last {
			return errors.New("boom")
		}
		return nil
	}
	res, err := apply.Run(ctx, p, o, apply.Deps{Sharer: sh})
	if err == nil || res.Status != model.StatusRolledBack || len(res.Rollback.Skipped) > 0 {
		t.Fatalf("want a clean rollback: %v %+v", err, res)
	}
	testtree.Equal(t, before, homeSnap(t, h))

	// And the same merge applied, then undone.
	o.TestHook = nil
	res, err = apply.Run(ctx, p, o, apply.Deps{Sharer: sh})
	if err != nil {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if got := readFile(t, h.Abs(".claude-3/projects/proj/f2.jsonl")); got != "f2\n" {
		t.Errorf("through the link: %q", got)
	}
	if got := readFile(t, h.Abs(".claude/projects/newproj/n.jsonl")); got != "new\n" {
		t.Errorf("P/projects/newproj: %q", got)
	}
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: sh}, backup.Options{})
	if err != nil || len(ur.Skipped) > 0 {
		t.Fatalf("undo: %v %+v", err, ur)
	}
	testtree.Equal(t, before, homeSnap(t, h))
}

// A later action's precondition on a path an earlier action of the plan changes is checked
// when that action runs, not against the world before the run: promote (5.1) moves F/X into
// the primary and then links F/X, whose precondition is "absent".
func TestPromoteAppliesAndUndoes(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-3/.claude.json", `{}`, 0o600)
	h.B.File(".claude-3/agents/x.md", "x\n", 0o644)
	o := homeOpts(h)
	sh := share.New(o.StateDir)
	p := itemPlan(t, h, model.Choices{}, "agents")
	before := homeSnap(t, h)
	if _, err := apply.Run(ctx, p, o, apply.Deps{Sharer: sh}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := readFile(t, h.Abs(".claude/agents/x.md")); got != "x\n" {
		t.Errorf("promoted: %q", got)
	}
	if raw, err := os.Readlink(h.Abs(".claude-3/agents")); err != nil || raw != h.Abs(".claude/agents") {
		t.Errorf("link: %q %v", raw, err)
	}
	if _, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: sh}, backup.Options{}); err != nil {
		t.Fatal(err)
	}
	testtree.Equal(t, before, homeSnap(t, h))
}

// Undo of a move of a link recreates the link at the source and removes the moved one.
func TestMoveOfLinkUndoesExactly(t *testing.T) {
	b, o, d := simple(t)
	d.Sharer = share.New(o.StateDir)
	b.File("P/x", "x", 0o644).Symlink("F/lnk", "../P/x").Dir("Q", 0o755)
	before := snap(t, b.Root)
	if _, err := apply.Run(ctx, plan(model.Action{Op: model.OpMove, From: b.Abs("F/lnk"), To: b.Abs("Q/lnk")}), o, d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(b.Abs("Q/lnk")); err != nil {
		t.Fatalf("not moved: %v", err)
	}
	if _, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: d.Sharer}, backup.Options{}); err != nil {
		t.Fatal(err)
	}
	testtree.Equal(t, before, snap(t, b.Root))
}
