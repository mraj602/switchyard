//go:build windows

package outbox

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The lock covers one byte far beyond any data (offset 2^62), so other handles can still
// read and append the real bytes of the file: LockFileEx locks are mandatory on Windows,
// and locking byte 0 would make the lock-free readers of outbox.md section 4 fail.
const (
	lockOffsetLow  = 0
	lockOffsetHigh = 0x40000000
)

func lockOverlapped() *windows.Overlapped {
	return &windows.Overlapped{Offset: lockOffsetLow, OffsetHigh: lockOffsetHigh}
}

func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockOverlapped())
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

func lockBlocking(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, lockOverlapped())
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockOverlapped())
}
