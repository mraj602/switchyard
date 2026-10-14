// Package testtree is the test helper of phase 3: it builds fake-home trees (files, folders,
// symlinks, modes, mtimes) in a temporary folder and snapshots a tree byte for byte, so apply
// followed by undo can be proven to give back exactly the same tree (setup.md 7.3 guarantee).
//
// A Snap records, per path relative to the root: type, permission bits (not for symlinks),
// size, SHA-256, file mtime (not for symlinks, whose mtime Go cannot set portably), and the
// raw symlink target (so a relative link must come back relative). Directory mtimes change
// whenever a child does, so they are compared only with Opts.DirMtimes.
//
// Only for tests; it calls t.Fatal on errors. Mode and symlink checks are meaningful on
// Unix; on Windows the package builds and round-trips but modes are not exact.
//
// Phase 3 wave 0. Depends on: nothing.
package testtree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Epoch is the mtime Builder gives every file it creates unless told otherwise: a fixed
// instant, so snapshots compare mtimes meaningfully and builds are reproducible.
var Epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// Builder creates a tree under Root. Paths are relative to Root with "/" separators (parent
// folders are created as needed with mode 0755 unless Dir was called for them).
type Builder struct {
	t    testing.TB
	Root string
}

// New returns a Builder for root; an empty root means t.TempDir().
func New(t testing.TB, root string) *Builder {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	return &Builder{t: t, Root: root}
}

// Abs returns the absolute path of rel under Root.
func (b *Builder) Abs(rel string) string { return filepath.Join(b.Root, filepath.FromSlash(rel)) }

func (b *Builder) mkparent(rel string) {
	b.t.Helper()
	if err := os.MkdirAll(filepath.Dir(b.Abs(rel)), 0o755); err != nil {
		b.t.Fatal(err)
	}
}

// Dir creates a folder (and parents) with the given permission bits and returns b.
func (b *Builder) Dir(rel string, mode os.FileMode) *Builder {
	b.t.Helper()
	b.mkparent(rel)
	p := b.Abs(rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		b.t.Fatal(err)
	}
	return b
}

// File writes a file with mode and the Epoch mtime and returns b.
func (b *Builder) File(rel, content string, mode os.FileMode) *Builder {
	b.t.Helper()
	b.mkparent(rel)
	p := b.Abs(rel)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		b.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // umask
		b.t.Fatal(err)
	}
	return b.Mtime(rel, Epoch)
}

// Symlink creates a symlink at rel with the raw target string (relative or absolute,
// dangling allowed) and returns b. Use "$ROOT/x" in target to mean Abs("x").
func (b *Builder) Symlink(rel, target string) *Builder {
	b.t.Helper()
	b.mkparent(rel)
	target = strings.ReplaceAll(target, "$ROOT", b.Root)
	if err := os.Symlink(target, b.Abs(rel)); err != nil {
		b.t.Fatal(err)
	}
	return b
}

// Mtime sets the modification time of a file or folder and returns b.
func (b *Builder) Mtime(rel string, t time.Time) *Builder {
	b.t.Helper()
	if err := os.Chtimes(b.Abs(rel), t, t); err != nil {
		b.t.Fatal(err)
	}
	return b
}

// Snap is a snapshot of a tree, keyed by slash-separated path relative to the root ("." is
// the root itself). It marshals to JSON.
type Snap map[string]Node

// Node is one path of a Snap.
type Node struct {
	Type    string      `json:"type"` // "file", "dir", "symlink", "other"
	Mode    os.FileMode `json:"mode,omitempty"`
	Size    int64       `json:"size,omitempty"`
	SHA256  string      `json:"sha256,omitempty"`
	MtimeNS int64       `json:"mtime_ns,omitempty"`
	Target  string      `json:"target,omitempty"`  // raw readlink string
	Content []byte      `json:"content,omitempty"` // only with Opts.KeepContent
}

// Opts tunes Take and Diff.
type Opts struct {
	// DirMtimes also records and compares folder mtimes (off by default: they move whenever a
	// child is created or removed).
	DirMtimes bool
	// NoMtime drops file mtimes from the comparison.
	NoMtime bool
	// KeepContent stores file bytes in the Snap so Materialize can rebuild the tree.
	KeepContent bool
	// Ignore skips a path (and everything under it) when it returns true; rel is slash-separated.
	Ignore func(rel string) bool
}

// Take snapshots the tree at root without following symlinks. It fails the test on I/O errors.
func Take(t testing.TB, root string, o Opts) Snap {
	t.Helper()
	s, err := TakeErr(root, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TakeErr is Take returning the error instead of failing a test.
func TakeErr(root string, o Opts) (Snap, error) {
	snap := Snap{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if o.Ignore != nil && rel != "." && o.Ignore(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		n := Node{}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			n.Type = "symlink"
			if n.Target, err = os.Readlink(p); err != nil {
				return err
			}
		case fi.IsDir():
			n.Type = "dir"
			n.Mode = fi.Mode().Perm()
			if o.DirMtimes {
				n.MtimeNS = fi.ModTime().UnixNano()
			}
		case fi.Mode().IsRegular():
			n.Type = "file"
			n.Mode = fi.Mode().Perm()
			n.Size = fi.Size()
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			n.SHA256 = hex.EncodeToString(sum[:])
			if !o.NoMtime {
				n.MtimeNS = fi.ModTime().UnixNano()
			}
			if o.KeepContent {
				n.Content = b
			}
		default:
			n.Type = "other"
			n.Mode = fi.Mode()
		}
		snap[rel] = n
		return nil
	})
	return snap, err
}

// Diff lists the differences between want and got, one line per path, sorted; empty means
// identical. Content bytes are not compared (the hash is).
func Diff(want, got Snap) []string {
	var out []string
	seen := map[string]bool{}
	for p, w := range want {
		seen[p] = true
		g, ok := got[p]
		if !ok {
			out = append(out, fmt.Sprintf("%s: missing (was %s)", p, w.Type))
			continue
		}
		if w.Type != g.Type {
			out = append(out, fmt.Sprintf("%s: type %s -> %s", p, w.Type, g.Type))
			continue
		}
		if w.Target != g.Target {
			out = append(out, fmt.Sprintf("%s: link target %q -> %q", p, w.Target, g.Target))
		}
		if w.Mode != g.Mode {
			out = append(out, fmt.Sprintf("%s: mode %v -> %v", p, w.Mode, g.Mode))
		}
		if w.SHA256 != g.SHA256 || w.Size != g.Size {
			out = append(out, fmt.Sprintf("%s: content changed (size %d -> %d)", p, w.Size, g.Size))
		}
		if w.MtimeNS != g.MtimeNS {
			out = append(out, fmt.Sprintf("%s: mtime %d -> %d", p, w.MtimeNS, g.MtimeNS))
		}
	}
	for p, g := range got {
		if !seen[p] {
			out = append(out, fmt.Sprintf("%s: unexpected %s", p, g.Type))
		}
	}
	sort.Strings(out)
	return out
}

// Equal fails the test with the differences when want and got differ.
func Equal(t testing.TB, want, got Snap) {
	t.Helper()
	if d := Diff(want, got); len(d) > 0 {
		t.Fatalf("trees differ:\n  %s", strings.Join(d, "\n  "))
	}
}

// Materialize rebuilds a tree from a Snap taken with KeepContent under root (which must
// exist), restoring modes, file mtimes and raw symlink targets. Folder mtimes are set last,
// deepest first, when the Snap has them.
func Materialize(t testing.TB, root string, s Snap) {
	t.Helper()
	paths := make([]string, 0, len(s))
	for p := range s {
		paths = append(paths, p)
	}
	sort.Strings(paths) // parents sort before children
	for _, p := range paths {
		n, abs := s[p], filepath.Join(root, filepath.FromSlash(p))
		switch n.Type {
		case "dir":
			if p != "." {
				if err := os.MkdirAll(abs, 0o755); err != nil {
					t.Fatal(err)
				}
			}
		case "file":
			if err := os.WriteFile(abs, n.Content, n.Mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(abs, n.Mode); err != nil {
				t.Fatal(err)
			}
			if n.MtimeNS != 0 {
				mt := time.Unix(0, n.MtimeNS)
				if err := os.Chtimes(abs, mt, mt); err != nil {
					t.Fatal(err)
				}
			}
		case "symlink":
			if err := os.Symlink(n.Target, abs); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("cannot materialize %s of type %s", p, n.Type)
		}
	}
	for i := len(paths) - 1; i >= 0; i-- { // deepest first: modes and mtimes of folders
		n := s[paths[i]]
		if n.Type != "dir" {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(paths[i]))
		if err := os.Chmod(abs, n.Mode); err != nil {
			t.Fatal(err)
		}
		if n.MtimeNS != 0 {
			mt := time.Unix(0, n.MtimeNS)
			if err := os.Chtimes(abs, mt, mt); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// String lists the snapshot one path per line, for failure messages.
func (s Snap) String() string {
	paths := make([]string, 0, len(s))
	for p := range s {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		n := s[p]
		switch n.Type {
		case "symlink":
			fmt.Fprintf(&b, "%s -> %s\n", p, n.Target)
		case "file":
			fmt.Fprintf(&b, "%s file %v %d %.8s\n", p, n.Mode, n.Size, n.SHA256)
		default:
			fmt.Fprintf(&b, "%s %s %v\n", p, n.Type, n.Mode)
		}
	}
	return b.String()
}
