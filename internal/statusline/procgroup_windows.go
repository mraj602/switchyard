//go:build windows

package statusline

import "os/exec"

// ownProcessGroup keeps the default behaviour on Windows (only the shell is killed).
func ownProcessGroup(*exec.Cmd) {}
