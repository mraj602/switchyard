//go:build !windows

package backup

import (
	"os"

	"github.com/mraj602/switchyard/internal/setup/model"
)

// linkKind returns the link type of a Lstat result, or "" when it is not a link.
func linkKind(_ string, fi os.FileInfo) model.PathType {
	if fi.Mode()&os.ModeSymlink != 0 {
		return model.TypeSymlink
	}
	return ""
}
