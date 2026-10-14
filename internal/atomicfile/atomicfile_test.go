package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	d := t.TempDir()
	real := filepath.Join(d, "dotfiles", "config")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "config")
	if err := os.Symlink(filepath.Join("dotfiles", "config"), link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(real); string(b) != "new" {
		t.Fatalf("target holds %q", b)
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// A dangling link is created at its target.
	dang := filepath.Join(d, "ak")
	if err := os.Symlink(filepath.Join(d, "dotfiles", "ak"), dang); err != nil {
		t.Fatal(err)
	}
	if err := Write(dang, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(d, "dotfiles", "ak")); string(b) != "x" {
		t.Fatalf("dangling target holds %q", b)
	}
}
