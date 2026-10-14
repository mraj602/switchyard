package detect

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/setup/model"
)

// hashFile is the SHA-256 (hex) of a file, opened through the forbidden-file guard.
func hashFile(fsys identity.FS, path string) (string, error) {
	f, err := fsys.Open(path)
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

// readDirSorted lists a folder without the files sy must never open.
func readDirSorted(fsys identity.FS, dir string) ([]fs.DirEntry, error) {
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := ents[:0:0]
	for _, e := range ents {
		if identity.Forbidden(filepath.Join(dir, e.Name())) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// sameEntry reports whether the filesystem objects at a and b are identical in the sense of
// setup.md 5.3: same type, and for files the same size and bytes, for links the same raw
// target, for folders the same tree (names, types, modes, file hashes, link targets). It
// stops at the first difference, so a differing pair is cheap.
func sameEntry(fsys identity.FS, a, b string) (bool, error) {
	ia, err := os.Lstat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Lstat(b)
	if err != nil {
		return false, err
	}
	return sameInfo(fsys, a, b, ia, ib)
}

func sameInfo(fsys identity.FS, a, b string, ia, ib fs.FileInfo) (bool, error) {
	ma, mb := ia.Mode(), ib.Mode()
	if ma.Type() != mb.Type() {
		return false, nil
	}
	switch {
	case ma.Type() == fs.ModeSymlink:
		ta, err := os.Readlink(a)
		if err != nil {
			return false, err
		}
		tb, err := os.Readlink(b)
		if err != nil {
			return false, err
		}
		return ta == tb, nil
	case ma.IsDir():
		if ma.Perm() != mb.Perm() {
			return false, nil
		}
		ea, err := readDirSorted(fsys, a)
		if err != nil {
			return false, err
		}
		eb, err := readDirSorted(fsys, b)
		if err != nil {
			return false, err
		}
		if len(ea) != len(eb) {
			return false, nil
		}
		for i := range ea {
			if ea[i].Name() != eb[i].Name() {
				return false, nil
			}
			xa, err := ea[i].Info()
			if err != nil {
				return false, err
			}
			xb, err := eb[i].Info()
			if err != nil {
				return false, err
			}
			same, err := sameInfo(fsys, filepath.Join(a, ea[i].Name()), filepath.Join(b, eb[i].Name()), xa, xb)
			if err != nil || !same {
				return false, err
			}
		}
		return true, nil
	case ma.IsRegular():
		if ma.Perm() != mb.Perm() || ia.Size() != ib.Size() {
			return false, nil
		}
		return sameBytes(fsys, a, b)
	}
	return true, nil // sockets, devices: same type is all that can be said
}

// sameBytes compares two files by SHA-256 (the files are opened through the guard).
func sameBytes(fsys identity.FS, a, b string) (bool, error) {
	ha, err := hashFile(fsys, a)
	if err != nil {
		return false, err
	}
	hb, err := hashFile(fsys, b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

// diffDir lists, for the folder f against p, the entries of f that p lacks and the entries
// present in both that differ, descending into folders that exist on both sides for `depth`
// levels (1 = only the entries of f themselves; `projects/` uses 2, project then session
// file, setup.md 5.3). Paths are relative and "/" separated.
func diffDir(fsys identity.FS, f, p string, depth int) (*model.DirDiff, error) {
	d := &model.DirDiff{}
	if err := diffInto(fsys, f, p, "", depth, d); err != nil {
		return nil, err
	}
	return d, nil
}

func diffInto(fsys identity.FS, f, p, rel string, depth int, d *model.DirDiff) error {
	ents, err := readDirSorted(fsys, f)
	if err != nil {
		return err
	}
	for _, e := range ents {
		name := e.Name()
		fa, pa := filepath.Join(f, name), filepath.Join(p, name)
		r := name
		if rel != "" {
			r = rel + "/" + name
		}
		ip, err := os.Lstat(pa)
		if os.IsNotExist(err) {
			d.Missing = append(d.Missing, r)
			continue
		}
		if err != nil {
			return err
		}
		ifa, err := os.Lstat(fa)
		if err != nil {
			return err
		}
		if depth > 1 && ifa.IsDir() && ip.IsDir() {
			if err := diffInto(fsys, fa, pa, r, depth-1, d); err != nil {
				return err
			}
			continue
		}
		same, err := sameInfo(fsys, fa, pa, ifa, ip)
		if err != nil {
			return err
		}
		if !same {
			d.Differ = append(d.Differ, r)
		}
	}
	return nil
}
