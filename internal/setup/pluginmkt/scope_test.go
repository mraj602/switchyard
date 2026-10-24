package pluginmkt

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/claudecli"
)

// rename.md 2.4: an install of the legacy plugin made from a project (Claude Code treats the
// home folder as one) is only uninstalled with its own scope and project folder; the default
// user scope is refused.

func legacyID(t *testing.T) string {
	t.Helper()
	ids := brand.LegacyPluginIDs()
	if ids == nil {
		t.Skip("no rename in this build")
	}
	return ids[0]
}

// market adds the marketplace of id's plugin to the fake (it names one after its folder).
func market(t *testing.T, fake *claudecli.Fake, f claudecli.Folder, id string) {
	t.Helper()
	name := id[len(id)-len("wy-local"):]
	if _, err := fake.PluginMarketplaceAdd(context.Background(), f, filepath.Join(t.TempDir(), name)); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveLegacyUsesEachInstallsScope(t *testing.T) {
	ctx := context.Background()
	id := legacyID(t)
	f := claudecli.Folder{Dir: "/h/.claude", Unset: true}
	fake := claudecli.NewFake()
	market(t, fake, f, id)
	if _, err := fake.PluginInstall(ctx, f, id); err != nil { // user scope
		t.Fatal(err)
	}
	fake.AddScopedInstall(f.Dir, id, "project", "/h")
	fake.AddScopedInstall(f.Dir, id, "local", "/h/work")
	fake.AddScopedInstall(f.Dir, "other@elsewhere", "project", "/h") // not ours: stays

	ids, err := New(fake).RemoveLegacy(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{PluginID}) {
		t.Errorf("ids = %v, want one current id", ids)
	}
	var got [][]string
	for _, c := range fake.CallsTo("PluginUninstallScoped") {
		got = append(got, c.Args)
	}
	want := [][]string{{id, "project", "/h"}, {id, "local", "/h/work"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped uninstalls %v, want %v", got, want)
	}
	if left := fake.ScopedInstalls(f.Dir); len(left) != 1 || left[0].ID != "other@elsewhere" {
		t.Errorf("installs left: %+v", left)
	}
	if n := len(fake.Installed(f.Dir)); n != 0 {
		t.Errorf("user-scope install left: %v", fake.Installed(f.Dir))
	}
}

// Only a project-scope install: the user-scope uninstall must not even be tried.
func TestRemoveLegacyProjectScopeOnly(t *testing.T) {
	ctx := context.Background()
	id := legacyID(t)
	f := claudecli.Folder{Dir: "/h/.claude", Unset: true}
	fake := claudecli.NewFake()
	fake.AddScopedInstall(f.Dir, id, "project", "/h")
	if _, err := New(fake).RemoveLegacy(ctx, f); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.CallsTo("PluginUninstall")); n != 0 {
		t.Errorf("%d user-scope uninstalls", n)
	}
	if len(fake.ScopedInstalls(f.Dir)) != 0 {
		t.Error("project install left")
	}
}

// The fake refuses a user-scope uninstall of a project-scope install, as the real CLI does.
func TestFakeRefusesWrongScope(t *testing.T) {
	fake := claudecli.NewFake()
	f := claudecli.Folder{Dir: "/h/.claude"}
	fake.AddScopedInstall(f.Dir, "x@y", "project", "/h")
	if _, err := fake.PluginUninstall(context.Background(), f, "x@y"); err == nil {
		t.Fatal("user-scope uninstall of a project install succeeded")
	}
}

// `wy setup --undo`: every install of the current plugin goes, whichever scope it is in.
func TestUninstallRemovesEveryScope(t *testing.T) {
	ctx := context.Background()
	f := claudecli.Folder{Dir: "/h/.claude", Unset: true}
	fake := claudecli.NewFake()
	market(t, fake, f, PluginID)
	if _, err := fake.PluginInstall(ctx, f, PluginID); err != nil {
		t.Fatal(err)
	}
	fake.AddScopedInstall(f.Dir, PluginID, "project", "/h")
	if err := New(fake).Uninstall(ctx, f); err != nil {
		t.Fatal(err)
	}
	if len(fake.Installed(f.Dir)) != 0 || len(fake.ScopedInstalls(f.Dir)) != 0 {
		t.Errorf("left: %v %+v", fake.Installed(f.Dir), fake.ScopedInstalls(f.Dir))
	}
}

// MissingCurrent counts a current plugin in any scope, and a legacy one in any scope, as
// present: the move is not repeated and nothing is installed twice.
func TestMissingCurrentAnyScope(t *testing.T) {
	ctx := context.Background()
	id := legacyID(t)
	f := claudecli.Folder{Dir: "/h/.claude", Unset: true}
	fake := claudecli.NewFake()
	in := New(fake)
	if ids, err := in.MissingCurrent(ctx, f); err != nil || len(ids) != 1 {
		t.Fatalf("empty root: %v %v", ids, err)
	}
	fake.AddScopedInstall(f.Dir, PluginID, "project", "/h")
	if ids, err := in.MissingCurrent(ctx, f); err != nil || ids != nil {
		t.Errorf("current in project scope: %v %v", ids, err)
	}
	fake2 := claudecli.NewFake()
	fake2.AddScopedInstall(f.Dir, id, "local", "/h")
	if ids, err := New(fake2).MissingCurrent(ctx, f); err != nil || ids != nil {
		t.Errorf("legacy in local scope: %v %v", ids, err)
	}
}
