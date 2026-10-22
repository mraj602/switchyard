package pluginmkt

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/claudecli"
	"github.com/mraj602/switchyard/plugin"
)

// The marketplace also carries switchyard-pane. The switchyard plugin's
// written tree is byte-identical to what it was before the pane existed (written alone, with
// an empty pane source), and the pane tree is the embedded one with only its version set.
func TestMarketplaceCarriesPaneAndLeavesSwitchyardAlone(t *testing.T) {
	state := t.TempDir()
	sy := filepath.Join(state, "bin", "sy")
	both := filepath.Join(state, "both", "marketplace")
	if err := New(claudecli.NewFake()).WriteMarketplace(both, sy, "0.7.0"); err != nil {
		t.Fatal(err)
	}
	alone := filepath.Join(state, "alone", "marketplace")
	empty := os.DirFS(t.TempDir())
	if err := (&Installer{Runner: claudecli.NewFake(), PaneSource: empty}).WriteMarketplace(alone, sy, "0.7.0"); err != nil {
		t.Fatal(err)
	}
	if a, b := treeFiles(t, PluginDir(both)), treeFiles(t, PluginDir(alone)); !sameFiles(a, b) {
		t.Fatal("the switchyard plugin tree changed when the pane was added")
	}

	pane := treeFiles(t, PaneDir(both))
	err := fs.WalkDir(plugin.PaneFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, _ := fs.ReadFile(plugin.PaneFS(), p)
		got, ok := pane[p]
		if !ok {
			t.Errorf("pane file %s missing", p)
			return nil
		}
		if p == ".claude-plugin/plugin.json" {
			want = setVersion(want, "0.7.0")
		}
		if p != "hooks/register.tsx" && !bytes.Equal(got, want) {
			t.Errorf("pane file %s differs from the embedded one", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pane[".claude-plugin/plugin.json"]), `"version": "0.7.0"`) {
		t.Error("pane plugin.json version not set to sy's version")
	}
	if PaneID != "switchyard-pane@sy-local" {
		t.Errorf("PaneID = %s", PaneID)
	}
	mj, _ := os.ReadFile(filepath.Join(both, ".claude-plugin", "marketplace.json"))
	if !strings.Contains(string(mj), "Read only; it runs only the sy command.") {
		t.Errorf("pane description not taken from its plugin.json:\n%s", mj)
	}
}

func treeFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}
