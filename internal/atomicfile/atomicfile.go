// Package atomicfile replaces a file atomically: write a temp file next to it, fsync,
// rename over the target, then fsync the folder. Readers see the old or the new file,
// never a partial one.
package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// TempName is the transient name used while writing target: ".<base>.<random>.tmp".
func TempName(base string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "." + base + "." + hex.EncodeToString(b[:]) + ".tmp"
}

// resolve follows symlinks at path (also dangling ones) and returns the real file to
// replace, so an atomic write keeps ~/.ssh/config (and the like) a symlink.
func resolve(path string) string {
	for i := 0; i < 40; i++ {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return path
		}
		t, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(path), t)
		}
		path = t
	}
	return path
}

// Write replaces path with data (mode perm). The folder must exist. When path is a
// symlink, the file it points to is replaced and the link stays.
func Write(path string, data []byte, perm os.FileMode) error {
	path = resolve(path)
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp := filepath.Join(dir, TempName(base))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	cleanup := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return cleanup(fmt.Errorf("write %s: %w", tmp, err))
	}
	if err := f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return cleanup(err)
	}
	if err := f.Sync(); err != nil {
		return cleanup(fmt.Errorf("fsync %s: %w", tmp, err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir makes the rename durable. Best effort: not supported on Windows.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
