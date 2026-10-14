package apply_test

// Regression tests for the phase 3 review findings on undo, rollback and crash recovery:
// undo never deletes or overwrites what the person made after the run.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/setup/apply"
	"github.com/mraj602/switchyard/internal/setup/backup"
	"github.com/mraj602/switchyard/internal/setup/backup/fakes"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/share"
)

func undoAll(t *testing.T, state string, sh model.Sharer) backup.Result {
	t.Helper()
	ur, err := backup.Undo(ctx, state, "", backup.Deps{Sharer: sh}, backup.Options{})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	return ur
}

// Finding 3: the parents a link action created are removed only when empty; a login and
// skills added in them later stay.
func TestUndoKeepsLaterFilesInCreatedParents(t *testing.T) {
	b, o, d := simple(t)
	b.File("primary/skills/x", "x", 0o644)
	p := plan(model.Action{Op: model.OpLink, Path: b.Abs("acct/skills/shared"), Target: b.Abs("primary/skills"), Kind: "symlink"})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	b.File("acct/.credentials.json", "SECRET", 0o600)
	b.File("acct/skills/mine/SKILL.md", "user data", 0o644)
	ur := undoAll(t, o.StateDir, &fakes.Sharer{})
	for _, f := range []string{"acct/skills/mine/SKILL.md", "acct/.credentials.json"} {
		if _, err := os.Stat(b.Abs(f)); err != nil {
			t.Errorf("%s lost: %v (%+v)", f, err, ur)
		}
	}
	if _, err := os.Lstat(b.Abs("acct/skills/shared")); err == nil {
		t.Error("the link itself was not removed")
	}
}

// Finding 2: a kept folder's .claude.json (servers, projects) is never deleted; only a
// first-run state file that is the folder's only entry goes with the folder.
func TestUndoKeepsStateFileOfAFolderInUse(t *testing.T) {
	b, o, d := simple(t)
	if _, err := apply.Run(ctx, plan(model.Action{Op: model.OpMkdir, Path: b.Abs("acct"), Mode: "0700"}), o, d); err != nil {
		t.Fatal(err)
	}
	b.File("acct/.claude.json", `{"numStartups":40,"mcpServers":{"gh":{}},"projects":{"/w":{}}}`, 0o600)
	b.File("acct/projects/w/s.jsonl", "{}\n", 0o600)
	undoAll(t, o.StateDir, &fakes.Sharer{})
	for _, f := range []string{"acct/projects/w/s.jsonl", "acct/.claude.json"} {
		if _, err := os.Stat(b.Abs(f)); err != nil {
			t.Errorf("%s lost: %v", f, err)
		}
	}
}

func TestUndoRemovesFolderHoldingOnlyFirstRunState(t *testing.T) {
	b, o, d := simple(t)
	if _, err := apply.Run(ctx, plan(model.Action{Op: model.OpMkdir, Path: b.Abs("acct"), Mode: "0700"}), o, d); err != nil {
		t.Fatal(err)
	}
	b.File("acct/.claude.json", `{"numStartups":1}`, 0o600).Dir("acct/sessions", 0o700)
	undoAll(t, o.StateDir, &fakes.Sharer{})
	if _, err := os.Stat(b.Abs("acct")); err == nil {
		t.Fatal("folder with only first-run state kept")
	}
}

type createRegExec struct{ reg string }

func (c createRegExec) Run(context.Context, []string, map[string]*string) ([]byte, error) {
	return []byte(`{"outcome":"ok"}`), os.WriteFile(c.reg, []byte(`{"plugins":{"switchyard@sy-local":[1]}}`), 0o644)
}

// Finding 8: a registry the run created is reverted entry by entry when someone registered
// other plugins in it since; it is not deleted.
func TestUndoKeepsCreatedRegistryWithLaterEntries(t *testing.T) {
	b, o, d := simple(t)
	b.Dir("plug", 0o755)
	reg := b.Abs("plug/installed_plugins.json")
	d.Exec = createRegExec{reg: reg}
	p := plan(model.Action{Op: model.OpRun, Argv: []string{"claude", "plugin"}, Step: model.StepPlugin,
		Touches: []string{reg}, Watch: []string{b.Abs("plug")}})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	_, _ = (&fakes.Sharer{}).JSONSet(reg, "/plugins/later@y", json.RawMessage(`[2]`))
	undoAll(t, o.StateDir, &fakes.Sharer{})
	plugins := readJSON(t, reg)["plugins"].(map[string]any)
	if plugins["later@y"] == nil || plugins["switchyard@sy-local"] != nil {
		t.Fatalf("%v", plugins)
	}
}

// Finding 8: a file created under a plugin root that changed since the run is left alone.
func TestUndoSkipsCreatedFileChangedSince(t *testing.T) {
	b, o, d := simple(t)
	root := b.Abs("plug")
	reg := filepath.Join(root, "installed_plugins.json")
	b.File("plug/installed_plugins.json", `{"plugins":{}}`, 0o644)
	d.Exec = mergeExec{root: root, reg: reg}
	p := plan(model.Action{Op: model.OpRun, Argv: []string{"claude", "plugin"}, Step: model.StepPlugin,
		Touches: []string{reg}, Watch: []string{root}})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	b.File("plug/cache/sy-local/notes.txt", "mine", 0o644) // a file sy did not create
	ur := undoAll(t, o.StateDir, &fakes.Sharer{})
	if _, err := os.Stat(b.Abs("plug/cache/sy-local/notes.txt")); err != nil {
		t.Fatalf("later file removed: %+v", ur)
	}
}

// Finding 7: recovery of an interrupted JSON edit keeps edits made after the crash.
func TestRecoveryKeepsEditsMadeAfterTheCrash(t *testing.T) {
	b, o, _ := simple(t)
	b.File("settings.json", `{"a":1}`, 0o644)
	f := b.Abs("settings.json")
	s, err := backup.Create(o.StateDir, backup.Header{Command: "t"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin("a1", model.OpJSONSet, backup.Spec{Paths: []backup.PathSpec{{Path: f, Save: backup.SaveCopy,
		Keys: []string{"/a"}, Intend: map[string]json.RawMessage{"/a": json.RawMessage(`2`)}}}}); err != nil {
		t.Fatal(err)
	}
	// crash before the action ran; days later the person edits another key
	if err := os.WriteFile(f, []byte(`{"a":1,"userkey":"important"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.UndoSet(ctx, s, backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{}, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f); !strings.Contains(string(got), "userkey") {
		t.Fatalf("edit lost: %s", got)
	}
	// and a value the person set at the key itself is not reverted
	if err := os.WriteFile(f, []byte(`{"a":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, _ := backup.Create(o.StateDir, backup.Header{Command: "t"}, time.Now().Add(time.Second))
	_ = os.WriteFile(f, []byte(`{"a":1}`), 0o644)
	_, _ = s2.Begin("a1", model.OpJSONSet, backup.Spec{Paths: []backup.PathSpec{{Path: f, Save: backup.SaveCopy,
		Keys: []string{"/a"}, Intend: map[string]json.RawMessage{"/a": json.RawMessage(`2`)}}}})
	_ = os.WriteFile(f, []byte(`{"a":7}`), 0o644)
	ur, _ := backup.UndoSet(ctx, s2, backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{}, true)
	if got, _ := os.ReadFile(f); string(got) != `{"a":7}` || len(ur.Skipped) != 1 {
		t.Fatalf("got %s %+v", got, ur)
	}
}

// Finding 7: recovery of an interrupted file write leaves a file changed since alone.
func TestRecoveryKeepsAFileRewrittenAfterTheCrash(t *testing.T) {
	b, o, _ := simple(t)
	f := b.Abs("bin/claude-2")
	s, _ := backup.Create(o.StateDir, backup.Header{Command: "t"}, time.Now())
	content := []byte("launcher\n")
	if _, err := s.Begin("a1", model.OpWriteFile, backup.Spec{Paths: []backup.PathSpec{{Path: f, Save: backup.SaveCopy,
		Intended: &model.PathState{Type: model.TypeFile, Size: int64(len(content)), SHA256: backup.SHA256Hex(content)}}}}); err != nil {
		t.Fatal(err)
	}
	b.File("bin/claude-2", "the person's own script\n", 0o755)
	ur, _ := backup.UndoSet(ctx, s, backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{}, true)
	if got, _ := os.ReadFile(f); string(got) != "the person's own script\n" || len(ur.Skipped) != 1 {
		t.Fatalf("got %q %+v", got, ur)
	}
}

// Finding 7: an interrupted plugin command removes only what it may have made for sy's
// marketplace, not what appeared under the root since.
func TestRecoveryOfRunRemovesOnlyExpectedPaths(t *testing.T) {
	b, o, _ := simple(t)
	root := b.Abs("plug")
	b.Dir("plug", 0o755)
	s, _ := backup.Create(o.StateDir, backup.Header{Command: "t"}, time.Now())
	if _, err := s.Begin("a1", model.OpRun, backup.Spec{Watch: []string{root}, Expect: []string{"sy-local"}}); err != nil {
		t.Fatal(err)
	}
	b.File("plug/cache/sy-local/switchyard/plugin.json", "{}", 0o644) // the interrupted install
	b.File("plug/cache/other-mkt/thing/plugin.json", "{}", 0o644)     // installed by the person later
	if _, err := backup.UndoSet(ctx, s, backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.Abs("plug/cache/other-mkt/thing/plugin.json")); err != nil {
		t.Fatal("a plugin installed later was removed")
	}
	if _, err := os.Stat(b.Abs("plug/cache/sy-local")); err == nil {
		t.Fatal("sy's half-installed plugin kept")
	}
}

// Finding 5: the MCP base is journaled, so after undo the next sync does not read the
// reverted keys as deletions and remove servers everywhere.
func TestMCPUndoRestoresTheBase(t *testing.T) {
	b, o, d := simple(t)
	b.File("a/.claude.json", `{"mcpServers":{"x":{"command":"x"}}}`, 0o600)
	b.File("b/.claude.json", `{"mcpServers":{"y":{"command":"y"}}}`, 0o600)
	sh := share.New(o.StateDir)
	d.Sharer = sh
	files := []model.MCPFile{{Folder: "a", StateFile: b.Abs("a/.claude.json"), Dir: b.Abs("a"), Primary: true},
		{Folder: "b", StateFile: b.Abs("b/.claude.json"), Dir: b.Abs("b")}}
	o.MCPFiles = files
	p := plan(model.Action{Op: model.OpMCPSync, Step: model.StepMCP, Files: []string{files[0].StateFile, files[1].StateFile},
		Names: &model.NameChanges{Added: []string{"x", "y"}}})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(share.MCPBasePath(o.StateDir)); err != nil {
		t.Fatal("no base after sync")
	}
	if ur := undoAll(t, o.StateDir, sh); ur.ExitCode != 0 {
		t.Fatalf("%+v", ur)
	}
	if _, err := os.Stat(share.MCPBasePath(o.StateDir)); err == nil {
		t.Fatal("base not restored (it did not exist before)")
	}
	if _, err := sh.MCPSync(ctx, model.MCPSyncRequest{StateDir: o.StateDir, Files: files}); err != nil {
		t.Fatal(err)
	}
	if readJSON(t, files[0].StateFile)["mcpServers"].(map[string]any)["x"] == nil ||
		readJSON(t, files[1].StateFile)["mcpServers"].(map[string]any)["y"] == nil {
		t.Fatal("a server was deleted after undo")
	}
	if apply.MCPBasePath(o.StateDir) != share.MCPBasePath(o.StateDir) {
		t.Fatal("apply and share disagree on the base path")
	}
}

// Findings 1/9: undo of a LinkCopy deregisters the copy, so the restored original is never
// synced over the primary.
func TestUndoOfLinkCopyDeregisters(t *testing.T) {
	b, o, d := simple(t)
	b.File("p/settings.json", `{"primary":true}`, 0o644).File("f/settings.json", `{"mine":true}`, 0o644)
	sh := share.New(o.StateDir)
	d.Sharer = sh
	p := plan(model.Action{Op: model.OpReplaceWithLink, Path: b.Abs("f/settings.json"), Target: b.Abs("p/settings.json"), Kind: model.LinkCopy})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	if g, _ := share.LoadCopies(o.StateDir); len(g) != 1 {
		t.Fatal("copy not registered")
	}
	undoAll(t, o.StateDir, sh)
	if g, _ := share.LoadCopies(o.StateDir); len(g) != 0 {
		t.Fatalf("copy still registered after undo: %+v", g)
	}
	if _, err := share.SyncCopies(o.StateDir, []string{b.Abs("f/settings.json")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(b.Abs("p/settings.json")); string(got) != `{"primary":true}` {
		t.Fatalf("primary overwritten: %s", got)
	}
}

// Lower finding (StateEq on folders): undo of a copied tree keeps files added in it since.
func TestUndoOfCopiedTreeKeepsNewFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	// the unshare of `sy account remove --unshare`: a link turned into a copied tree, journaled
	for _, edit := range []bool{false, true} {
		b, o, _ := simple(t)
		b.File("p/skills/a/SKILL.md", "x", 0o644)
		b.Symlink("f/skills", b.Abs("p/skills"))
		sh := share.New(o.StateDir)
		p := b.Abs("f/skills")
		s, _ := backup.Create(o.StateDir, backup.Header{Command: "t"}, time.Now())
		tx, err := s.Begin("a1", model.OpReplaceWithLink, backup.Spec{Paths: []backup.PathSpec{{Path: p}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := sh.Unshare(p); err != nil {
			t.Fatal(err)
		}
		if err := tx.Finish(nil); err != nil {
			t.Fatal(err)
		}
		if err := s.SetStatus(model.StatusApplied); err != nil {
			t.Fatal(err)
		}
		if g, _ := share.LoadCopies(o.StateDir); len(g) != 0 {
			t.Fatalf("unshare registered a synced copy: %+v", g)
		}
		if edit {
			b.File("f/skills/new/SKILL.md", "precious", 0o644)
		}
		ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: sh}, backup.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if edit {
			if ur.ExitCode != backup.ExitSkipped {
				t.Fatalf("%+v", ur)
			}
			if got, _ := os.ReadFile(p + "/new/SKILL.md"); string(got) != "precious" {
				t.Fatal("new work deleted by undo")
			}
			continue
		}
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 || ur.ExitCode != 0 {
			t.Fatalf("link not restored: %v %+v", err, ur)
		}
	}
}
