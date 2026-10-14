//go:build !windows

package statusline

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts the command in a new process group and makes cancellation (the
// timeout) kill the whole group, so a wrapped script's background children die with it.
func ownProcessGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
