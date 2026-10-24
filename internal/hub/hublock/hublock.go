// Package hublock is the lock `wy hub serve` holds on its data folder for as long as it
// runs (machines.md 8.2 step 1): `wy hub import` refuses while it is held, whatever address
// or database file the hub uses. The lock is an OS file lock on <data>/hub.lock, so a hub
// that dies releases it by dying; there is no stale pidfile to clean up.
//
// Depends on: nothing in this repository (golang.org/x/sys on Windows).
package hublock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// File is the lock file's name inside the data folder.
const File = "hub.lock"

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("a hub is running on this data folder")

// Lock is a held lock.
type Lock struct{ f *os.File }

// Acquire takes the lock on dataDir (made if missing, mode 0700) without blocking. It
// returns ErrHeld when a running hub has it. The pid is written for people to read; the
// lock itself is the file lock.
func Acquire(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, File), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	ok, err := tryLock(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if !ok {
		f.Close()
		return nil, ErrHeld
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	return &Lock{f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlock(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}

// Held reports whether a running hub holds the lock on dataDir. A folder with no lock file
// has no running hub.
func Held(dataDir string) (bool, error) {
	f, err := os.OpenFile(filepath.Join(dataDir, File), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	ok, err := tryLock(f)
	if err != nil {
		return false, err
	}
	if ok {
		unlock(f)
		return false, nil
	}
	return true, nil
}
