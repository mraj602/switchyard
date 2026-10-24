// Package detach starts background processes that outlive the caller, and debounces
// them with a timestamp file. `wy hook` uses it to start `wy flush` and the login refresh
// without ever waiting for them.
//
// Depends on: nothing inside the module.
package detach

import (
	"os"
	"os/exec"
	"time"
)

// Start runs exe with args as a detached background process: its own session (unix) or
// DETACHED_PROCESS (Windows), no stdin/stdout/stderr, never waited for. env nil means the
// caller's environment. It returns as soon as the process has been created.
func Start(exe string, args []string, env []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil // /dev/null
	cmd.SysProcAttr = sysProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	// Not waited for: the child is reparented when we exit; Release frees our handle.
	return cmd.Process.Release()
}

// Debounce reports whether the caller may act now, and if so records the time in stamp
// (the file's mtime). It returns false when stamp was touched less than d ago. Two callers
// racing may both win; that costs one extra process, never correctness. If the stamp
// cannot be written it returns true: a read-only folder must not stop the work, it only
// loses the rate limit.
func Debounce(stamp string, d time.Duration, now time.Time) bool {
	if fi, err := os.Stat(stamp); err == nil {
		if age := now.Sub(fi.ModTime()); age < d && age > -d {
			return false
		}
	}
	f, err := os.OpenFile(stamp, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return true
	}
	f.Close()
	_ = os.Chtimes(stamp, now, now)
	return true
}
