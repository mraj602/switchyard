//go:build windows

package client

import "os"

// ownedByMe: forward sockets are a Unix feature (sshd on the remote); never remove on Windows.
func ownedByMe(os.FileInfo) bool { return false }
