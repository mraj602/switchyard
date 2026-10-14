package pluginmkt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/claudecli"
	"github.com/mraj602/switchyard/plugin"
)

func readEmbedded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(plugin.FS(), name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHookCommand(t *testing.T) {
	got, err := HookCommand("command -v sy >/dev/null 2>&1 && sy hook Stop || true", "/home/u/.local/bin/sy")
	want := `[ -x '/home/u/.local/bin/sy' ] && '/home/u/.local/bin/sy' hook Stop || true`
	if err != nil || got != want {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := HookCommand("echo hi", "/x/sy"); err == nil {
		t.Fatal("unrecognised command accepted")
	}
	got, _ = HookCommand("command -v sy >/dev/null 2>&1 && sy hook Stop || true", "/home/o'brien/sy")
	if !strings.Contains(got, `'/home/o'\''brien/sy'`) {
		t.Fatalf("quote not escaped: %s", got)
	}
}

func TestHookCommandRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	dir := filepath.Join(t.TempDir(), "it's here")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sy := filepath.Join(dir, "sy")
	if err := os.WriteFile(sy, []byte("#!/bin/sh\necho \"called: $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, err := HookCommand("command -v sy >/dev/null 2>&1 && sy hook Stop || true", sy)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil || strings.TrimSpace(string(out)) != "called: hook Stop" {
		t.Fatalf("%q %v", out, err)
	}
	// missing binary: exits 0 quietly
	if err := os.Remove(sy); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("missing sy must be silent and exit 0: %q %v", out, err)
	}
}

type hooksDoc struct {
	Description string `json:"description"`
	Hooks       map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Async   bool   `json:"async"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func TestRewriteHooksOnlyCommandsChange(t *testing.T) {
	orig := readEmbedded(t, "hooks/hooks.json")
	const sy = "/home/u/.local/bin/sy"
	out, err := RewriteHooks(orig, sy)
	if err != nil {
		t.Fatal(err)
	}
	var a, b hooksDoc
	if err := json.Unmarshal(orig, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatal(err)
	}
	if len(a.Hooks) == 0 || len(a.Hooks) != len(b.Hooks) {
		t.Fatalf("events: %d vs %d", len(a.Hooks), len(b.Hooks))
	}
	n := 0
	for ev, ga := range a.Hooks {
		gb := b.Hooks[ev]
		if len(ga) != len(gb) {
			t.Fatalf("%s: groups differ", ev)
		}
		for i := range ga {
			if ga[i].Matcher != gb[i].Matcher || len(ga[i].Hooks) != len(gb[i].Hooks) {
				t.Fatalf("%s: matcher differs", ev)
			}
			for j := range ga[i].Hooks {
				x, y := ga[i].Hooks[j], gb[i].Hooks[j]
				want, _ := HookCommand(x.Command, sy)
				if y.Command != want {
					t.Fatalf("%s: command %q", ev, y.Command)
				}
				x.Command, y.Command = "", ""
				if !reflect.DeepEqual(x, y) {
					t.Fatalf("%s: other fields differ: %+v vs %+v", ev, x, y)
				}
				n++
			}
		}
	}
	if a.Description != b.Description || n == 0 {
		t.Fatal("description or hook count")
	}
	// byte level: undoing the rewrite on the commands gives the original bytes back
	back := out
	for _, ga := range a.Hooks {
		for _, g := range ga {
			for _, h := range g.Hooks {
				w, _ := HookCommand(h.Command, sy)
				back = bytes.ReplaceAll(back, jsonString(w), jsonString(h.Command))
			}
		}
	}
	if !bytes.Equal(back, orig) {
		t.Fatal("something other than the commands changed")
	}
	ev1, _ := HooksEvents(orig)
	ev2, _ := HooksEvents(out)
	if !reflect.DeepEqual(ev1, ev2) || len(ev1) == 0 {
		t.Fatalf("%v vs %v", ev1, ev2)
	}
}

func TestSetVersion(t *testing.T) {
	got := setVersion([]byte(`{"name":"x","version": "0.1.0","nested":{"version":"z"}}`), "1.2.3")
	if string(got) != `{"name":"x","version": "1.2.3","nested":{"version":"z"}}` {
		t.Fatal(string(got))
	}
}

func TestWriteMarketplace(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "plugin", "marketplace")
	in := New(claudecli.NewFake())
	sy := filepath.Join(state, "bin", "sy")
	if err := in.WriteMarketplace(dir, sy, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	var mj struct {
		Name    string                `json:"name"`
		Owner   struct{ Name string } `json:"owner"`
		Plugins []struct {
			Name, Source, Version string
		} `json:"plugins"`
	}
	b, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	if err != nil || json.Unmarshal(b, &mj) != nil {
		t.Fatalf("%v %s", err, b)
	}
	if mj.Name != "sy-local" || mj.Owner.Name == "" || len(mj.Plugins) != 2 ||
		mj.Plugins[0].Name != "switchyard" || mj.Plugins[0].Source != "./plugins/switchyard" || mj.Plugins[0].Version != "0.3.0" ||
		mj.Plugins[1].Name != "switchyard-pane" || mj.Plugins[1].Source != "./plugins/switchyard-pane" || mj.Plugins[1].Version != "0.3.0" {
		t.Fatalf("%+v", mj)
	}
	pd := PluginDir(dir)
	pj, err := os.ReadFile(filepath.Join(pd, ".claude-plugin", "plugin.json"))
	if err != nil || !strings.Contains(string(pj), `"version": "0.3.0"`) {
		t.Fatalf("%v %s", err, pj)
	}
	hj, err := os.ReadFile(filepath.Join(pd, "hooks", "hooks.json"))
	if err != nil || strings.Contains(string(hj), "command -v sy") || !strings.Contains(string(hj), "[ -x ") {
		t.Fatalf("%v hooks not rewritten", err)
	}
	// every embedded file is present
	err = fs.WalkDir(plugin.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if _, e := os.Stat(filepath.Join(pd, filepath.FromSlash(p))); e != nil {
				t.Errorf("missing %s", p)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// a second write replaces the tree and leaves no temp or old folders behind
	if err := os.WriteFile(filepath.Join(dir, "stale.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.WriteMarketplace(dir, sy, "0.4.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.txt")); err == nil {
		t.Fatal("old tree survived")
	}
	b, _ = os.ReadFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	if !strings.Contains(string(b), `"0.4.0"`) {
		t.Fatal("not replaced")
	}
	ents, _ := os.ReadDir(filepath.Dir(dir))
	if len(ents) != 1 {
		t.Fatalf("leftovers next to the marketplace: %v", ents)
	}
}

func TestWriteMarketplaceRefusals(t *testing.T) {
	in := New(claudecli.NewFake())
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		bad := filepath.Join(root, "a:b", "marketplace")
		err := in.WriteMarketplace(bad, "/x/sy", "1")
		if !errors.Is(err, ErrPathSeparator) {
			t.Fatalf("got %v", err)
		}
		if _, e := os.Stat(filepath.Join(root, "a:b")); e == nil {
			t.Fatal("nothing may be written for a refused path")
		}
	} else if err := in.WriteMarketplace(filepath.Join(root, "a;b", "m"), `C:\sy.exe`, "1"); !errors.Is(err, ErrPathSeparator) {
		t.Fatalf("got %v", err)
	}
	if err := in.WriteMarketplace("relative/dir", "/x/sy", "1"); err == nil {
		t.Fatal("relative dir accepted")
	}
	if err := in.WriteMarketplace(filepath.Join(root, "m"), "sy", "1"); err == nil {
		t.Fatal("relative sy path accepted")
	}
}

func TestInstallerFlows(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	mkt := filepath.Join(state, "plugin", "marketplace")
	fake := claudecli.NewFake()
	in := New(fake)
	if err := in.WriteMarketplace(mkt, filepath.Join(state, "sy"), "0.3.0"); err != nil {
		t.Fatal(err)
	}
	owner := claudecli.Folder{Dir: "/h/.claude", Unset: true}
	linked := claudecli.Folder{Dir: "/h/.claude-2"}
	ownSettings := claudecli.Folder{Dir: "/h/.claude-3"}
	fake.LinkRoot(linked.Dir, owner.Dir)
	fake.LinkSettings(linked.Dir, owner.Dir)
	fake.LinkRoot(ownSettings.Dir, owner.Dir)

	if err := in.Install(ctx, owner, mkt); err != nil {
		t.Fatal(err)
	}
	if got := fake.Installed(owner.Dir); len(got) != 1 || got[0] != PluginID {
		t.Fatalf("installed %v", got)
	}
	if !fake.Enabled(linked.Dir, PluginID) {
		t.Fatal("shared settings must see enabledPlugins")
	}
	if fake.Enabled(ownSettings.Dir, PluginID) {
		t.Fatal("own settings must not be enabled by the owner's install")
	}
	if err := in.Enable(ctx, ownSettings); err != nil || !fake.Enabled(ownSettings.Dir, PluginID) {
		t.Fatalf("enable: %v", err)
	}
	// installing again (second run) updates the known marketplace instead of adding it
	if err := in.Install(ctx, owner, mkt); err != nil {
		t.Fatal(err)
	}
	if len(fake.CallsTo("PluginMarketplaceAdd")) != 1 || len(fake.CallsTo("PluginMarketplaceUpdate")) != 1 {
		t.Fatalf("calls: %+v", fake.Calls())
	}
	// every call ran as the folder it was given
	for _, c := range fake.CallsTo("PluginInstall") {
		if c.Folder != owner || c.Args[0] != PluginID {
			t.Fatalf("%+v", c)
		}
	}
	// a duplicate from another marketplace is disabled by id
	fake.PluginMarketplaceAdd(ctx, owner, mkt)
	if err := in.Disable(ctx, ownSettings, PluginID); err != nil || fake.Enabled(ownSettings.Dir, PluginID) {
		t.Fatalf("disable: %v", err)
	}
	if err := in.Disable(ctx, ownSettings, ""); err == nil {
		t.Fatal("empty id accepted")
	}

	// uninstall once per root, from any folder of it; repeating is fine
	if err := in.Uninstall(ctx, linked); err != nil {
		t.Fatal(err)
	}
	if len(fake.Installed(owner.Dir)) != 0 || len(fake.Marketplaces(owner.Dir)) != 0 {
		t.Fatal("not removed")
	}
	if err := in.Uninstall(ctx, owner); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestInstallerErrorsAreReturned(t *testing.T) {
	ctx := context.Background()
	f := claudecli.Folder{Dir: "/h/.claude"}
	boom := errors.New("boom")
	for _, m := range []string{"PluginMarketplaceAdd", "PluginInstall"} {
		fake := claudecli.NewFake()
		fake.FailOn(m, boom)
		err := New(fake).Install(ctx, f, "/state/marketplace")
		if !errors.Is(err, boom) {
			t.Errorf("%s: got %v", m, err)
		}
	}
	fake := claudecli.NewFake()
	fake.FailOn("PluginEnable", boom)
	if err := New(fake).Enable(ctx, f); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

// TestValidateStrict runs `claude plugin validate --strict` on the generated folder. It runs
// only with SY_E2E_REAL_CLAUDE=1 (and SY_E2E_REAL_CLAUDE_BIN or claude on PATH), in a
// throwaway HOME so no real ~/.claude* folder is touched.
func TestValidateStrict(t *testing.T) {
	if os.Getenv("SY_E2E_REAL_CLAUDE") != "1" {
		t.Skip("set SY_E2E_REAL_CLAUDE=1 to run `claude plugin validate --strict`")
	}
	bin := os.Getenv("SY_E2E_REAL_CLAUDE_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("claude"); err != nil {
			t.Skip("claude not found")
		}
	}
	home := t.TempDir()
	dir := filepath.Join(home, "state", "plugin", "marketplace")
	if err := New(claudecli.NewFake()).WriteMarketplace(dir, filepath.Join(home, "sy"), "0.3.0"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{dir, PluginDir(dir)} {
		cmd := exec.Command(bin, "plugin", "validate", "--strict", target)
		cmd.Env = []string{"HOME=" + home, "USERPROFILE=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"),
			"PATH=" + os.Getenv("PATH"), "TERM=dumb", "DISABLE_AUTOUPDATER=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("validate --strict %s: %v\n%s", target, err, out)
		}
	}
}

// alreadyRunner answers every plugin command the way Claude Code 2.1.296 does when the goal
// already holds: outcome "failed", failureCode "already_in_goal_state", alreadyInGoalState
// true (Exec turns that into a nil error). The installer must treat all of it as success.
type alreadyRunner struct {
	*claudecli.Fake
}

func already(cmd string) claudecli.Result {
	return claudecli.Result{Outcome: "failed", Command: cmd, FailureCode: "already_in_goal_state",
		AlreadyInGoalState: true, Message: "already"}
}

func (alreadyRunner) PluginMarketplaceAdd(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return already("marketplace-add"), nil
}
func (alreadyRunner) PluginMarketplaceRemove(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return already("marketplace-remove"), nil
}
func (alreadyRunner) PluginMarketplaceUpdate(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return already("marketplace-update"), nil
}
func (alreadyRunner) PluginInstall(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return already("install"), nil
}
func (alreadyRunner) PluginUninstall(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return already("uninstall"), nil
}
func (alreadyRunner) PluginEnable(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return already("enable"), nil
}
func (alreadyRunner) PluginDisable(context.Context, claudecli.Folder, string) (claudecli.Result, error) {
	return already("disable"), nil
}

func TestInstallerTreatsAlreadyInGoalStateAsSuccess(t *testing.T) {
	ctx := context.Background()
	f := claudecli.Folder{Dir: "/h/.claude"}
	mktDir := filepath.Join(t.TempDir(), "sy-local") // the fake names a marketplace after its folder
	base := claudecli.NewFake()
	// the marketplace and plugin are listed so Uninstall reaches its remove calls
	if _, err := base.PluginMarketplaceAdd(ctx, f, mktDir); err != nil {
		t.Fatal(err)
	}
	if _, err := base.PluginInstall(ctx, f, PluginID); err != nil {
		t.Fatal(err)
	}
	in := New(alreadyRunner{base})
	if err := in.Install(ctx, f, mktDir); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := in.Enable(ctx, f); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := in.Disable(ctx, f, PluginID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := in.Uninstall(ctx, f); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	// a plain failed outcome is still an error
	bad := claudecli.Result{Outcome: "failed", FailureCode: "not_found", Message: "nope"}
	if err := check(bad, nil, "x"); err == nil {
		t.Fatal("a failed outcome without alreadyInGoalState must be an error")
	}
}
