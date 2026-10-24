package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
)

const evilLabel = "x/../../evil\ntouch /tmp/pwned"

// `wy config set slots.labels.<folder>` refuses a label that
// is not `^[a-z0-9][a-z0-9-]{0,23}$` and leaves config.toml as it was.
func TestConfigSetRejectsInvalidLabel(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	s.mustSy(t, "config", "set", "slots.labels.~/.claude-2", "work")
	cfg := filepath.Join(s.syHome, "config", "config.toml")
	before, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{evilLabel, "x/../../evil", "Work", ""} {
		out, errOut, code := s.wy(t, "", "config", "set", "slots.labels.~/.claude-2", l)
		if code == 0 {
			t.Errorf("config set label %q: exit 0\n%s%s", l, out, errOut)
		}
	}
	after, _ := os.ReadFile(cfg)
	if !bytes.Equal(before, after) {
		t.Errorf("config.toml changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

// Regression: an invalid label put into config.toml by hand yields no launcher outside the
// launcher dir and no injected line in any launcher; setup falls back to the default label.
func TestHandEditedInvalidLabelNoLauncherEscape(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	s := newSetupHome(t, h)
	cfg := "version = 1\n[slots.labels]\n\"~/.claude-2\" = " + tomlString(evilLabel) + "\n"
	if err := os.MkdirAll(filepath.Join(s.syHome, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.syHome, "config", "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := s.wy(t, "", "setup", "--yes", "--hub", "later", "--named-launchers")
	if code != 0 {
		t.Fatalf("setup: exit %d\n%s\n%s", code, out, errOut)
	}
	// Nothing named "evil" anywhere near the home (the traversal target is its parent).
	for _, root := range []string{filepath.Dir(h.Root), s.syHome} {
		ents, _ := os.ReadDir(root)
		for _, e := range ents {
			if strings.Contains(e.Name(), "evil") {
				t.Errorf("file escaped the launcher dir: %s", filepath.Join(root, e.Name()))
			}
		}
	}
	bin := h.Abs(".local/bin")
	launchers := 0
	_ = filepath.WalkDir(h.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(p, "evil") {
			t.Errorf("file with the label in its path: %s", p)
		}
		if filepath.Dir(p) == bin && strings.HasPrefix(d.Name(), "claude-") {
			launchers++
			b, _ := os.ReadFile(p)
			for _, l := range strings.Split(string(b), "\n") {
				if strings.Contains(l, "touch") || strings.Contains(l, "evil") {
					t.Errorf("%s: injected line %q", p, l)
				}
			}
		}
		return nil
	})
	if launchers == 0 {
		t.Errorf("no launcher written for ~/.claude-2\n%s", out)
	}
	if b, _ := os.ReadFile(h.Abs(".local/bin/claude-2")); !bytes.Contains(b, []byte("label=claude-2\n")) {
		t.Errorf("claude-2 launcher should use the default label:\n%s", b)
	}
}

func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
