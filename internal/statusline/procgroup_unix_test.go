//go:build !windows

package statusline

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWrapTimeoutKillsGrandchildren(t *testing.T) {
	sb := newSandbox(t)
	marker := filepath.Join(t.TempDir(), "pid")
	cmd := "sleep 30 & echo $! > " + marker + "; wait"
	sb.run(fixture(t, "full.json"), "--wrap", cmd)
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("grandchild pid not recorded: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(3 * time.Second)
	for {
		// signal 0 succeeds for a live process and a zombie; a reaped one gives ESRCH.
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived the timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
