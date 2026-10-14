//go:build windows

package hublock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The lock covers one byte far beyond the pid text (offset 2^62), so the pid stays readable
// while the lock is held (LockFileEx locks are mandatory on Windows).
func overlapped() *windows.Overlapped { return &windows.Overlapped{Offset: 0, OffsetHigh: 0x40000000} }

func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped())
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped())
}
