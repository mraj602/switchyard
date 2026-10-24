package apply_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/backup/fakes"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/testtree"
)

var ctx = context.Background()

func simple(t *testing.T) (*testtree.Builder, apply.Options, apply.Deps) {
	b := testtree.New(t, "")
	o := apply.Options{StateDir: b.Abs("state"), Command: "t", WyVersion: "0.3.0", Machine: "m", WyPath: "/bin/wy"}
	d := apply.Deps{Sharer: &fakes.Sharer{}, Plugins: &fakes.Plugins{}, Contents: contents}
	return b, o, d
}

func plan(as ...model.Action) model.Plan {
	for i := range as {
		as[i].ID = "a" + string(rune('1'+i))
		if as[i].Step == "" {
			as[i].Step = model.StepSharing
		}
	}
	return model.Plan{PlanID: "p_x", Actions: as}
}

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEmptyPlanMakesNoSet(t *testing.T) {
	_, o, d := simple(t)
	res, err := apply.Run(ctx, model.Plan{}, o, d)
	if err != nil || res.Backup != "" {
		t.Fatal(res, err)
	}
	if _, err := os.Stat(filepath.Join(o.StateDir, "backup")); err == nil {
		t.Fatal("backup folder created for an empty plan")
	}
}

func TestUndoKeepsLaterJSONEdits(t *testing.T) {
	b, o, d := simple(t)
	b.File("s/settings.json", `{"statusLine":{"type":"command","command":"old"},"keep":1}`+"\n", 0o644)
	b.Symlink("link/settings.json", "../s/settings.json")
	file := b.Abs("s/settings.json")
	p := plan(model.Action{Op: model.OpJSONSet, File: file, Via: []string{b.Abs("link/settings.json")},
		Pointer: "/statusLine/command", Value: json.RawMessage(`"new"`)})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	// someone else edits another key afterwards
	if _, err := (&fakes.Sharer{}).JSONSet(file, "/later", json.RawMessage(`{"x":2}`)); err != nil {
		t.Fatal(err)
	}
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{})
	if err != nil || ur.ExitCode != 0 || len(ur.KeysRestored) != 1 {
		t.Fatalf("%v %+v", err, ur)
	}
	m := readJSON(t, file)
	if m["statusLine"].(map[string]any)["command"] != "old" || m["later"] == nil || m["keep"] == nil {
		t.Fatalf("got %v", m)
	}
	if fi, _ := os.Lstat(b.Abs("link/settings.json")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link lost")
	}
}

func TestUndoSkipsKeyEditedSinceThenForce(t *testing.T) {
	b, o, d := simple(t)
	b.File("settings.json", `{"a":1}`+"\n", 0o644)
	file := b.Abs("settings.json")
	if _, err := apply.Run(ctx, plan(model.Action{Op: model.OpJSONSet, File: file, Pointer: "/a", Value: json.RawMessage(`2`)}), o, d); err != nil {
		t.Fatal(err)
	}
	_, _ = (&fakes.Sharer{}).JSONSet(file, "/a", json.RawMessage(`3`))
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{})
	if err != nil || ur.ExitCode != backup.ExitSkipped || ur.Status != model.StatusPartlyUndone || len(ur.Skipped) != 1 {
		t.Fatalf("%v %+v", err, ur)
	}
	if readJSON(t, file)["a"].(float64) != 3 {
		t.Fatal("later edit lost")
	}
	// the file as a whole changed since (not equal to after) and has keys: --force still
	// reverts at key level only when the key matches; here force moves the file aside.
	ur, err = backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = ur
}

func TestRelativeSymlinkComesBackRelative(t *testing.T) {
	b, o, d := simple(t)
	b.File("primary/x", "x", 0o644)
	b.Symlink("f/x", "../primary/x")
	start := testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState})
	p := plan(model.Action{Op: model.OpLink, Path: b.Abs("f/x"), Target: b.Abs("primary/x"), Kind: "symlink"})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(b.Abs("f/x")); got != b.Abs("primary/x") {
		t.Fatalf("not re-pointed: %s", got)
	}
	if _, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(b.Abs("f/x")); got != "../primary/x" {
		t.Fatalf("got %q", got)
	}
	testtree.Equal(t, start, testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState}))
}

func TestStackRule(t *testing.T) {
	b, o, d := simple(t)
	for _, n := range []string{"one", "two"} {
		if _, err := apply.Run(ctx, plan(model.Action{Op: model.OpMkdir, Path: b.Abs(n), Mode: "0700"}), o, d); err != nil {
			t.Fatal(err)
		}
		o.Now = nil
	}
	ids, _ := backup.List(o.StateDir)
	if len(ids) != 2 {
		t.Fatal(ids)
	}
	_, err := backup.Undo(ctx, o.StateDir, ids[0].ID, backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{})
	var se *backup.StackError
	if !errors.As(err, &se) {
		t.Fatalf("want StackError, got %v", err)
	}
	if _, err := backup.Undo(ctx, o.StateDir, ids[0].ID, backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	// default picks the newest eligible: two (still applied)
	rid, lines, err := backup.UndoPlan(o.StateDir, "", backup.Options{})
	if err != nil || rid != ids[1].ID || len(lines) != 1 {
		t.Fatal(rid, lines, err)
	}
}

func TestPreconditionMismatchExit4NoSet(t *testing.T) {
	b, o, d := simple(t)
	b.File("f", "hello", 0o644)
	p := plan(model.Action{Op: model.OpWriteFile, Path: b.Abs("f"), Mode: "0644"})
	p.Actions[0].Pre = []model.Precondition{{Path: b.Abs("f"), State: model.PathState{Type: model.TypeFile, SHA256: strings.Repeat("0", 64)}}}
	res, err := apply.Run(ctx, p, o, d)
	var ae *apply.Error
	if !errors.As(err, &ae) || ae.Code != apply.ExitPrecondition || !errors.Is(err, apply.ErrPrecondition) || res.Backup != "" {
		t.Fatalf("%v %+v", err, res)
	}
	if got, _ := os.ReadFile(b.Abs("f")); string(got) != "hello" {
		t.Fatal("changed")
	}
	// the right hash passes
	p.Actions[0].Pre[0].State.SHA256 = backup.SHA256Hex([]byte("hello"))
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
}

func TestLockHeldExit2(t *testing.T) {
	b, o, d := simple(t)
	var inner error
	o.TestHook = func(e string, seq int) error {
		if e == "intent" {
			o2 := o
			o2.TestHook = nil
			_, inner = apply.Run(ctx, plan(model.Action{Op: model.OpMkdir, Path: b.Abs("other"), Mode: "0700"}), o2, d)
		}
		return nil
	}
	if _, err := apply.Run(ctx, plan(model.Action{Op: model.OpMkdir, Path: b.Abs("m"), Mode: "0700"}), o, d); err != nil {
		t.Fatal(err)
	}
	var ae *apply.Error
	if !errors.As(inner, &ae) || ae.Code != apply.ExitLocked {
		t.Fatalf("inner: %v", inner)
	}
	if _, err := os.Stat(b.Abs("other")); err == nil {
		t.Fatal("second run changed things")
	}
}

type repointExec struct{ link string }

func (r repointExec) Run(context.Context, []string, map[string]*string) ([]byte, error) {
	_ = os.Remove(r.link)
	return []byte(`{"outcome":"ok"}`), os.Symlink("/elsewhere", r.link)
}

func TestLinkGuardTripsAndRollsBack(t *testing.T) {
	b, o, d := simple(t)
	b.File("primary/x", "x", 0o644)
	b.Symlink("f/x", "../primary/x")
	o.Links = []model.LinkRecord{{Path: b.Abs("f/x"), Raw: "../primary/x"}}
	d.Exec = repointExec{link: b.Abs("f/x")}
	start := testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState})
	p := plan(model.Action{Op: model.OpMkdir, Path: b.Abs("newdir"), Mode: "0700", Step: model.StepFolders},
		model.Action{Op: model.OpRun, Argv: []string{"claude", "x"}, Step: model.StepPlugin})
	res, err := apply.Run(ctx, p, o, d)
	var ae *apply.Error
	if !errors.As(err, &ae) || ae.Code != apply.ExitFailed || !strings.Contains(err.Error(), "link guard") || res.Status != model.StatusRolledBack {
		t.Fatalf("%v %+v", err, res)
	}
	testtree.Equal(t, start, testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState}))
}

func TestRunOutcomeNotOkFails(t *testing.T) {
	b, o, d := simple(t)
	d.Exec = &fakeExec{root: b.Abs("r"), fail: true}
	_, err := apply.Run(ctx, plan(model.Action{Op: model.OpRun, Argv: []string{"claude"}, Step: model.StepPlugin}), o, d)
	if err == nil || !strings.Contains(err.Error(), "outcome") {
		t.Fatal(err)
	}
}

func TestRunUndoRemovesOnlyOurEntriesAndCreated(t *testing.T) {
	b, o, d := simple(t)
	root := b.Abs("plug")
	b.File("plug/installed_plugins.json", `{"plugins":{"other@x":[1]}}`, 0o644)
	reg := filepath.Join(root, "installed_plugins.json")
	d.Exec = mergeExec{root: root, reg: reg}
	p := plan(model.Action{Op: model.OpRun, Argv: []string{"claude", "plugin"}, Step: model.StepPlugin,
		Touches: []string{reg}, Watch: []string{root}})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	// the fake replaced the file: now add a later entry (someone else installs)
	_, _ = (&fakes.Sharer{}).JSONSet(reg, "/plugins/later@y", json.RawMessage(`[2]`))
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{})
	if err != nil || ur.ExitCode != 0 {
		t.Fatalf("%v %+v", err, ur)
	}
	plugins := readJSON(t, reg)["plugins"].(map[string]any)
	if _, ok := plugins["watchyard@wy-local"]; ok {
		t.Fatal("our entry kept")
	}
	if plugins["other@x"] == nil || plugins["later@y"] == nil {
		t.Fatalf("others lost: %v", plugins)
	}
	if _, err := os.Stat(filepath.Join(root, "cache")); err == nil {
		t.Fatal("created cache kept")
	}
}

func TestCreatedFolderWithUnknownFilesKept(t *testing.T) {
	b, o, d := simple(t)
	if _, err := apply.Run(ctx, plan(model.Action{Op: model.OpMkdir, Path: b.Abs("acct"), Mode: "0700"}), o, d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Abs("acct/.credentials.json"), []byte("login"), 0o600); err != nil {
		t.Fatal(err)
	}
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{})
	if err != nil || len(ur.Kept) != 1 {
		t.Fatalf("%v %+v", err, ur)
	}
	if got, _ := os.ReadFile(b.Abs("acct/.credentials.json")); string(got) != "login" {
		t.Fatal("login touched")
	}
}

// Claude Code creates a login-less .claude.json in a new folder on its first run (setup runs
// `claude auth status` there); undo must still remove the folder it created, and keep one
// whose state file holds an oauthAccount.
func TestCreatedFolderWithLoginlessStateFileRemoved(t *testing.T) {
	b, o, d := simple(t)
	acts := []model.Action{
		{Op: model.OpMkdir, Path: b.Abs("empty"), Mode: "0700"},
		{Op: model.OpMkdir, Path: b.Abs("acct"), Mode: "0700"},
	}
	if _, err := apply.Run(ctx, plan(acts...), o, d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Abs("empty/.claude.json"), []byte(`{"numStartups":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Abs("acct/.claude.json"), []byte(`{"oauthAccount":{"emailAddress":"a@b.c"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{})
	if err != nil || len(ur.Kept) != 1 || ur.Kept[0] != b.Abs("acct") {
		t.Fatalf("%v %+v", err, ur)
	}
	if _, err := os.Stat(b.Abs("empty")); err == nil {
		t.Fatal("folder with a login-less state file kept")
	}
	if _, err := os.Stat(b.Abs("acct/.claude.json")); err != nil {
		t.Fatal("state file with a login removed")
	}
}

func TestStateFileNeverSavedWholeAndMCPKeyRevert(t *testing.T) {
	b, o, d := simple(t)
	b.File("a/.claude.json", `{"oauthAccount":{"email":"SECRET"},"mcpServers":{"x":{"c":1}}}`, 0o600)
	b.File("b/.claude.json", `{"oauthAccount":{"email":"OTHER"},"mcpServers":{}}`, 0o600)
	d.Sharer = &fakes.Sharer{}
	o.MCPFiles = []model.MCPFile{{StateFile: b.Abs("a/.claude.json"), Primary: true}, {StateFile: b.Abs("b/.claude.json")}}
	p := plan(model.Action{Op: model.OpMCPSync, Step: model.StepMCP,
		Files: []string{b.Abs("a/.claude.json"), b.Abs("b/.claude.json")}, Names: &model.NameChanges{Added: []string{"x"}}})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	if m := readJSON(t, b.Abs("b/.claude.json")); m["mcpServers"].(map[string]any)["x"] == nil {
		t.Fatal("not synced")
	}
	_ = filepath.WalkDir(o.StateDir, func(p string, de os.DirEntry, err error) error {
		if err == nil && !de.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "OTHER") {
				t.Errorf("login data in the backup: %s", p)
			}
		}
		return nil
	})
	// Claude Code rewrites its state file meanwhile: oauthAccount changes (token refresh)
	_, _ = (&fakes.Sharer{}).JSONSet(b.Abs("b/.claude.json"), "/oauthAccount", json.RawMessage(`{"email":"REFRESHED"}`))
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{})
	if err != nil || ur.ExitCode != 0 {
		t.Fatalf("%v %+v", err, ur)
	}
	m := readJSON(t, b.Abs("b/.claude.json"))
	if len(m["mcpServers"].(map[string]any)) != 0 || m["oauthAccount"].(map[string]any)["email"] != "REFRESHED" {
		t.Fatalf("got %v", m)
	}
}

func hubPlan(t *testing.T, b *testtree.Builder) model.Plan {
	return plan(
		model.Action{Op: model.OpMkdir, Path: b.Abs("local"), Mode: "0700", Step: model.StepFolders},
		model.Action{Op: model.OpConnect, URL: "https://hub.example", Step: model.StepHub})
}

func TestHubFailureKeepsLocalExit6(t *testing.T) {
	for _, mode := range []string{"error", "unhealthy"} {
		b, o, d := simple(t)
		c := &fakes.Connector{TokenPath: b.Abs("state/token"), ConfigPath: b.Abs("config.toml")}
		if mode == "error" {
			c.N = 1
		} else {
			c.Unhealthy = true
		}
		d.Connector = c
		o.Token, o.ConfigPath = "SECRET-TOKEN", b.Abs("config.toml")
		res, err := apply.Run(ctx, hubPlan(t, b), o, d)
		var ae *apply.Error
		if !errors.As(err, &ae) || ae.Code != apply.ExitHub || res.Hub == nil || res.Hub.Error == "" || res.Status != model.StatusApplied {
			t.Fatalf("%s: %v %+v", mode, err, res)
		}
		if _, err := os.Stat(b.Abs("local")); err != nil {
			t.Fatalf("%s: local step lost", mode)
		}
		if mode == "error" {
			if _, err := os.Stat(b.Abs("state/token")); err == nil {
				t.Fatal("failed connect left a token file")
			}
		}
		_ = filepath.WalkDir(o.StateDir, func(p string, de os.DirEntry, err error) error {
			if err == nil && !de.IsDir() && !strings.HasSuffix(p, "/token") {
				if data, _ := os.ReadFile(p); strings.Contains(string(data), "SECRET-TOKEN") {
					t.Errorf("token in %s", p)
				}
			}
			return nil
		})
	}
}

func TestHubSuccessAndUndoRestoresAll(t *testing.T) {
	b, o, d := simple(t)
	b.File("config.toml", "old = 1\n", 0o600)
	start := testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState})
	c := &fakes.Connector{TokenPath: b.Abs("state/token"), ConfigPath: b.Abs("config.toml")}
	d.Connector = c
	timer := 0
	d.RemoveTimer = func(context.Context) error { timer++; return nil }
	o.Token, o.ConfigPath = "T", b.Abs("config.toml")
	res, err := apply.Run(ctx, hubPlan(t, b), o, d)
	if err != nil || res.Hub == nil || !res.Hub.Healthy {
		t.Fatalf("%v %+v", err, res)
	}
	l, _ := ledger.Read(o.StateDir)
	if !l.Timer {
		t.Fatalf("ledger: %+v", l)
	}
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}, RemoveTimer: d.RemoveTimer}, backup.Options{})
	if err != nil || ur.ExitCode != 0 || timer != 1 {
		t.Fatalf("%v %+v timer=%d", err, ur, timer)
	}
	testtree.Equal(t, start, testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState}))
}

func TestServiceUndoUninstallsAndRestoresUnit(t *testing.T) {
	b, o, d := simple(t)
	svc := &fakes.Service{RemoveUnit: true}
	d.Service = svc
	unit := b.Abs("units/hub.service")
	start := testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState})
	p := plan(model.Action{Op: model.OpService, Name: "watchyard-hub", Unit: unit, Listen: "127.0.0.1:19100", Step: model.StepHub})
	if _, err := apply.Run(ctx, p, o, d); err != nil {
		t.Fatal(err)
	}
	if len(svc.Installed()) != 1 {
		t.Fatal("not installed")
	}
	if got := svc.Spec("watchyard-hub").Listen; got != "127.0.0.1:19100" {
		t.Errorf("service installed with listen %q, want the plan's 127.0.0.1:19100", got)
	}
	ur, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}, Service: svc}, backup.Options{})
	if err != nil || ur.ExitCode != 0 || len(svc.Installed()) != 0 {
		t.Fatalf("%v %+v", err, ur)
	}
	testtree.Equal(t, start, testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState}))
}

func TestConfigSetBacksUpAndUndoes(t *testing.T) {
	b, o, d := simple(t)
	cfg := b.Abs("cfg/config.toml")
	b.File("cfg/config.toml", "a = 1\n", 0o600)
	start := testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState})
	o.ConfigPath = cfg
	d.ConfigSet = func(k string, v json.RawMessage) error {
		return os.WriteFile(cfg, []byte(k+" = "+string(v)+"\n"), 0o600)
	}
	if _, err := apply.Run(ctx, plan(model.Action{Op: model.OpConfigSet, Key: "share.primary", Value: json.RawMessage(`"~"`), Step: model.StepConfig}), o, d); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg + ".bak"); string(b) != "a = 1\n" {
		t.Fatalf("bak: %q", b)
	}
	if _, err := backup.Undo(ctx, o.StateDir, "", backup.Deps{Sharer: &fakes.Sharer{}}, backup.Options{}); err != nil {
		t.Fatal(err)
	}
	testtree.Equal(t, start, testtree.Take(t, b.Root, testtree.Opts{Ignore: ignoreState}))
}

func TestLoginsReturnedNotRun(t *testing.T) {
	_, o, d := simple(t)
	res, err := apply.Run(ctx, plan(model.Action{Op: model.OpLogin, Folder: "~/.claude-4", Step: model.StepLogin}), o, d)
	if err != nil || len(res.Logins) != 1 || res.Backup != "" {
		t.Fatalf("%v %+v", err, res)
	}
}

// mergeExec adds the wy-local entry to the registry without dropping others.
type mergeExec struct{ root, reg string }

func (m mergeExec) Run(ctx context.Context, argv []string, env map[string]*string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Join(m.root, "cache", "wy-local"), 0o755); err != nil {
		return nil, err
	}
	_, err := (&fakes.Sharer{}).JSONSet(m.reg, "/plugins/watchyard@wy-local", json.RawMessage(`[{"v":1}]`))
	return []byte(`{"outcome":"ok"}`), err
}
