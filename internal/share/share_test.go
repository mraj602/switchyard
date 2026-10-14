package share

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

func skipWin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink semantics")
	}
}

func readF(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestItemsTable(t *testing.T) {
	if len(Items) != len(model.ShareItems) {
		t.Fatal("table drifted from model")
	}
	for _, n := range []string{".credentials.json", ".claude.json", "sessions", "foo.lock", "unknown-thing"} {
		if !NeverShared(n) {
			t.Errorf("%s must be never-shared", n)
		}
	}
	if NeverShared("skills") || NeverShared("history.jsonl") {
		t.Error("table row marked never-shared")
	}
	d, _ := Lookup("skills")
	f, _ := Lookup("settings.json")
	if LinkKind(d, "windows", false) != model.LinkJunction || LinkKind(f, "windows", false) != model.LinkCopy ||
		LinkKind(f, "windows", true) != model.LinkSymlink || LinkKind(d, "linux", false) != model.LinkSymlink {
		t.Error("LinkKind")
	}
}

func TestLinkPrimitives(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("p/skills/a.md", "A", 0o644).File("f/CLAUDE.md", "old", 0o644).File("p/CLAUDE.md", "new", 0o644)
	s := New("")
	// absent -> link, absolute raw target
	if err := s.Link(b.Abs("f/skills"), b.Abs("p/skills"), model.LinkSymlink); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.Readlink(b.Abs("f/skills")); raw != b.Abs("p/skills") {
		t.Fatalf("raw %s", raw)
	}
	// existing file replaced atomically, no temp left
	if err := s.Link(b.Abs("f/CLAUDE.md"), b.Abs("p/CLAUDE.md"), model.LinkSymlink); err != nil {
		t.Fatal(err)
	}
	if readF(t, b.Abs("f/CLAUDE.md")) != "new" {
		t.Fatal("not linked")
	}
	ents, _ := os.ReadDir(b.Abs("f"))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatal("temp left", e.Name())
		}
	}
	// dangling target refused, relative target refused, real dir refused by Link
	if err := s.Link(b.Abs("f/x"), b.Abs("p/nope"), model.LinkSymlink); err == nil {
		t.Fatal("dangling accepted")
	}
	if err := s.Link(b.Abs("f/x"), "p/skills", model.LinkSymlink); err == nil {
		t.Fatal("relative accepted")
	}
	b.File("f/agents/x", "x", 0o644).File("p/agents/y", "y", 0o644)
	if err := s.Link(b.Abs("f/agents"), b.Abs("p/agents"), model.LinkSymlink); err == nil {
		t.Fatal("Link replaced a real dir")
	}
	// credentials refused
	b.File("p/.credentials.json", "{}", 0o600)
	if err := s.Link(b.Abs("f/.credentials.json"), b.Abs("p/.credentials.json"), model.LinkSymlink); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err %v", err)
	}
	if _, err := s.Snapshot(b.Abs("p/.credentials.json")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("snapshot err %v", err)
	}
}

func TestReplaceWithLinkDirSavesOriginal(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("p/skills/a.md", "A", 0o644).File("f/skills/a.md", "A", 0o644).File("f/skills/sub/b", "B", 0o600)
	before := testtree.Take(t, b.Abs("f/skills"), testtree.Opts{})
	s := New("")
	save := b.Abs("save/skills")
	if err := s.ReplaceWithLink(b.Abs("f/skills"), b.Abs("p/skills"), model.LinkSymlink, save); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(b.Abs("f/skills")); got != b.Abs("p/skills") {
		t.Fatal("not linked")
	}
	testtree.Equal(t, before, testtree.Take(t, save, testtree.Opts{}))
	// without saveTo the original is discarded
	b.File("g/skills/z", "z", 0o644)
	if err := s.ReplaceWithLink(b.Abs("g/skills"), b.Abs("p/skills"), model.LinkSymlink, ""); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(b.Abs("g"))
	if len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestReplaceWithLinkFileKeepsLinkAtAllTimes(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("p/settings.json", "P", 0o600).File("f/settings.json", "F", 0o600)
	s := New("")
	if err := s.ReplaceWithLink(b.Abs("f/settings.json"), b.Abs("p/settings.json"), model.LinkSymlink, b.Abs("save/settings.json")); err != nil {
		t.Fatal(err)
	}
	if readF(t, b.Abs("save/settings.json")) != "F" || readF(t, b.Abs("f/settings.json")) != "P" {
		t.Fatal("wrong content")
	}
}

func TestCrossDeviceFallbackTree(t *testing.T) {
	// copyPath is what moveAny falls back to on EXDEV: it must keep modes, mtimes, link targets.
	skipWin(t)
	b := testtree.New(t, "")
	b.File("a/x", "x", 0o640).File("a/d/y", "y", 0o600).Symlink("a/l", "relative/target").
		Mtime("a/x", testtree.Epoch.Add(5))
	if err := copyPath(b.Abs("a"), b.Abs("c")); err != nil {
		t.Fatal(err)
	}
	testtree.Equal(t, testtree.Take(t, b.Abs("a"), testtree.Opts{}), testtree.Take(t, b.Abs("c"), testtree.Opts{}))
	if !crossDevice(&os.LinkError{Err: syscall.EXDEV}) {
		t.Fatal("EXDEV not detected")
	}
}

func TestTreesEqual(t *testing.T) {
	b := testtree.New(t, "")
	b.File("a/x", "1", 0o644).File("a/d/y", "2", 0o644).File("b/x", "1", 0o644).File("b/d/y", "2", 0o644)
	if eq, err := TreesEqual(b.Abs("a"), b.Abs("b")); err != nil || !eq {
		t.Fatalf("%v %v", eq, err)
	}
	b.File("b/d/y", "3", 0o644)
	if eq, _ := TreesEqual(b.Abs("a"), b.Abs("b")); eq {
		t.Fatal("content difference missed")
	}
	b.File("b/d/y", "2", 0o644).File("b/extra", "", 0o644)
	if eq, _ := TreesEqual(b.Abs("a"), b.Abs("b")); eq {
		t.Fatal("extra entry missed")
	}
	if runtime.GOOS != "windows" {
		b.File("c/x", "1", 0o600).File("d/x", "1", 0o644)
		if eq, _ := TreesEqual(b.Abs("c"), b.Abs("d")); eq {
			t.Fatal("mode difference missed")
		}
	}
}

// ---- 5.3 first table ----

func TestClassifyRows(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("p/skills/a", "A", 0o644).File("p/CLAUDE.md", "P", 0o644)
	b.Symlink("f1/skills", b.Abs("p/skills"))
	b.Symlink("f2/skills", b.Abs("other/skills"))
	b.File("other/skills/q", "q", 0o644)
	b.File("f3/skills/a", "A", 0o644)
	b.File("f4/skills/a", "DIFFERENT", 0o644)
	b.Dir("f5", 0o755)
	cases := map[string]Verdict{"f1": VerdictAdopted, "f2": VerdictElsewhere, "f3": VerdictReplace, "f4": VerdictConflict, "f5": VerdictLink}
	for f, want := range cases {
		got, err := Classify(b.Abs("p/skills"), b.Abs(f+"/skills"))
		if err != nil || got != want {
			t.Errorf("%s: %v %v want %v", f, got, err, want)
		}
	}
	if v, _ := Classify(b.Abs("p/none"), b.Abs("f4/skills")); v != VerdictNoPrimary {
		t.Errorf("no primary: %v", v)
	}
	// a link row is untouched by classify; "elsewhere" is never re-pointed
	if raw, _ := os.Readlink(b.Abs("f2/skills")); raw != b.Abs("other/skills") {
		t.Error("link changed")
	}
}

// ---- 5.3 second table ----

func conflictFixture(t *testing.T) (*testtree.Builder, Item, Item) {
	b := testtree.New(t, "")
	b.File("p/CLAUDE.md", "primary", 0o644).File("f/CLAUDE.md", "folder", 0o644)
	b.File("p/skills/same", "S", 0o644).File("p/skills/diff", "P", 0o644).
		File("f/skills/same", "S", 0o644).File("f/skills/diff", "F", 0o644).File("f/skills/extra/x", "X", 0o644)
	md, _ := Lookup("CLAUDE.md")
	sk, _ := Lookup("skills")
	return b, md, sk
}

func TestResolveKeep(t *testing.T) {
	skipWin(t)
	b, md, sk := conflictFixture(t)
	before := testtree.Take(t, b.Root, testtree.Opts{})
	s := New("")
	for _, it := range []Item{md, sk} {
		r, err := s.Resolve(it, b.Abs("p/"+it.Name), b.Abs("f/"+it.Name), ResKeep, b.Abs("save"), model.LinkSymlink)
		if err != nil || !r.Separate || r.Linked {
			t.Fatalf("%+v %v", r, err)
		}
	}
	testtree.Equal(t, before, testtree.Take(t, b.Root, testtree.Opts{}))
}

func TestResolvePrimary(t *testing.T) {
	skipWin(t)
	b, md, sk := conflictFixture(t)
	s := New("")
	for _, it := range []Item{md, sk} {
		if _, err := s.Resolve(it, b.Abs("p/"+it.Name), b.Abs("f/"+it.Name), ResPrimary, b.Abs("save"), model.LinkSymlink); err != nil {
			t.Fatal(err)
		}
		if raw, _ := os.Readlink(b.Abs("f/" + it.Name)); raw != b.Abs("p/"+it.Name) {
			t.Fatalf("%s not linked", it.Name)
		}
	}
	if readF(t, b.Abs("save/CLAUDE.md")) != "folder" || readF(t, b.Abs("save/skills/diff")) != "F" {
		t.Fatal("originals not saved")
	}
	if readF(t, b.Abs("p/CLAUDE.md")) != "primary" {
		t.Fatal("primary changed")
	}
}

func TestResolveFolder(t *testing.T) {
	skipWin(t)
	b, md, _ := conflictFixture(t)
	s := New("")
	if _, err := s.Resolve(md, b.Abs("p/CLAUDE.md"), b.Abs("f/CLAUDE.md"), ResFolder, b.Abs("save"), model.LinkSymlink); err != nil {
		t.Fatal(err)
	}
	if readF(t, b.Abs("p/CLAUDE.md")) != "folder" || readF(t, b.Abs("save/primary-CLAUDE.md")) != "primary" {
		t.Fatal("contents wrong")
	}
	if raw, _ := os.Readlink(b.Abs("f/CLAUDE.md")); raw != b.Abs("p/CLAUDE.md") {
		t.Fatal("not linked")
	}
}

func TestResolveMergeFileRefused(t *testing.T) {
	b, md, _ := conflictFixture(t)
	_, err := New("").Resolve(md, b.Abs("p/CLAUDE.md"), b.Abs("f/CLAUDE.md"), ResMerge, b.Abs("save"), model.LinkSymlink)
	if !errors.Is(err, ErrMergeRefused) {
		t.Fatalf("err %v", err)
	}
}

func TestResolveMergeDirWithConflictNotLinked(t *testing.T) {
	skipWin(t)
	b, _, sk := conflictFixture(t)
	r, err := New("").Resolve(sk, b.Abs("p/skills"), b.Abs("f/skills"), ResMerge, b.Abs("save"), model.LinkSymlink)
	if err != nil {
		t.Fatal(err)
	}
	if r.Linked || !r.Separate || len(r.Conflicts) != 1 || r.Conflicts[0] != "diff" {
		t.Fatalf("%+v", r)
	}
	// extra moved into primary, identical dropped (saved), differing stays in F
	if readF(t, b.Abs("p/skills/extra/x")) != "X" {
		t.Fatal("extra not moved")
	}
	if _, err := os.Lstat(b.Abs("f/skills/same")); !os.IsNotExist(err) {
		t.Fatal("identical entry not dropped")
	}
	if readF(t, b.Abs("save/skills/same")) != "S" {
		t.Fatal("dropped entry not saved")
	}
	if readF(t, b.Abs("f/skills/diff")) != "F" || readF(t, b.Abs("p/skills/diff")) != "P" {
		t.Fatal("differing entry touched")
	}
	if fi, _ := os.Lstat(b.Abs("f/skills")); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("linked despite conflict")
	}
}

func TestResolveMergeDirClean(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("p/skills/a", "A", 0o644).File("f/skills/a", "A", 0o644).File("f/skills/new", "N", 0o644)
	sk, _ := Lookup("skills")
	r, err := New("").Resolve(sk, b.Abs("p/skills"), b.Abs("f/skills"), ResMerge, b.Abs("save"), model.LinkSymlink)
	if err != nil || !r.Linked {
		t.Fatalf("%+v %v", r, err)
	}
	if readF(t, b.Abs("p/skills/new")) != "N" {
		t.Fatal("not merged")
	}
	if raw, _ := os.Readlink(b.Abs("f/skills")); raw != b.Abs("p/skills") {
		t.Fatal("not linked")
	}
}

func TestResolveMergeLines(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("p/history.jsonl", `{"timestamp":300,"d":"c"}`+"\n"+`{"timestamp":100,"d":"a"}`+"\n", 0o600)
	b.File("f/history.jsonl", `{"timestamp":200,"d":"b"}`+"\n"+`{"timestamp":100,"d":"a"}`+"\nnotjson\n", 0o600)
	it, _ := Lookup("history.jsonl")
	r, err := New("").Resolve(it, b.Abs("p/history.jsonl"), b.Abs("f/history.jsonl"), ResMerge, b.Abs("save"), model.LinkSymlink)
	if err != nil || !r.Linked {
		t.Fatalf("%+v %v", r, err)
	}
	want := `{"timestamp":100,"d":"a"}` + "\n" + `{"timestamp":200,"d":"b"}` + "\n" + `{"timestamp":300,"d":"c"}` + "\nnotjson\n"
	if got := readF(t, b.Abs("p/history.jsonl")); got != want {
		t.Fatalf("got\n%s", got)
	}
	if !strings.Contains(readF(t, b.Abs("save/history.jsonl")), "notjson") {
		t.Fatal("folder copy not saved")
	}
	if fi, _ := os.Stat(b.Abs("p/history.jsonl")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatal("mode lost")
	}
}

func TestMergeLinesTimestampForms(t *testing.T) {
	out := string(MergeLinesBytes(
		[]byte("{\"timestamp\":\"2026-01-02T00:00:00Z\"}\n{\"x\":1}\n"),
		[]byte("{\"timestamp\":1000}\n{\"timestamp\":\"2025-01-01T00:00:00Z\"}\n{\"x\":1}\n{\"y\":2}\n")))
	want := "{\"timestamp\":1000}\n{\"timestamp\":\"2025-01-01T00:00:00Z\"}\n{\"timestamp\":\"2026-01-02T00:00:00Z\"}\n{\"x\":1}\n{\"y\":2}\n"
	// RFC3339 strings are millisecond epochs of ~1.7e12 and sort after 1000
	if out != want {
		t.Fatalf("got\n%s", out)
	}
}

func TestPromoteAndAgree(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("a/rules/r", "1", 0o644).File("c/rules/r", "1", 0o644).File("d/rules/r", "2", 0o644)
	if ok, err := AgreeAll([]string{b.Abs("a/rules"), b.Abs("c/rules")}); err != nil || !ok {
		t.Fatal("should agree")
	}
	if ok, _ := AgreeAll([]string{b.Abs("a/rules"), b.Abs("d/rules")}); ok {
		t.Fatal("should differ")
	}
	s := New("")
	if err := s.Promote(b.Abs("a/rules"), b.Abs("p/rules")); err != nil {
		t.Fatal(err)
	}
	if readF(t, b.Abs("p/rules/r")) != "1" {
		t.Fatal("not promoted")
	}
	if err := s.Promote(b.Abs("c/rules"), b.Abs("p/rules")); err == nil {
		t.Fatal("promote over existing")
	}
}

// ---- projects/ merge: renames only ----

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return inodeOf(fi)
}

func TestProjectsMergeIsRenamesOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("inode numbers")
	}
	root := t.TempDir()
	dst, src := filepath.Join(root, "p", "projects"), filepath.Join(root, "f", "projects")
	type f struct {
		path string
		ino  uint64
	}
	var tracked []f
	mk := func(base string, proj, n int, prefix string) {
		for i := 0; i < n; i++ {
			dir := filepath.Join(base, fmt.Sprintf("-home-u-proj%02d", proj))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, fmt.Sprintf("%s-%03d.jsonl", prefix, i))
			if err := os.WriteFile(p, []byte(fmt.Sprintf("session %s %d %d", prefix, proj, i)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 20 projects x 25 files in src; projects 0-9 also exist in dst (disjoint sessions),
	// project 10 has one identical session in both.
	for p := 0; p < 20; p++ {
		mk(src, p, 25, "f")
	}
	for p := 0; p < 10; p++ {
		mk(dst, p, 25, "p")
	}
	same := "same-session.jsonl"
	for _, b := range []string{dst, src} {
		_ = os.MkdirAll(filepath.Join(b, "-home-u-proj10"), 0o755)
		_ = os.WriteFile(filepath.Join(b, "-home-u-proj10", same), []byte("S"), 0o600)
	}
	// Track inodes of every src file that will be moved (all but the identical one).
	_ = filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() && fi.Name() != same {
			tracked = append(tracked, f{p, inodeOf(fi)})
		}
		return nil
	})
	if len(tracked) != 500 {
		t.Fatalf("fixture has %d files", len(tracked))
	}
	// Scale to 1000 files in src+dst to match the acceptance criterion.
	for p := 0; p < 10; p++ {
		mk(dst, p, 25, "q")
	}
	var dstTracked []f
	_ = filepath.Walk(dst, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			dstTracked = append(dstTracked, f{p, inodeOf(fi)})
		}
		return nil
	})
	save := filepath.Join(root, "save")
	res, err := New("").MergeDir(dst, src, save)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("conflicts %v", res.Conflicts)
	}
	if len(res.Dropped) != 1 || res.Dropped[0] != "-home-u-proj10/"+same {
		t.Fatalf("dropped %v", res.Dropped)
	}
	for _, tr := range tracked {
		rel, _ := filepath.Rel(src, tr.path)
		if got := inode(t, filepath.Join(dst, rel)); got != tr.ino {
			t.Fatalf("%s was copied, not renamed (inode %d -> %d)", rel, tr.ino, got)
		}
	}
	for _, tr := range dstTracked {
		if inode(t, tr.path) != tr.ino {
			t.Fatalf("%s: dst file touched", tr.path)
		}
	}
	// 10 whole project folders moved with one rename each
	if len(res.Moved) < 10 {
		t.Fatalf("moved %d", len(res.Moved))
	}
	if readF(t, filepath.Join(save, "-home-u-proj10", same)) != "S" {
		t.Fatal("dropped file not saved")
	}
}

func TestProjectsMergeSessionConflict(t *testing.T) {
	b := testtree.New(t, "")
	b.File("p/projects/x/s.jsonl", "A", 0o600).File("f/projects/x/s.jsonl", "B", 0o600).File("f/projects/x/t.jsonl", "T", 0o600)
	res, err := New("").MergeDir(b.Abs("p/projects"), b.Abs("f/projects"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "x/s.jsonl" || len(res.Moved) != 1 || res.Moved[0] != "x/t.jsonl" {
		t.Fatalf("%+v", res)
	}
}

// ---- JSON edits through the Sharer ----

func TestSharerJSONSetKeepsLinkAndMode(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("p/settings.json", "{\n  \"a\": 1\n}\n", 0o640)
	b.Symlink("f/settings.json", b.Abs("p/settings.json"))
	s := New("")
	r, err := s.JSONSet(b.Abs("f/settings.json"), "/env/CLAUDE_CODE_PLUGIN_CACHE_DIR", []byte(`"/x/plugins"`))
	if err != nil || !r.Changed || r.BeforePresent {
		t.Fatalf("%+v %v", r, err)
	}
	if rt, _ := filepath.EvalSymlinks(b.Abs("p/settings.json")); r.Target != rt {
		t.Fatalf("target %s", r.Target)
	}
	if raw, _ := os.Readlink(b.Abs("f/settings.json")); raw != b.Abs("p/settings.json") {
		t.Fatal("link changed")
	}
	if fi, _ := os.Stat(b.Abs("p/settings.json")); fi.Mode().Perm() != 0o640 {
		t.Fatal("mode")
	}
	r, err = s.JSONDelete(b.Abs("f/settings.json"), "/env/CLAUDE_CODE_PLUGIN_CACHE_DIR")
	if err != nil || !r.Changed || !r.BeforePresent || string(r.Before) != `"/x/plugins"` {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = s.JSONSet(b.Abs("f/settings.json"), "/a", []byte(`1`))
	if err != nil || r.Changed {
		t.Fatalf("idempotent set: %+v %v", r, err)
	}
}

func TestSharerRefusesClaudeJSONOutsideMCP(t *testing.T) {
	b := testtree.New(t, "")
	b.File("c/.claude.json", `{"oauthAccount":{"a":1},"mcpServers":{}}`, 0o600)
	s := New("")
	if _, err := s.JSONSet(b.Abs("c/.claude.json"), "/oauthAccount/a", []byte(`2`)); err == nil {
		t.Fatal("edited oauthAccount")
	}
	if _, err := s.JSONSet(b.Abs("c/.claude.json"), "/mcpServers/s", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshot(t *testing.T) {
	skipWin(t)
	b := testtree.New(t, "")
	b.File("a", "hello", 0o640).Symlink("l", "rel/target").Dir("d", 0o700)
	s := New("")
	st, _ := s.Snapshot(b.Abs("a"))
	if st.Type != model.TypeFile || st.Mode != "0640" || st.Size != 5 || len(st.SHA256) != 64 {
		t.Fatalf("%+v", st)
	}
	st, _ = s.Snapshot(b.Abs("l"))
	if st.Type != model.TypeSymlink || st.Target != "rel/target" {
		t.Fatalf("%+v", st)
	}
	st, _ = s.Snapshot(b.Abs("d"))
	if st.Type != model.TypeDir || st.Mode != "0700" {
		t.Fatalf("%+v", st)
	}
	st, _ = s.Snapshot(b.Abs("none"))
	if st.Type != model.TypeAbsent {
		t.Fatalf("%+v", st)
	}
}

func TestLinkCopyRegistersAndSyncs(t *testing.T) {
	b := testtree.New(t, "")
	state := b.Abs("state")
	b.File("p/settings.json", "v1", 0o600)
	s := New(state)
	if err := s.Link(b.Abs("f/settings.json"), b.Abs("p/settings.json"), model.LinkCopy); err != nil {
		t.Fatal(err)
	}
	if err := s.Link(b.Abs("g/settings.json"), b.Abs("p/settings.json"), model.LinkCopy); err != nil {
		t.Fatal(err)
	}
	g, _ := LoadCopies(state)
	if len(g) != 1 || len(g[0].Paths) != 3 {
		t.Fatalf("%+v", g)
	}
	r, err := SyncCopies(state, managedAll(t, state))
	if err != nil || len(r.Synced)+len(r.Conflicts) != 0 {
		t.Fatalf("unchanged: %+v %v", r, err)
	}
	// one side changed -> copied to the others
	_ = os.WriteFile(b.Abs("f/settings.json"), []byte("v2"), 0o600)
	r, err = SyncCopies(state, managedAll(t, state))
	if err != nil || len(r.Synced) != 1 || readF(t, b.Abs("p/settings.json")) != "v2" || readF(t, b.Abs("g/settings.json")) != "v2" {
		t.Fatalf("%+v %v", r, err)
	}
	// two sides changed differently -> nothing copied, conflict reported
	_ = os.WriteFile(b.Abs("f/settings.json"), []byte("v3"), 0o600)
	_ = os.WriteFile(b.Abs("g/settings.json"), []byte("v4"), 0o600)
	r, err = SyncCopies(state, managedAll(t, state))
	if err != nil || len(r.Conflicts) != 1 || readF(t, b.Abs("p/settings.json")) != "v2" || readF(t, b.Abs("f/settings.json")) != "v3" {
		t.Fatalf("%+v %v", r, err)
	}
	g, _ = LoadCopies(state)
	if !g[0].Conflict {
		t.Fatal("conflict not recorded")
	}
}

func TestCopiesHistoryLineUnion(t *testing.T) {
	b := testtree.New(t, "")
	state := b.Abs("state")
	b.File("p/history.jsonl", `{"timestamp":1}`+"\n", 0o600)
	s := New(state)
	if err := s.Link(b.Abs("f/history.jsonl"), b.Abs("p/history.jsonl"), model.LinkCopy); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(b.Abs("f/history.jsonl"), []byte(`{"timestamp":1}`+"\n"+`{"timestamp":3}`+"\n"), 0o600)
	_ = os.WriteFile(b.Abs("p/history.jsonl"), []byte(`{"timestamp":1}`+"\n"+`{"timestamp":2}`+"\n"), 0o600)
	r, err := SyncCopies(state, managedAll(t, state))
	if err != nil || len(r.Conflicts) != 0 || len(r.Synced) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	want := `{"timestamp":1}` + "\n" + `{"timestamp":2}` + "\n" + `{"timestamp":3}` + "\n"
	if readF(t, b.Abs("p/history.jsonl")) != want || readF(t, b.Abs("f/history.jsonl")) != want {
		t.Fatal("not unioned")
	}
}
