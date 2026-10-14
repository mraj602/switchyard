//go:build !windows

package share

import (
	"os"
	"syscall"
)

func inodeOf(fi os.FileInfo) uint64 { return uint64(fi.Sys().(*syscall.Stat_t).Ino) }
