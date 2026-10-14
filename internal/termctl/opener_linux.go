//go:build linux

package termctl

import "os/exec"

// prepare adjusts the opener process for Linux: nothing.
func prepare(*exec.Cmd) {}
