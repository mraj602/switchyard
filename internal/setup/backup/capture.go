package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mraj602/switchyard/internal/setup/model"
)

// Capture records what is at path without following a final symlink: type, permission
// bits, size, mtime and SHA-256 of a regular file, the raw readlink string of a link.
// A missing path (or a missing parent) is type absent, not an error.
func Capture(path string) (model.PathState, error) { return capture(path, false) }

// CaptureMeta is Capture without reading the file's bytes (no hash): used for a
// `.claude.json`, whose content other than `mcpServers` sy never reads (rule 0.3).
func CaptureMeta(path string) (model.PathState, error) { return capture(path, true) }

// IsStateFile reports whether path is a Claude Code state file (`.claude.json`), which is
// never saved whole and never hashed.
func IsStateFile(path string) bool {
	b := filepath.Base(path)
	return b == ".claude.json" || strings.HasPrefix(b, ".claude.json.")
}

func capture(path string, meta bool) (model.PathState, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return model.PathState{Type: model.TypeAbsent}, nil
		}
		return model.PathState{}, err
	}
	st := model.PathState{Mode: model.ModeString(fi.Mode())}
	switch t := linkKind(path, fi); {
	case t != "":
		st.Type = t
		st.Mode = ""
		if st.Target, err = os.Readlink(path); err != nil {
			return model.PathState{}, err
		}
	case fi.IsDir():
		st.Type = model.TypeDir
	case fi.Mode().IsRegular():
		st.Type = model.TypeFile
		st.Size = fi.Size()
		st.MtimeNS = fi.ModTime().UnixNano()
		if !meta {
			if st.SHA256, err = hashFile(path); err != nil {
				return model.PathState{}, err
			}
		}
	default:
		return model.PathState{}, fmt.Errorf("backup: %s is neither file, folder nor link (%v)", path, fi.Mode())
	}
	return st, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SHA256Hex returns the hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// StateEq reports whether two states describe the same thing: type, link target, and for
// files size and hash, for folders the tree hash, and permission bits where both are
// recorded (not on Windows). mtime is not compared. When either side lacks a hash (metadata
// capture, a folder Capture does not hash) that part is not compared.
func StateEq(a, b model.PathState) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case model.TypeAbsent:
		return true
	case model.TypeSymlink, model.TypeJunction:
		return a.Target == b.Target
	case model.TypeFile:
		if a.Size != b.Size {
			return false
		}
		if a.SHA256 != "" && b.SHA256 != "" && a.SHA256 != b.SHA256 {
			return false
		}
	case model.TypeDir:
		// A folder's SHA256, when both sides have one, is its TreeHash.
		if a.SHA256 != "" && b.SHA256 != "" && a.SHA256 != b.SHA256 {
			return false
		}
	}
	if runtime.GOOS != "windows" && a.Mode != "" && b.Mode != "" && a.Mode != b.Mode {
		return false
	}
	return true
}

// ReadPointer returns the raw JSON value (compacted) at an RFC 6901 pointer in file,
// following symlinks. A missing file or key is present=false with no error. It reads the
// whole file, so callers use it only on files they may read (settings, the mcpServers key
// of a state file).
func ReadPointer(file, pointer string) (raw json.RawMessage, present bool, err error) {
	b, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return PointerIn(b, pointer)
}

// PointerIn is ReadPointer on a document already in memory.
func PointerIn(doc []byte, pointer string) (json.RawMessage, bool, error) {
	cur := json.RawMessage(bytes.TrimSpace(doc))
	if !json.Valid(cur) {
		return nil, false, errors.New("invalid JSON")
	}
	if pointer != "" {
		if !strings.HasPrefix(pointer, "/") {
			return nil, false, fmt.Errorf("bad JSON pointer %q", pointer)
		}
		for _, tok := range strings.Split(pointer[1:], "/") {
			tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
			var obj map[string]json.RawMessage
			if json.Unmarshal(cur, &obj) == nil && obj != nil {
				v, ok := obj[tok]
				if !ok {
					return nil, false, nil
				}
				cur = v
				continue
			}
			var arr []json.RawMessage
			if json.Unmarshal(cur, &arr) == nil && arr != nil {
				var i int
				if _, err := fmt.Sscanf(tok, "%d", &i); err != nil || i < 0 || i >= len(arr) {
					return nil, false, nil
				}
				cur = arr[i]
				continue
			}
			return nil, false, nil
		}
	}
	var out bytes.Buffer
	if err := json.Compact(&out, cur); err != nil {
		return nil, false, err
	}
	return out.Bytes(), true, nil
}

// copyFile copies src to dst (created with mode, truncated).
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTree copies a folder tree: modes, file mtimes and raw link targets preserved.
func copyTree(src, dst string) error {
	type dirFix struct {
		path string
		mode os.FileMode
		mt   time.Time
	}
	var fixes []dirFix
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(t, to)
		case fi.IsDir():
			if err := os.Mkdir(to, 0o700); err != nil {
				return err
			}
			fixes = append(fixes, dirFix{to, fi.Mode().Perm(), fi.ModTime()})
			return nil
		case fi.Mode().IsRegular():
			if err := copyFile(p, to, fi.Mode().Perm()); err != nil {
				return err
			}
			if err := os.Chmod(to, fi.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(to, fi.ModTime(), fi.ModTime())
		}
		return fmt.Errorf("backup: cannot copy %s (%v)", p, fi.Mode())
	})
	if err != nil {
		return err
	}
	for i := len(fixes) - 1; i >= 0; i-- {
		f := fixes[i]
		if err := os.Chmod(f.path, f.mode); err != nil {
			return err
		}
		if err := os.Chtimes(f.path, f.mt, f.mt); err != nil {
			return err
		}
	}
	return nil
}

// MoveTree moves a file, link or folder: rename, or copy then delete across file systems.
// to must not exist.
func MoveTree(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("backup: move %s: %s exists", from, to)
	}
	err := os.Rename(from, to)
	if err == nil {
		return nil
	}
	if _, serr := os.Lstat(from); serr != nil {
		return err
	}
	fi, _ := os.Lstat(from)
	if fi.IsDir() {
		if err := copyTree(from, to); err != nil {
			_ = os.RemoveAll(to)
			return err
		}
		return os.RemoveAll(from)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(from)
		if err != nil {
			return err
		}
		if err := os.Symlink(t, to); err != nil {
			return err
		}
		return os.Remove(from)
	}
	if err := copyFile(from, to, fi.Mode().Perm()); err != nil {
		_ = os.Remove(to)
		return err
	}
	_ = os.Chmod(to, fi.Mode().Perm())
	_ = os.Chtimes(to, fi.ModTime(), fi.ModTime())
	return os.Remove(from)
}

// TreeHash is a deterministic hash of a folder tree (names, types, modes, file hashes,
// link targets) for comparing two trees (setup.md 5.3).
func TreeHash(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		st, err := Capture(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\n", filepath.ToSlash(rel), st.Type, st.Mode, st.SHA256, st.Target)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}
