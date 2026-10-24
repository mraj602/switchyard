package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/machineadd"
	"github.com/mraj602/watchyard/internal/sshx/sshxtest"
)

// operator is a temp home and WY_HOME for running the real wy as the operator, with the
// fake ssh. Nothing of the person's own environment is passed on.
type operator struct {
	home, syHome string
	fake         *sshxtest.Fake
}

func newOperator(t *testing.T) *operator {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	return &operator{home: t.TempDir(), syHome: t.TempDir(), fake: sshxtest.New(t)}
}

func (o *operator) run(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(syBin, args...)
	cmd.Env = []string{"HOME=" + o.home, "WY_HOME=" + o.syHome, "PATH=/usr/bin:/bin", "TERM=dumb", "WY_NO_SPAWN=1",
		"WY_SSH_COMMAND=" + os.Getenv("WY_SSH_COMMAND"), "WY_FAKE_SSH_DIR=" + o.fake.Dir}
	cmd.Dir = o.home
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

// remote prepares the fake remote's home: fake claude and inert service tools on its PATH,
// so the real wy setup there never reaches the test machine's systemd or launchd.
func (o *operator) remote(t *testing.T, host string, withClaude bool) string {
	t.Helper()
	h := o.fake.RemoteHome(host)
	bin := filepath.Join(h, "bin")
	os.MkdirAll(bin, 0o755)
	for _, tool := range []string{"systemctl", "loginctl", "launchctl", "tailscale"} {
		os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
	if withClaude {
		b, err := os.ReadFile(filepath.Join(fakeClaudeDir(t), "claude"))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(bin, "claude"), b, 0o755)
		os.MkdirAll(filepath.Join(h, ".claude"), 0o700)
		os.WriteFile(filepath.Join(h, ".claude/settings.json"), []byte("{}\n"), 0o644)
		os.WriteFile(filepath.Join(h, ".claude.json"), []byte("{}\n"), 0o600)
	}
	return h
}

func TestMachineAddEndToEndWithRealSy(t *testing.T) {
	o := newOperator(t)
	rh := o.remote(t, "me@gpu-box", true)
	var got map[string]any
	hub := claimHub(t, 200, &got)
	out, errOut, code := o.run(t, "482-913\n", "machine", "add", "me@gpu-box", "--name", "gpu-box", "--reach", "direct", "--hub-url", hub.URL,
		"--insecure-lan", "--binary", syBin, "--code-stdin", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	hub.Close()
	if got["code"] != "482-913" || got["expect_kind"] != "machine" {
		t.Fatalf("claim body %v", got)
	}
	o.fake.AssertNoSecret("482-913", goodToken)
	// The remote is connected and has its own machine id and the copied wy.
	if b, _ := os.ReadFile(filepath.Join(rh, ".local/bin/wy")); len(b) == 0 {
		t.Fatal("no wy on the remote")
	}
	cfg, _ := os.ReadFile(filepath.Join(rh, ".config/watchyard/config.toml"))
	if !strings.Contains(string(cfg), hub.URL) {
		t.Fatalf("remote config.toml:\n%s", cfg)
	}
	var sum struct {
		Summary machineadd.Summary `json:"summary"`
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sum); err != nil || sum.Summary.Machine == "" {
		t.Fatalf("summary %q %v", lines[len(lines)-1], err)
	}
	m, err := identity.LoadOrCreateMachine(filepath.Join(rh, ".local/state/watchyard"), identity.MachineOptions{})
	if err != nil || m.ID != sum.Summary.Machine {
		t.Fatalf("remote machine %v %v, summary %s", m, err, sum.Summary.Machine)
	}

	// list shows it; remove forgets it and prints the uninstall step.
	out, _, code = o.run(t, "", "machine", "list", "--json")
	if code != 0 || !strings.Contains(out, `"ssh":"me@gpu-box"`) {
		t.Fatalf("list exit %d: %s", code, out)
	}
	out, errOut, code = o.run(t, "", "machine", "remove", "gpu-box")
	if code != 0 || !strings.Contains(out, "uninstall") {
		t.Fatalf("remove exit %d: %s %s", code, out, errOut)
	}
}

func TestMachineAddNoClaudeExit7(t *testing.T) {
	o := newOperator(t)
	o.remote(t, "me@bare", false)
	hub := claimHub(t, 200, &map[string]any{})
	_, errOut, code := o.run(t, "482-913\n", "machine", "add", "me@bare", "--reach", "direct", "--hub-url", hub.URL,
		"--insecure-lan", "--binary", syBin, "--code-stdin", "--yes")
	if code != 7 || !strings.Contains(errOut, "install Claude Code") {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
}

func TestMachineAddRefusesForbiddenSSHOption(t *testing.T) {
	o := newOperator(t)
	_, errOut, code := o.run(t, "", "machine", "add", "me@x", "--ssh-option", "StrictHostKeyChecking=no", "--dry-run")
	if code != 2 || !strings.Contains(errOut, "host key") {
		t.Fatalf("exit %d %s", code, errOut)
	}
	if len(o.fake.Calls()) != 0 {
		t.Fatal("ssh ran")
	}
}

func TestMachineRemoteSide(t *testing.T) {
	o := newOperator(t)
	// probe
	out, errOut, code := o.run(t, "", "machine", "probe", "--json", "--ports", "47830-47832")
	var r machineadd.ProbeReport
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil || r.Machine == "" || r.Home != o.home {
		t.Fatalf("probe exit %d %s %s", code, out, errOut)
	}
	// authorize-pull twice, then remove
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub watchyard-pull@hub\n"
	for i := 0; i < 2; i++ {
		if _, errOut, code := o.run(t, key, "machine", "authorize-pull", "--key-stdin", "--target", "pt_01ABC", "--from", "10.0.0.1"); code != 0 {
			t.Fatalf("authorize exit %d %s", code, errOut)
		}
	}
	b, _ := os.ReadFile(filepath.Join(o.home, ".ssh/authorized_keys"))
	if strings.Count(string(b), "sy-pull:pt_01ABC") != 1 || !strings.Contains(string(b), `command="`) || !strings.Contains(string(b), `outbox drain --stdio"`) {
		t.Fatalf("authorized_keys:\n%s", b)
	}
	if _, _, code := o.run(t, "", "machine", "authorize-pull", "--remove", "--target", "pt_01ABC"); code != 0 {
		t.Fatal("remove failed")
	}
	if b, _ := os.ReadFile(filepath.Join(o.home, ".ssh/authorized_keys")); len(b) != 0 {
		t.Fatalf("left: %q", b)
	}
	if _, _, code := o.run(t, "", "machine", "authorize-pull", "--target", "pt_1"); code != 2 {
		t.Fatalf("no mode: exit %d", code)
	}
	// rename
	out, _, code = o.run(t, "", "machine", "rename", "gpu box 2")
	if code != 0 || !strings.Contains(out, "gpu box 2") {
		t.Fatalf("rename %d %s", code, out)
	}
	out, _, _ = o.run(t, "", "machine", "show")
	if !strings.Contains(out, "gpu box 2") || !strings.Contains(out, r.Machine) {
		t.Fatalf("show after rename: %s (id %s)", out, r.Machine)
	}
	if _, _, code := o.run(t, "", "machine", "rename", ""); code != 2 {
		t.Fatalf("empty name: exit %d", code)
	}
}
