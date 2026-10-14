//go:build !windows

package detach

import "syscall"

// Setsid makes the child a session leader with no controlling terminal, so it survives
// the hook's process group being signalled and is reparented to init when we exit.
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
