package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mraj602/switchyard/internal/record"
)

// `sy scan --slot X` must not stamp X on rows of a projects folder that X shares with
// another slot (a symlink): the sharing is decided over every slot, then filtered.
func TestScanSlotFlagKeepsSharedProjectsUnstamped(t *testing.T) {
	home, ob := fakeHome(t)
	other := filepath.Join(home, ".claude-2")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".claude", "projects"), filepath.Join(other, "projects")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if code, out, errb := runCmd(t, "scan", "--slot", "~/.claude"); code != 0 {
		t.Fatalf("scan: %d %s %s", code, out, errb)
	}
	n := 0
	for _, r := range outboxRecords(t, ob) {
		if r.Kind != record.KindTokenRows {
			continue
		}
		n++
		if r.Slot != "" {
			t.Errorf("token_rows stamped with slot %q although the projects folder is shared", r.Slot)
		}
	}
	if n == 0 {
		t.Fatal("no token_rows written")
	}
}
