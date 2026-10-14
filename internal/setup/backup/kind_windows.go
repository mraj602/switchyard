//go:build windows

package backup

import (
	"os"

	"github.com/mraj602/switchyard/internal/setup/model"
)

// linkKind returns the link type of a Lstat result, or "" when it is not a link. Since Go
// 1.23 a junction (mount point) is reported as ModeIrregular, and os.Readlink still works
// on it; a symbolic link is ModeSymlink.
func linkKind(path string, fi os.FileInfo) model.PathType {
	if fi.Mode()&os.ModeSymlink != 0 {
		return model.TypeSymlink
	}
	if fi.Mode()&os.ModeIrregular != 0 {
		if _, err := os.Readlink(path); err == nil {
			return model.TypeJunction
		}
	}
	return ""
}
