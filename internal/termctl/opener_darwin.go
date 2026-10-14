//go:build darwin

package termctl

import "os/exec"

// prepare adjusts the opener process for macOS: nothing.
func prepare(*exec.Cmd) {}
