//go:build !windows

package main

import (
	"os"
	"syscall"
)

// execFile replaces this process with path (argv[0] is the path itself), keeping the
// environment and the terminal: the launcher then runs in this terminal as if typed.
func execFile(path string, argv []string) error {
	return syscall.Exec(path, argv, os.Environ())
}
