package testtree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sample(t *testing.T) *Builder {
	t.Helper()
	b := New(t, "")
	b.Dir(".claude", 0o755).
		File(".claude/settings.json", `{"a":1}`, 0o644).
		File(".claude/secret", "s", 0o600).
		Dir(".claude/skills/one", 0o755).
		File(".claude/skills/one/SKILL.md", "# one", 0o644).
		Symlink(".claude-2/settings.json", "$ROOT/.claude/settings.json"). // absolute
		Symlink(".claude-2/skills", "../.claude/skills").                  // relative
		Symlink(".claude-2/dangling", "/nonexistent/target").
		Dir(".claude-3", 0o700).
		File("empty", "", 0o644)
	return b
}

func TestBuilderAndSnapshot(t *testing.T) {
	b := sample(t)
	s := Take(t, b.Root, Opts{})
	if n := s[".claude-2/skills"]; n.Type != "symlink" || n.Target != "../.claude/skills" {
		t.Fatalf("relative link: %+v", n)
	}
	if n := s[".claude-2/settings.json"]; n.Target != filepath.Join(b.Root, ".claude/settings.json") {
		t.Fatalf("absolute link: %+v", n)
	}
	if n := s[".claude-2/dangling"]; n.Type != "symlink" || n.Target != "/nonexistent/target" {
		t.Fatalf("dangling link: %+v", n)
	}
	if n := s[".claude/settings.json"]; n.Type != "file" || n.MtimeNS != Epoch.UnixNano() || n.Size != 7 {
		t.Fatalf("file: %+v", n)
	}
	if runtime.GOOS != "windows" {
		if s[".claude/secret"].Mode != 0o600 || s[".claude-3"].Mode != 0o700 {
			t.Fatalf("modes: %v %v", s[".claude/secret"].Mode, s[".claude-3"].Mode)
		}
	}
	if s["empty"].Type != "file" || s["empty"].SHA256 == "" {
		t.Fatalf("empty file: %+v", s["empty"])
	}
	if s["."].Type != "dir" {
		t.Fatal("root missing")
	}
	// Taking twice gives the same snapshot (nothing reads changes anything).
	Equal(t, s, Take(t, b.Root, Opts{}))
	if !strings.Contains(s.String(), ".claude-2/skills -> ../.claude/skills") {
		t.Fatalf("String: %s", s)
	}
}

func TestRoundTripThroughJSONAndMaterialize(t *testing.T) {
	b := sample(t)
	opts := Opts{KeepContent: true, DirMtimes: true}
	s := Take(t, b.Root, opts)

	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Snap
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	Equal(t, s, back)

	dst := t.TempDir()
	Materialize(t, dst, back)
	got := Take(t, dst, opts)
	// Absolute links point into the old root; the raw string must still be reproduced exactly.
	Equal(t, s, got)
	if tgt, _ := os.Readlink(filepath.Join(dst, ".claude-2/skills")); tgt != "../.claude/skills" {
		t.Fatalf("relative link came back as %q", tgt)
	}
}

func TestDiffDetectsEveryKindOfChange(t *testing.T) {
	mutations := map[string]func(b *Builder){
		"content": func(b *Builder) { _ = os.WriteFile(b.Abs(".claude/settings.json"), []byte(`{"a":2}`), 0o644) },
		"mtime": func(b *Builder) {
			_ = os.Chtimes(b.Abs(".claude/skills/one/SKILL.md"), time.Now(), time.Now())
		},
		"mode": func(b *Builder) { _ = os.Chmod(b.Abs(".claude/secret"), 0o644) },
		"target": func(b *Builder) { // same place, spelled differently
			_ = os.Remove(b.Abs(".claude-2/skills"))
			_ = os.Symlink(b.Abs(".claude/skills"), b.Abs(".claude-2/skills"))
		},
		"removed": func(b *Builder) { _ = os.Remove(b.Abs("empty")) },
		"added":   func(b *Builder) { _ = os.WriteFile(b.Abs("new"), nil, 0o644) },
		"type": func(b *Builder) {
			_ = os.Remove(b.Abs(".claude-2/dangling"))
			_ = os.Mkdir(b.Abs(".claude-2/dangling"), 0o755)
		},
	}
	if runtime.GOOS == "windows" {
		delete(mutations, "mode")
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := sample(t)
			before := Take(t, b.Root, Opts{})
			mutate(b)
			if d := Diff(before, Take(t, b.Root, Opts{})); len(d) == 0 {
				t.Fatal("change not detected")
			}
		})
	}
}

func TestIgnore(t *testing.T) {
	b := sample(t)
	s := Take(t, b.Root, Opts{Ignore: func(rel string) bool { return rel == ".claude-2" || rel == "empty" }})
	for p := range s {
		if strings.HasPrefix(p, ".claude-2") || p == "empty" {
			t.Errorf("%s not ignored", p)
		}
	}
	if _, ok := s[".claude/settings.json"]; !ok {
		t.Fatal("over-ignored")
	}
}
