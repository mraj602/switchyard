//go:build windows

package termctl

import (
	"os/exec"
	"syscall"
)

// prepare keeps rundll32 from flashing a console window.
func prepare(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
