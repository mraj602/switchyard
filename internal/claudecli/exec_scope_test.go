package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project- or local-scope uninstall passes --scope and runs in the project folder; the
// user scope is the plain command.
func TestExecPluginUninstallScoped(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	f := r.folder(".claude")
	proj := t.TempDir()
	if _, err := r.x.PluginUninstallScoped(ctx, f, "watchyard@wy-local", "project", proj); err != nil {
		t.Fatal(err)
	}
	if _, err := r.x.PluginUninstallScoped(ctx, f, "watchyard@wy-local", "user", ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	last2 := lines[len(lines)-2:]
	if !strings.Contains(last2[0], `"plugin","uninstall","watchyard@wy-local","--scope=project","--json"`) {
		t.Errorf("project call: %s", last2[0])
	}
	if strings.Contains(last2[1], "--scope") {
		t.Errorf("user call: %s", last2[1])
	}
	// The working directory is the project's: a folder that is gone cannot be entered.
	if _, err := r.x.PluginUninstallScoped(ctx, f, "watchyard@wy-local", "local", filepath.Join(proj, "gone")); err == nil {
		t.Error("ran without the project folder")
	}
}
