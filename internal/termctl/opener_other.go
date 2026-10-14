//go:build !linux && !darwin && !windows

package termctl

import "os/exec"

// prepare adjusts the opener process for other Unix systems: nothing (xdg-open).
func prepare(*exec.Cmd) {}
