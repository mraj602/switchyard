package plugin

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

// The pane mod is embedded on its own: its manifest names it, its hooks.json lists only
// modules (no settings-style hooks, so it never records anything), and the switchyard tree
// does not contain it.
func TestPaneEmbed(t *testing.T) {
	fsys := PaneFS()
	b, err := fs.ReadFile(fsys, ".claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pj struct {
		Name, Version, Types string
	}
	if err := json.Unmarshal(b, &pj); err != nil || pj.Name != PaneName || pj.Version == "" {
		t.Fatalf("plugin.json = %+v (%v)", pj, err)
	}
	h, err := fs.ReadFile(fsys, "hooks/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(h, &hooks); err != nil {
		t.Fatal(err)
	}
	var mods []string
	if len(hooks) != 1 || json.Unmarshal(hooks["modules"], &mods) != nil || len(mods) != 1 || mods[0] != "./register.tsx" {
		t.Fatalf("hooks.json must be exactly {\"modules\": [\"./register.tsx\"]}, got %s", h)
	}
	reg, err := fs.ReadFile(fsys, "hooks/register.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reg), `"__SY_PATH__"`) {
		t.Error("register.tsx has no __SY_PATH__ placeholder for pluginmkt to rewrite")
	}
	if pj.Types != "" {
		if _, err := fs.Stat(fsys, strings.TrimPrefix(pj.Types, "./")); err != nil {
			t.Errorf("plugin.json types %q: %v", pj.Types, err)
		}
	}
	if _, err := fs.Stat(FS(), PaneName); err == nil {
		t.Error("the switchyard tree contains the pane")
	}
}
