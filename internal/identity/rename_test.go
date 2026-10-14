package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameMachine(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadOrCreateMachine(dir, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	old, got, err := RenameMachine(dir, "  gpu-box  ", MachineOptions{})
	if err != nil || got.ID != m.ID || got.Name != "gpu-box" || old.Name != m.Name {
		t.Fatalf("rename: %+v %+v %v", old, got, err)
	}
	again, err := LoadOrCreateMachine(dir, MachineOptions{})
	if err != nil || again != got {
		t.Fatalf("reload: %+v %v", again, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, machineFile))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	for _, bad := range []string{"", "   ", "a\x07b", strings.Repeat("x", 61)} {
		if _, _, err := RenameMachine(dir, bad, MachineOptions{}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if _, _, err := RenameMachine(dir, strings.Repeat("é", 60), MachineOptions{}); err != nil {
		t.Errorf("60 characters refused: %v", err)
	}
	// A fresh state folder gets an id first, then the name.
	fresh := t.TempDir()
	_, n, err := RenameMachine(fresh, "srv", MachineOptions{})
	if err != nil || n.Name != "srv" || !strings.HasPrefix(n.ID, "m_") {
		t.Fatalf("fresh: %+v %v", n, err)
	}
}
