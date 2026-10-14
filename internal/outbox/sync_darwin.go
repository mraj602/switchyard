package outbox

import (
	"os"
	"syscall"
)

// syncFile is plain fsync on macOS. Go's (*os.File).Sync issues F_FULLFSYNC there, which
// costs tens of milliseconds; the spec asks for the cheaper call.
func syncFile(f *os.File) error {
	for {
		err := syscall.Fsync(int(f.Fd()))
		if err != syscall.EINTR {
			return err
		}
	}
}
