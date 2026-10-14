//go:build !windows

package peers

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock on path (creating it) and returns the unlock
// function. It dies with the process. When the file cannot be made, it locks nothing: the
// state files are then unwritable too, so there is nothing to protect.
func lockFile(path string) func() {
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return func() {}
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}
}
