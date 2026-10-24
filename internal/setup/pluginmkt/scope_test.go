package pluginmkt

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mraj602/watchyard/internal/claudecli"
)

// market adds the marketplace of id's plugin to the fake (it names one after its folder).
func market(t *testing.T, fake *claudecli.Fake, f claudecli.Folder, id string) {
	t.Helper()
	name := id[len(id)-len("wy-local"):]
	if _, err := fake.PluginMarketplaceAdd(context.Background(), f, filepath.Join(t.TempDir(), name)); err != nil {
		t.Fatal(err)
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
