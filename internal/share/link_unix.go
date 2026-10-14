//go:build !windows

package share

import (
	"errors"
	"os"
	"syscall"
)

func crossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

// linkInfo reports whether path itself is a link, and its raw target and whether it is a
// junction (never on Unix).
func linkInfo(path string) (raw string, junction, isLink bool) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return "", false, false
	}
	raw, err = os.Readlink(path)
	if err != nil {
		return "", false, false
	}
	return raw, false, true
}

func makeJunction(path, target string) error {
	return errors.New("share: junctions exist only on Windows")
}
