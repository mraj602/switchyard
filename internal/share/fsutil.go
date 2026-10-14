package share

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/mraj602/switchyard/internal/identity"
)

// ErrForbidden is returned for a path through a credentials file (identity.Forbidden).
var ErrForbidden = identity.ErrForbidden

func guard(paths ...string) error {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if identity.Forbidden(p) {
			return fmt.Errorf("%w: %s", ErrForbidden, p)
		}
	}
	return nil
}

func randHex() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// tempName is a transient sibling name for path: ".<base>.sy.<rand>.tmp".
func tempName(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".sy."+randHex()+".tmp")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// FilesEqual compares two regular files byte for byte, stopping at the first difference.
func FilesEqual(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	ia, err := fa.Stat()
	if err != nil {
		return false, err
	}
	ib, err := fb.Stat()
	if err != nil {
		return false, err
	}
	if ia.Size() != ib.Size() {
		return false, nil
	}
	ra, rb := bufio.NewReaderSize(fa, 64<<10), bufio.NewReaderSize(fb, 64<<10)
	bufA, bufB := make([]byte, 32<<10), make([]byte, 32<<10)
	for {
		na, ea := io.ReadFull(ra, bufA)
		nb, eb := io.ReadFull(rb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		if ea != nil || eb != nil {
			if isEOF(ea) && isEOF(eb) {
				return true, nil
			}
			if isEOF(ea) != isEOF(eb) {
				return false, nil
			}
			if !isEOF(ea) {
				return false, ea
			}
			return false, eb
		}
	}
}

func isEOF(err error) bool { return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) }

// TreesEqual compares two paths the way 5.3 defines "identical": same type, same permission
// bits (not on Windows), same link target, same bytes for files, and for directories the
// same names with identical entries. It is lazy: the first difference ends it, and file
// contents are only read when the sizes match.
func TreesEqual(a, b string) (bool, error) {
	ia, err := os.Lstat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Lstat(b)
	if err != nil {
		return false, err
	}
	if ia.Mode().Type() != ib.Mode().Type() {
		return false, nil
	}
	if runtime.GOOS != "windows" && ia.Mode().Type() != os.ModeSymlink && ia.Mode().Perm() != ib.Mode().Perm() {
		return false, nil
	}
	switch {
	case ia.Mode()&os.ModeSymlink != 0:
		ta, err := os.Readlink(a)
		if err != nil {
			return false, err
		}
		tb, err := os.Readlink(b)
		if err != nil {
			return false, err
		}
		return ta == tb, nil
	case ia.IsDir():
		ea, err := readDirNames(a)
		if err != nil {
			return false, err
		}
		eb, err := readDirNames(b)
		if err != nil {
			return false, err
		}
		if len(ea) != len(eb) {
			return false, nil
		}
		for i := range ea {
			if ea[i] != eb[i] {
				return false, nil
			}
		}
		for _, n := range ea {
			eq, err := TreesEqual(filepath.Join(a, n), filepath.Join(b, n))
			if err != nil || !eq {
				return false, err
			}
		}
		return true, nil
	case ia.Mode().IsRegular():
		return FilesEqual(a, b)
	}
	return false, nil // devices, sockets: never "identical"
}

func readDirNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(ents))
	for i, e := range ents {
		names[i] = e.Name()
	}
	sort.Strings(names)
	return names, nil
}

// copyPath copies a file, link or tree, keeping modes and mtimes and raw link targets.
func copyPath(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		t, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(t, dst)
	case fi.IsDir():
		if err := os.Mkdir(dst, fi.Mode().Perm()|0o700); err != nil {
			return err
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		if err := os.Chmod(dst, fi.Mode().Perm()); err != nil && runtime.GOOS != "windows" {
			return err
		}
		return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	case fi.Mode().IsRegular():
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Chmod(fi.Mode().Perm()); err != nil && runtime.GOOS != "windows" {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	}
	return fmt.Errorf("share: cannot copy %s (unsupported file type)", src)
}

// moveAny renames from to to; across file systems it copies then deletes. to must not exist.
func moveAny(from, to string) error {
	if exists(to) {
		return fmt.Errorf("share: move target exists: %s", to)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	err := os.Rename(from, to)
	if err == nil {
		return nil
	}
	if !crossDevice(err) {
		return err
	}
	if err := copyPath(from, to); err != nil {
		_ = os.RemoveAll(to)
		return err
	}
	return os.RemoveAll(from)
}

// removeAny deletes a path; a link is removed itself, never followed.
func removeAny(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		if _, _, isLink := linkInfo(p); isLink {
			return os.Remove(p)
		}
	}
	return os.RemoveAll(p)
}
