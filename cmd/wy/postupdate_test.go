package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/pluginmkt"
	"github.com/mraj602/watchyard/internal/update"
)

func TestPostUpdateStepsAreRegisteredInOrder(t *testing.T) {
	var names []string
	for _, s := range update.PostUpdateSteps() {
		// "probe" is registered (and never removed) by the update command test in this package;
		// with -count>1 it is already there when this test runs again.
		if s.Name == "probe" {
			continue
		}
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "command,plugin,timer,hub service,agent service,machine_info,doctor" {
		t.Errorf("post-update steps: %s", got)
	}
}

func TestPostUpdatePluginRefreshesEachRootAsItsOwner(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WY_HOME", filepath.Join(root, "wy"))
	t.Setenv("WY_CONFIG_DIR", "")
	t.Setenv("WY_STATE_DIR", "")
	t.Setenv("WY_DATA_DIR", "")
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	f := claudecli.NewFake()
	e := &setupEnv{home: home, dirs: d, wy: "/usr/local/bin/wy", goos: "linux",
		getenv: func(string) string { return "" }, runner: f}
	old := postUpdateEnv
	postUpdateEnv = func() (*setupEnv, error) { return e, nil }
	defer func() { postUpdateEnv = old }()
	ctx := context.Background()

	// No ledger: nothing to do.
	if s, err := postUpdatePlugin(ctx, "0.4.0"); err != nil || !strings.Contains(s, "no wy-local") {
		t.Fatalf("no ledger: %q %v", s, err)
	}

	mkt := filepath.Join(d.State.Path, "plugin", "marketplace")
	claudeDir := filepath.Join(home, ".claude")
	other := filepath.Join(home, ".claude-2")
	for _, dir := range []string{claudeDir, other} {
		fo := claudecli.Folder{Dir: dir, Unset: dir == claudeDir}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := pluginmkt.New(f).WriteMarketplace(mkt, e.wy, "0.4.0"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.PluginMarketplaceAdd(ctx, fo, mkt); err != nil {
			t.Fatal(err)
		}
		if _, err := f.PluginInstall(ctx, fo, pluginmkt.PluginID); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.Write(d.State.Path, model.Ledger{Plugin: &model.LedgerPlugin{Marketplace: mkt, ID: pluginmkt.PluginID,
		Roots: []string{filepath.Join(claudeDir, "plugins"), filepath.Join(other, "plugins")}}}); err != nil {
		t.Fatal(err)
	}
	s, err := postUpdatePlugin(ctx, "0.4.0")
	if err != nil {
		t.Fatalf("plugin step: %v", err)
	}
	if !strings.Contains(s, "2 plugin root") {
		t.Errorf("detail: %q", s)
	}
	ups := f.CallsTo("PluginUpdate")
	if len(ups) != 2 || ups[0].Folder != (claudecli.Folder{Dir: claudeDir, Unset: true}) ||
		ups[1].Folder != (claudecli.Folder{Dir: other}) {
		t.Errorf("plugin update calls: %+v", ups)
	}
	if n := len(f.CallsTo("PluginMarketplaceUpdate")); n != 2 {
		t.Errorf("marketplace updates: %d", n)
	}

	// An adopted plugin is left alone.
	if err := ledger.Write(d.State.Path, model.Ledger{Plugin: &model.LedgerPlugin{Adopted: true}}); err != nil {
		t.Fatal(err)
	}
	if s, err := postUpdatePlugin(ctx, "0.4.0"); err != nil || !strings.Contains(s, "no wy-local") {
		t.Errorf("adopted: %q %v", s, err)
	}
}

// rename.md 2.4: `wy update` to a renamed release uninstalls the plugin and pane of the old
// name in every plugin root it installed and installs the current ones.
func TestPostUpdatePluginMovesLegacyPlugin(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	defer brand.SetLegacyCLIForTest(brand.CLI)() // the old plugins sit in the current marketplace
	legacy := brand.LegacyPluginIDs()
	root := t.TempDir()
	t.Setenv("WY_HOME", filepath.Join(root, "wy"))
	t.Setenv("WY_CONFIG_DIR", "")
	t.Setenv("WY_STATE_DIR", "")
	t.Setenv("WY_DATA_DIR", "")
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	f := claudecli.NewFake()
	e := &setupEnv{home: home, dirs: d, wy: "/usr/local/bin/wy", goos: "linux",
		getenv: func(string) string { return "" }, runner: f}
	old := postUpdateEnv
	postUpdateEnv = func() (*setupEnv, error) { return e, nil }
	defer func() { postUpdateEnv = old }()
	ctx := context.Background()

	mkt := filepath.Join(d.State.Path, "plugin", "marketplace")
	claudeDir := filepath.Join(home, ".claude")
	fo := claudecli.Folder{Dir: claudeDir, Unset: true}
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pluginmkt.New(f).WriteMarketplace(mkt, e.wy, "0.9.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PluginMarketplaceAdd(ctx, fo, mkt); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{legacy[0], legacy[1], "other@else"} {
		if _, err := f.PluginInstall(ctx, fo, id); err != nil && !strings.HasPrefix(id, "other") {
			t.Fatal(err)
		}
	}
	if err := ledger.Write(d.State.Path, model.Ledger{Plugin: &model.LedgerPlugin{Marketplace: mkt, ID: legacy[0],
		Roots: []string{filepath.Join(claudeDir, "plugins")}}}); err != nil {
		t.Fatal(err)
	}
	s, err := postUpdatePlugin(ctx, "1.0.0")
	if err != nil {
		t.Fatalf("plugin step: %v", err)
	}
	if !strings.Contains(s, "moved from "+legacy[0]+" in 1") {
		t.Errorf("detail: %q", s)
	}
	var ids []string
	list, _ := f.PluginList(ctx, fo)
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	want := []string{pluginmkt.PluginID, pluginmkt.PaneID}
	sort.Strings(want)
	if got, want := strings.Join(ids, ","), strings.Join(want, ","); got != want {
		t.Errorf("installed: %s, want %s", got, want)
	}
	if n := len(f.CallsTo("PluginUpdate")); n != 0 {
		t.Errorf("plugin update calls: %d", n)
	}
	led, err := ledger.Read(d.State.Path)
	if err != nil || led.Plugin.ID != pluginmkt.PluginID {
		t.Errorf("ledger plugin id: %+v %v", led.Plugin, err)
	}
	// The next update is an ordinary one.
	if s, err := postUpdatePlugin(ctx, "1.0.1"); err != nil || strings.Contains(s, "moved") {
		t.Errorf("second run: %q %v", s, err)
	}
}

// Legacy compat (rename.md 2.13): a unit written for sy sets SY_STATE_DIR; WY_STATE_DIR finds it.
func TestLegacyEnvLookupLiteral(t *testing.T) {
	m := map[string]string{"SY_STATE_DIR": "/old", "WY_HOME": "/h"} // rename:keep
	if got := legacyEnvLookup(m, "WY_STATE_DIR"); got != "/old" {
		t.Errorf("WY_STATE_DIR = %q", got)
	}
	if got := legacyEnvLookup(m, "WY_HOME"); got != "/h" {
		t.Errorf("WY_HOME = %q", got)
	}
	m["WY_STATE_DIR"] = "/new"
	if got := legacyEnvLookup(m, "WY_STATE_DIR"); got != "/new" {
		t.Errorf("both: %q", got)
	}
}

// rename.md 2.4: a move that uninstalled the old plugin and then failed to install the
// current one is finished by the next run (install, not update); a failed uninstall leaves the
// marketplace as it was.
func TestPostUpdatePluginRecoversHalfMove(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	defer brand.SetLegacyCLIForTest(brand.CLI)()
	legacy := brand.LegacyPluginIDs()
	root := t.TempDir()
	t.Setenv("WY_HOME", filepath.Join(root, "wy"))
	t.Setenv("WY_CONFIG_DIR", "")
	t.Setenv("WY_STATE_DIR", "")
	t.Setenv("WY_DATA_DIR", "")
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	f := claudecli.NewFake()
	e := &setupEnv{home: home, dirs: d, wy: "/usr/local/bin/wy", goos: "linux",
		getenv: func(string) string { return "" }, runner: f}
	old := postUpdateEnv
	postUpdateEnv = func() (*setupEnv, error) { return e, nil }
	defer func() { postUpdateEnv = old }()
	ctx := context.Background()
	mkt := filepath.Join(d.State.Path, "plugin", "marketplace")
	claudeDir := filepath.Join(home, ".claude")
	fo := claudecli.Folder{Dir: claudeDir, Unset: true}
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pluginmkt.New(f).WriteMarketplace(mkt, e.wy, "0.9.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PluginMarketplaceAdd(ctx, fo, mkt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PluginInstall(ctx, fo, legacy[0]); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Write(d.State.Path, model.Ledger{Plugin: &model.LedgerPlugin{Marketplace: mkt, ID: legacy[0],
		Roots: []string{filepath.Join(claudeDir, "plugins")}}}); err != nil {
		t.Fatal(err)
	}
	stamp := func() string {
		b, _ := os.ReadFile(filepath.Join(mkt, ".claude-plugin", "marketplace.json"))
		return string(b)
	}

	// The uninstall fails: nothing rewritten, an error that says to run again.
	before := stamp()
	f.FailOn("PluginUninstall", errors.New("boom"))
	if _, err := postUpdatePlugin(ctx, "1.0.0"); err == nil || !strings.Contains(err.Error(), "post-update") {
		t.Fatalf("failed uninstall: %v", err)
	}
	if stamp() != before {
		t.Error("marketplace rewritten after a failed uninstall")
	}
	f.FailOn("PluginUninstall", nil)

	// The uninstall works, the install fails: the old plugin is gone, the new one missing.
	f.FailOn("PluginInstall", errors.New("boom"))
	if _, err := postUpdatePlugin(ctx, "1.0.0"); err == nil {
		t.Fatal("install failure not reported")
	}
	f.FailOn("PluginInstall", nil)
	if got := f.Installed(claudeDir); len(got) != 0 {
		t.Fatalf("installed after the half move: %v", got)
	}
	// The next run installs it instead of updating a plugin that is not there.
	nUpd := len(f.CallsTo("PluginUpdate"))
	if s, err := postUpdatePlugin(ctx, "1.0.0"); err != nil || !strings.Contains(s, "moved") {
		t.Fatalf("recovery run: %q %v", s, err)
	}
	if got := f.Installed(claudeDir); len(got) != 1 || got[0] != "watchyard@wy-local" {
		t.Errorf("installed: %v", got)
	}
	if n := len(f.CallsTo("PluginUpdate")); n != nUpd {
		t.Errorf("plugin update called on a missing plugin")
	}
}

// rename.md 2.3: on Windows the task of the old name is not dropped silently: the summary
// carries the commands that replace it.
func TestPostUpdateTimerWindowsLegacyTask(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WY_HOME", filepath.Join(root, "wy"))
	t.Setenv("WY_CONFIG_DIR", "")
	t.Setenv("WY_STATE_DIR", "")
	t.Setenv("WY_DATA_DIR", "")
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		t.Fatal(err)
	}
	e := &setupEnv{home: filepath.Join(root, "home"), dirs: d, wy: `C:\wy\wy.exe`, goos: "windows",
		getenv: func(string) string { return "" }, runner: claudecli.NewFake()}
	old := postUpdateEnv
	postUpdateEnv = func() (*setupEnv, error) { return e, nil }
	defer func() { postUpdateEnv = old }()
	if s, err := postUpdateTimer(context.Background(), "1.0.0"); err != nil || s != "unchanged (windows)" {
		t.Fatalf("no legacy task: %q %v", s, err)
	}
	if err := os.MkdirAll(d.State.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyXML := filepath.Join(d.State.Path, "switchyard-flush.task.xml") // rename:keep
	if err := os.WriteFile(legacyXML, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := postUpdateTimer(context.Background(), "1.0.0")
	if err != nil || !strings.Contains(s, `schtasks /Delete /TN "Switchyard Flush" /F`) || !strings.Contains(s, `schtasks /Create /TN "Watchyard Flush"`) { // rename:keep
		t.Fatalf("legacy task: %q %v", s, err)
	}
	if _, err := os.Stat(legacyXML); !os.IsNotExist(err) {
		t.Errorf("legacy task file kept: %v", err)
	}
}
