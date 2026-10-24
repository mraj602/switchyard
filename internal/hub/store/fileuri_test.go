package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A data folder whose name holds URI characters still opens, exports and checks the right file.
func TestOpenPathWithURICharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("'?' is not valid in a Windows path")
	}
	dir := filepath.Join(t.TempDir(), "a b#c?d%e")
	s := openTest(t, dir)
	seedDevice(t, s)
	if _, err := os.Stat(filepath.Join(dir, "hub.db")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "x.wyx")
	if _, err := s.Export(context.Background(), ExportOptions{Out: out, NoFreeze: true}); err != nil {
		t.Fatal(err)
	}
	if v, err := ReadSchemaVersion(context.Background(), filepath.Join(dir, "hub.db")); err != nil || v != LatestVersion() {
		t.Fatalf("version %d, %v", v, err)
	}
}
