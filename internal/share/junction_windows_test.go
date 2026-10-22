//go:build windows

package share

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

// Run by the Windows CI job: junction create, detect by reparse tag, replace
// of a real directory, snapshot type.
func TestJunctionRoundTrip(t *testing.T) {
	b := testtree.New(t, "")
	b.File("p/skills/a.md", "A", 0o644).File("f/skills/a.md", "A", 0o644)
	s := New("")
	if err := s.ReplaceWithLink(b.Abs("f/skills"), b.Abs("p/skills"), model.LinkJunction, b.Abs("save/skills")); err != nil {
		t.Fatal(err)
	}
	raw, kind, ok := ReadLink(b.Abs("f/skills"))
	if !ok || kind != model.LinkJunction || filepath.Clean(raw) != filepath.Clean(b.Abs("p/skills")) {
		t.Fatalf("raw=%q kind=%q ok=%v", raw, kind, ok)
	}
	if got, err := os.ReadFile(b.Abs("f/skills/a.md")); err != nil || string(got) != "A" {
		t.Fatalf("read through junction: %q %v", got, err)
	}
	st, err := s.Snapshot(b.Abs("f/skills"))
	if err != nil || st.Type != model.TypeJunction {
		t.Fatalf("%+v %v", st, err)
	}
	v, _ := Classify(b.Abs("p/skills"), b.Abs("f/skills"))
	if v != VerdictAdopted {
		t.Fatalf("verdict %v", v)
	}
	if err := removeAny(b.Abs("f/skills")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.Abs("p/skills/a.md")); err != nil {
		t.Fatal("removing the junction removed the target's content")
	}
}
