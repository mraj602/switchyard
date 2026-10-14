package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sy tick removes title files last written over 7 days ago, and nothing else in <state>/term.
func TestTickPruneTitles(t *testing.T) {
	state := t.TempDir()
	if err := tickPruneTitles(state, time.Now()); err != nil {
		t.Fatalf("no term folder: %v", err)
	}
	dir := filepath.Join(state, "term")
	now := time.Now()
	files := map[string]time.Duration{
		"title-old.json":   8 * 24 * time.Hour,
		"title-new.json":   6 * 24 * time.Hour,
		".title-123":       8 * 24 * time.Hour,
		"other-old.json":   30 * 24 * time.Hour,
		"journal/pane.add": 30 * 24 * time.Hour,
	}
	for name, age := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tickPruneTitles(state, now); err != nil {
		t.Fatal(err)
	}
	for name, gone := range map[string]bool{"title-old.json": true, ".title-123": true, "title-new.json": false,
		"other-old.json": false, "journal/pane.add": false} {
		_, err := os.Stat(filepath.Join(dir, name))
		if gone != os.IsNotExist(err) {
			t.Errorf("%s: gone=%v, want %v", name, os.IsNotExist(err), gone)
		}
	}
}
