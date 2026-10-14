//go:build !windows

package apply

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive advisory lock on path without blocking; held reports that
// another process owns it. The lock dies with the process, so a crash never leaves it stuck.
func tryLock(path string) (unlock func(), held bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, false, nil
}
