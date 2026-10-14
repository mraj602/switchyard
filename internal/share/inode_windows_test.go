//go:build windows

package share

import "os"

func inodeOf(fi os.FileInfo) uint64 { return 0 }
