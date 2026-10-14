package main

import (
	"os"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/termenv"
)

// A nested hop forwards the first hop's host id with its focus URL, not the middle
// machine's own id.
func TestSSHNestedHopKeepsInheritedHost(t *testing.T) {
	script, calls := fakeSSH(t, "0")
	other := "warp://session/ffffffffffffffffffffffffffffffff"
	host := "m_0123456789ABCDEFGHJKMNPQRS"
	runSSHWith(t, map[string]string{"SY_SSH_COMMAND": script, "LC_SY_FOCUS": other, "LC_SY_HOST": host}, "h")
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "focus:"+other+"\n") || !strings.Contains(string(b), "host:"+host+"\n") {
		t.Errorf("nested hop: %s", b)
	}
	// An inherited host that is not a machine id is replaced, not forwarded.
	os.Remove(calls)
	runSSHWith(t, map[string]string{"SY_SSH_COMMAND": script, "LC_SY_FOCUS": other, "LC_SY_HOST": "evil"}, "h")
	b, _ = os.ReadFile(calls)
	if strings.Contains(string(b), "host:evil") {
		t.Errorf("a bogus host was forwarded: %s", b)
	}
}

func TestSSHChildEnvStripsInheritedCaseInsensitivelyOnWindows(t *testing.T) {
	base := []string{"Lc_Sy_Focus=x", "lc_sy_host=y", "PATH=/bin"}
	got := sshChildEnv(base, termenv.Fake(t.TempDir(), "windows", map[string]string{}))
	if len(got) != 1 || got[0] != "PATH=/bin" {
		t.Errorf("windows: %q", got)
	}
	got = sshChildEnv(base, termenv.Fake(t.TempDir(), "linux", map[string]string{}))
	if len(got) != 3 {
		t.Errorf("linux keeps differently-cased names: %q", got)
	}
}
