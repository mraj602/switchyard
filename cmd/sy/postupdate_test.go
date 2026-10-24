package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/brand"
	"github.com/mraj602/switchyard/internal/claudecli"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/pluginmkt"
	"github.com/mraj602/switchyard/internal/update"
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
	t.Setenv("SY_HOME", filepath.Join(root, "sy"))
	t.Setenv("SY_CONFIG_DIR", "")
	t.Setenv("SY_STATE_DIR", "")
	t.Setenv("SY_DATA_DIR", "")
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	f := claudecli.NewFake()
	e := &setupEnv{home: home, dirs: d, sy: "/usr/local/bin/sy", goos: "linux",
		getenv: func(string) string { return "" }, runner: f}
	old := postUpdateEnv
	postUpdateEnv = func() (*setupEnv, error) { return e, nil }
	defer func() { postUpdateEnv = old }()
	ctx := context.Background()

	// No ledger: nothing to do.
	if s, err := postUpdatePlugin(ctx, "0.4.0"); err != nil || !strings.Contains(s, "no sy-local") {
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
		if err := pluginmkt.New(f).WriteMarketplace(mkt, e.sy, "0.4.0"); err != nil {
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
	if s, err := postUpdatePlugin(ctx, "0.4.0"); err != nil || !strings.Contains(s, "no sy-local") {
		t.Errorf("adopted: %q %v", s, err)
	}
}

// rename.md 2.4: `sy update` to a renamed release uninstalls the plugin and pane of the old
// name in every plugin root it installed and installs the current ones.
func TestPostUpdatePluginMovesLegacyPlugin(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	defer brand.SetLegacyCLIForTest(brand.CLI)() // the old plugins sit in the current marketplace
	legacy := brand.LegacyPluginIDs()
	root := t.TempDir()
	t.Setenv("SY_HOME", filepath.Join(root, "sy"))
	t.Setenv("SY_CONFIG_DIR", "")
	t.Setenv("SY_STATE_DIR", "")
	t.Setenv("SY_DATA_DIR", "")
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	f := claudecli.NewFake()
	e := &setupEnv{home: home, dirs: d, sy: "/usr/local/bin/sy", goos: "linux",
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
	if err := pluginmkt.New(f).WriteMarketplace(mkt, e.sy, "0.9.0"); err != nil {
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
