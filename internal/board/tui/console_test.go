package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// A handle that is not a console is left alone on every OS (on Windows the console file
// only changes real console handles, and restore puts back what it changed).
func TestEnableVTOnNonConsole(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	restore, err := EnableVT(f, f)
	if err != nil {
		t.Fatal(err)
	}
	restore()
	restore() // idempotent
	r, err := EnableVT(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r()
}
