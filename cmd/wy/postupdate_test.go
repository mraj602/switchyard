package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if got := strings.Join(names, ","); got != "plugin,timer,hub service,agent service,machine_info,doctor" {
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
