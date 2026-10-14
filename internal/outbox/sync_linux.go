package outbox

import (
	"os"
	"syscall"
)

// syncFile is fdatasync on Linux (outbox.md section 3 step 8).
func syncFile(f *os.File) error {
	for {
		err := syscall.Fdatasync(int(f.Fd()))
		if err != syscall.EINTR {
			return err
		}
	}
}
