package jsonedit

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, s string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const claudeDoc = "{\n  \"numStartups\": 3,\n  \"oauthAccount\": {\"emailAddress\": \"a@b.c\",  \"x\": 1.0},\n  \"mcpServers\": {}\n}\n"

func TestApplyKeepsModeAndOauth(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	write(t, p, claudeDoc, 0o600)
	res, err := Apply(p, Options{Claude: true, Folder: dir}, Op{Pointer: "/mcpServers", Value: []byte(`{"s":{"command":"c"}}`)})
	if err != nil || !res.Written {
		t.Fatalf("%+v %v", res, err)
	}
	got := read(t, p)
	if !strings.Contains(got, `"oauthAccount": {"emailAddress": "a@b.c",  "x": 1.0}`) || !strings.Contains(got, `"command": "c"`) {
		t.Fatalf("unexpected: %s", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(p + ".lock"); !os.IsNotExist(err) {
		t.Fatal("lock not released")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp left: %s", e.Name())
		}
	}
}

func TestApplyNoChangeNoWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	write(t, p, claudeDoc, 0o600)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(p, old, old)
	res, err := Apply(p, Options{}, Op{Pointer: "/mcpServers", Value: []byte(`{ }`)})
	if err != nil || res.Written || res.Changed() {
		t.Fatalf("%+v %v", res, err)
	}
	if fi, _ := os.Stat(p); !fi.ModTime().Equal(old) {
		t.Fatal("file rewritten")
	}
}

func TestStaleLockBroken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	write(t, p, claudeDoc, 0o600)
	if err := os.Mkdir(p+".lock", 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-11 * time.Second)
	if err := os.Chtimes(p+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, Options{Claude: true}, Op{Pointer: "/mcpServers/a", Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, p), `"a"`) {
		t.Fatal("edit missing")
	}
}

func TestFreshLockWaitsThenSkips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	write(t, p, claudeDoc, 0o600)
	if err := os.Mkdir(p+".lock", 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := Apply(p, Options{Claude: true, LockWait: 300 * time.Millisecond}, Op{Pointer: "/mcpServers/a", Value: []byte(`{}`)})
	var se *SkipError
	if !errors.Is(err, ErrSkipped) || !errors.As(err, &se) || se.Reason != ReasonLocked {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("did not wait")
	}
	if read(t, p) != claudeDoc {
		t.Fatal("file changed")
	}
	if _, err := os.Stat(p + ".lock"); err != nil {
		t.Fatal("someone else's lock must stay")
	}
}

func TestFreshLockReleasedDuringWait(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	write(t, p, claudeDoc, 0o600)
	_ = os.Mkdir(p+".lock", 0o755)
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.Remove(p + ".lock")
	}()
	if _, err := Apply(p, Options{Claude: true, LockWait: 3 * time.Second}, Op{Pointer: "/mcpServers/a", Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveSessionSkip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	write(t, p, claudeDoc, 0o600)
	sess := filepath.Join(dir, "sessions")
	_ = os.Mkdir(sess, 0o700)
	write(t, filepath.Join(sess, "123.json"), `{"pid": `+strconv.Itoa(os.Getpid())+`}`, 0o600)
	// a .key file must never be read; make it unreadable garbage
	write(t, filepath.Join(sess, "123.key"), `secret`, 0o000)
	op := Op{Pointer: "/mcpServers/a", Value: []byte(`{}`)}
	_, err := Apply(p, Options{Claude: true, Folder: dir}, op)
	var se *SkipError
	if !errors.As(err, &se) || se.Reason != ReasonLiveSession {
		t.Fatalf("err = %v", err)
	}
	if read(t, p) != claudeDoc {
		t.Fatal("file changed")
	}
	if _, err := Apply(p, Options{Claude: true, Folder: dir, IgnoreLive: true}, op); err != nil {
		t.Fatalf("now: %v", err)
	}
}

func TestDeadSessionDoesNotSkip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	write(t, p, claudeDoc, 0o600)
	_ = os.Mkdir(filepath.Join(dir, "sessions"), 0o700)
	write(t, filepath.Join(dir, "sessions", "x.json"), `{"pid": 2147483000}`, 0o600)
	if _, err := Apply(p, Options{Claude: true, Folder: dir}, Op{Pointer: "/mcpServers/a", Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkChainStaysLinked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	real := filepath.Join(root, "real")
	_ = os.Mkdir(real, 0o755)
	target := filepath.Join(real, "settings.json")
	write(t, target, "{\n  \"a\": 1\n}\n", 0o640)
	mid := filepath.Join(root, "mid.json")
	if err := os.Symlink(target, mid); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink("mid.json", link); err != nil { // relative link to a link
		t.Fatal(err)
	}
	res, err := Apply(link, Options{}, Op{Pointer: "/env/K", Value: []byte(`"v"`)})
	if err != nil || res.Target != mustEval(t, target) {
		t.Fatalf("%+v %v", res, err)
	}
	for _, l := range []struct{ p, raw string }{{link, "mid.json"}, {mid, target}} {
		fi, err := os.Lstat(l.p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s no longer a link", l.p)
		}
		if raw, _ := os.Readlink(l.p); raw != l.raw {
			t.Fatalf("%s now points to %s", l.p, raw)
		}
	}
	if !strings.Contains(read(t, link), `"K": "v"`) || !strings.Contains(read(t, target), `"K": "v"`) {
		t.Fatal("edit not at target")
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestVerifyRefusesOtherKeyChange(t *testing.T) {
	before := []byte(`{"oauthAccount":{"a":1},"mcpServers":{}}`)
	if err := Verify(before, []byte(`{"oauthAccount":{"a":2},"mcpServers":{"x":1}}`), []Op{{Pointer: "/mcpServers"}}); err == nil {
		t.Fatal("oauthAccount change accepted")
	}
	if err := Verify(before, []byte(`{"oauthAccount":{"a":1},"mcpServers":{"x":1}}`), []Op{{Pointer: "/mcpServers"}}); err != nil {
		t.Fatal(err)
	}
	if err := Verify(before, []byte(`{"oauthAccount":{"a":1}`), []Op{{Pointer: "/mcpServers"}}); err == nil {
		t.Fatal("invalid result accepted")
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Apply(filepath.Join(t.TempDir(), "nope.json"), Options{}, Op{Pointer: "/a", Value: []byte(`1`)})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}
