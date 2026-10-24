package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/termenv"
)

// fakeSSH writes a script that records its argv and the LC_SY_* variables to a file, and
// exits with code.
func fakeSSH(t *testing.T, code string) (script, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	out = filepath.Join(dir, "calls")
	script = filepath.Join(dir, "fake-ssh")
	body := "#!/bin/sh\n{ for a in \"$@\"; do echo \"arg:$a\"; done; echo \"focus:$LC_SY_FOCUS\"; echo \"host:$LC_SY_HOST\"; } >> '" + out + "'\nexit " + code + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, out
}

func runSSHWith(t *testing.T, vars map[string]string, args ...string) (int, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WY_HOME", filepath.Join(home, "wy"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("LC_SY_FOCUS", "")
	t.Setenv("LC_SY_HOST", "")
	all := map[string]string{"WY_HOME": filepath.Join(home, "wy")}
	for k, v := range vars {
		all[k] = v
	}
	env := termenv.Fake(home, runtime.GOOS, all)
	old := sshEnv
	sshEnv = func() termenv.Env { return env }
	t.Cleanup(func() { sshEnv = old })
	if f := all["WARP_FOCUS_URL"]; f != "" {
		t.Setenv("WARP_FOCUS_URL", f)
	}
	var out, errb bytes.Buffer
	code := dispatch(append([]string{"ssh"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestSSHPassesOptionsFirstAndEnv(t *testing.T) {
	script, calls := fakeSSH(t, "0")
	code, _, errs := runSSHWith(t, map[string]string{"WY_SSH_COMMAND": script, "WARP_FOCUS_URL": focusURL1}, "gpu-box", "-p", "22")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	b, _ := os.ReadFile(calls)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"arg:-o", "arg:SendEnv=LC_SY_FOCUS", "arg:-o", "arg:SendEnv=LC_SY_HOST", "arg:gpu-box", "arg:-p", "arg:22", "focus:" + focusURL1}
	if len(lines) != len(want)+1 {
		t.Fatalf("lines %q", lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
	if h := lines[len(lines)-1]; !strings.HasPrefix(h, "host:m_") || len(h) != len("host:m_")+26 {
		t.Errorf("host line %q", h)
	}
}

func TestSSHNestedHopKeepsLCFocus(t *testing.T) {
	script, calls := fakeSSH(t, "0")
	other := "warp://session/ffffffffffffffffffffffffffffffff"
	runSSHWith(t, map[string]string{"WY_SSH_COMMAND": script, "LC_SY_FOCUS": other, "WARP_FOCUS_URL": focusURL1}, "h")
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "focus:"+other+"\n") {
		t.Errorf("nested hop: %s", b)
	}
}

func TestSSHDoesNotForwardNonMatchingFocusURL(t *testing.T) {
	script, calls := fakeSSH(t, "0")
	for _, bad := range []string{"https://evil.example/", "file:///etc/passwd", "warp://session/ABCDEF0123456789ABCDEF0123456789", ""} {
		os.Remove(calls)
		code, _, _ := runSSHWith(t, map[string]string{"WY_SSH_COMMAND": script, "WARP_FOCUS_URL": bad}, "h")
		b, _ := os.ReadFile(calls)
		s := string(b)
		if code != 0 || !strings.Contains(s, "focus:\n") || !strings.Contains(s, "host:\n") || !strings.Contains(s, "arg:SendEnv=LC_SY_FOCUS") {
			t.Errorf("%q: exit %d, call %q", bad, code, s)
		}
	}
}

func TestSSHExitCodeAndMissingSSH(t *testing.T) {
	script, _ := fakeSSH(t, "255")
	if code, _, _ := runSSHWith(t, map[string]string{"WY_SSH_COMMAND": script}, "h"); code != 255 {
		t.Errorf("exit %d, want ssh's 255", code)
	}
	if code, _, errs := runSSHWith(t, map[string]string{"WY_SSH_COMMAND": "/nonexistent/ssh-nope"}, "h"); code != 127 || !strings.Contains(errs, "not found") {
		t.Errorf("missing ssh: exit %d %q", code, errs)
	}
	if code, _, _ := runSSHWith(t, map[string]string{"WY_SSH_COMMAND": "ssh -o StrictHostKeyChecking=no"}, "h"); code != 2 {
		t.Errorf("forbidden option: exit %d, want 2", code)
	}
	if code, _, _ := runSSHWith(t, nil); code != 2 {
		t.Errorf("no args: exit %d, want 2", code)
	}
}

func TestSSHPrintConfigWritesNothing(t *testing.T) {
	code, out, _ := runSSHWith(t, nil, "--print-config")
	if code != 0 || !strings.Contains(out, "SendEnv LC_SY_FOCUS LC_SY_HOST") {
		t.Errorf("exit %d: %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".ssh")); err == nil {
		t.Error("touched ~/.ssh")
	}
}
