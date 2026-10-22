package share

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

// managedAll lists every copy path copies.json registers (what the ledger lists after setup).
func managedAll(t *testing.T, state string) []string {
	t.Helper()
	gs, err := LoadCopies(state)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range gs {
		out = append(out, g.Paths[1:]...)
	}
	return out
}

// Regression: after an undo restored a folder's own file where a synced
// copy was, the next sync must not copy that file into the shared primary. The ledger no
// longer lists the path as a copy, so SyncCopies leaves it alone.
func TestSyncCopiesSkipsPathsTheLedgerDropped(t *testing.T) {
	b := testtree.New(t, "")
	state := b.Abs("state")
	b.File("p/settings.json", `{"primary":true}`, 0o644).File("f/settings.json", `{"folder-original":true}`, 0o644)
	s := New(state)
	if err := s.ReplaceWithLink(b.Abs("f/settings.json"), b.Abs("p/settings.json"), model.LinkCopy, b.Abs("saved")); err != nil {
		t.Fatal(err)
	}
	// undo puts the folder's original back (here without deregistering: an older sy)
	if err := os.Remove(b.Abs("f/settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(b.Abs("saved"), b.Abs("f/settings.json")); err != nil {
		t.Fatal(err)
	}
	r, err := SyncCopies(state, nil) // the ledger lists no copies any more
	if err != nil || len(r.Synced) != 0 || len(r.Unmanaged) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if got := readF(t, b.Abs("p/settings.json")); got != `{"primary":true}` {
		t.Fatalf("primary overwritten: %s", got)
	}
	if got := readF(t, b.Abs("f/settings.json")); got != `{"folder-original":true}` {
		t.Fatalf("folder file overwritten: %s", got)
	}
}

func TestForgetCopyDeregisters(t *testing.T) {
	b := testtree.New(t, "")
	state := b.Abs("state")
	b.File("p/settings.json", "v1", 0o600)
	s := New(state)
	for _, f := range []string{"f", "g"} {
		if err := s.Link(b.Abs(f+"/settings.json"), b.Abs("p/settings.json"), model.LinkCopy); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ForgetCopy(b.Abs("f/settings.json")); err != nil {
		t.Fatal(err)
	}
	gs, _ := LoadCopies(state)
	if len(gs) != 1 || len(gs[0].Paths) != 2 || gs[0].Paths[1] != b.Abs("g/settings.json") {
		t.Fatalf("%+v", gs)
	}
	if err := s.ForgetCopy(b.Abs("g/settings.json")); err != nil {
		t.Fatal(err)
	}
	if gs, _ := LoadCopies(state); len(gs) != 0 {
		t.Fatalf("group with no copy kept: %+v", gs)
	}
	// forgetting an unknown path is a no-op
	if err := s.ForgetCopy(b.Abs("nope")); err != nil {
		t.Fatal(err)
	}
}

// Regression: one group failing to write must not stop the others, and the failed group
// keeps its old hash so the next pass retries it.
func TestSyncCopiesContinuesAfterAWriteError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a read-only folder")
	}
	b := testtree.New(t, "")
	state := b.Abs("state")
	b.File("p/a.json", "a1", 0o600).File("p/b.json", "b1", 0o600)
	s := New(state)
	if err := s.Link(b.Abs("f/a.json"), b.Abs("p/a.json"), model.LinkCopy); err != nil {
		t.Fatal(err)
	}
	if err := s.Link(b.Abs("g/b.json"), b.Abs("p/b.json"), model.LinkCopy); err != nil {
		t.Fatal(err)
	}
	before, _ := LoadCopies(state)
	_ = os.WriteFile(b.Abs("p/a.json"), []byte("a2"), 0o600)
	_ = os.WriteFile(b.Abs("p/b.json"), []byte("b2"), 0o600)
	// f/a.json cannot be replaced: its folder is read-only
	if err := os.Chmod(b.Abs("f"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(b.Abs("f"), 0o700) })
	r, err := SyncCopies(state, managedAll(t, state))
	if err == nil || len(r.Errors) != 1 {
		t.Fatalf("want one group error: %+v %v", r, err)
	}
	if got := readF(t, b.Abs("g/b.json")); got != "b2" {
		t.Fatalf("the other group was not synced: %q", got)
	}
	after, _ := LoadCopies(state)
	if after[0].Name != "a.json" || after[0].SHA256 != before[0].SHA256 {
		t.Fatalf("the failed group's hash moved on, it would never be retried: %+v", after)
	}
}

// Regression (lower finding): unsharing a folder link failed on Linux/macOS (a folder cannot
// be renamed over a symlink), and Unshare must never register a synced copy.
func TestUnshareFolderAndFileLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	b := testtree.New(t, "")
	state := b.Abs("state")
	b.File("p/skills/a/SKILL.md", "x", 0o644).File("p/CLAUDE.md", "md", 0o644)
	b.Symlink("f/skills", b.Abs("p/skills")).Symlink("f/CLAUDE.md", b.Abs("p/CLAUDE.md"))
	s := New(state)
	for _, n := range []string{"skills", "CLAUDE.md"} {
		if err := s.Unshare(b.Abs("f/" + n)); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		fi, err := os.Lstat(b.Abs("f/" + n))
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s still a link", n)
		}
	}
	if readF(t, b.Abs("f/skills/a/SKILL.md")) != "x" || readF(t, b.Abs("f/CLAUDE.md")) != "md" {
		t.Fatal("copy content")
	}
	if gs, _ := LoadCopies(state); len(gs) != 0 {
		t.Fatalf("unshare registered a synced copy: %+v", gs)
	}
	if err := s.Unshare(b.Abs("f/CLAUDE.md")); err == nil {
		t.Fatal("unshare of a non-link must fail")
	}
}

// Regression (lower finding): Link with LinkCopy of a folder over an existing link.
func TestLinkCopyFolderOverLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	b := testtree.New(t, "")
	b.File("p/skills/a/SKILL.md", "x", 0o644)
	b.Symlink("f/skills", b.Abs("p/skills"))
	if err := New("").Link(b.Abs("f/skills"), b.Abs("p/skills"), model.LinkCopy); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(b.Abs("f/skills")); err != nil || !fi.IsDir() {
		t.Fatalf("not a folder copy: %v", err)
	}
	if readF(t, filepath.Join(b.Abs("f/skills"), "a", "SKILL.md")) != "x" {
		t.Fatal("copy content")
	}
}
