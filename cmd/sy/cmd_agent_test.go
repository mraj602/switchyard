package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/service"
	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
)

// recorder is the injectable service-manager runner: nothing real runs.
type recorder struct{ calls []string }

func (r *recorder) Run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if len(args) > 1 && args[1] == "is-active" {
		return []byte("active\n"), nil
	}
	return []byte("Linger=yes"), nil
}

// fakeServiceEnv is a service environment in the test's HOME, with the recording runner.
func fakeServiceEnv(t *testing.T, r *recorder) func(io.Writer) (service.Env, error) {
	t.Helper()
	return func(io.Writer) (service.Env, error) {
		home := os.Getenv("HOME")
		return service.Env{GOOS: "linux", Home: home, UID: 1000, User: "tester", SyPath: "/opt/sy/bin/sy",
			StateDir: filepath.Join(home, "state"), Getenv: func(string) string { return "" }, Runner: r}, nil
	}
}

func runSyArgs(args ...string) (code int, out, errs string) {
	var o, e bytes.Buffer
	code = dispatch(args, &o, &e)
	return code, o.String(), e.String()
}

func TestAgentEnableDisable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SY_HOME", filepath.Join(home, "sy"))
	for _, k := range []string{"SY_CONFIG_DIR", "SY_STATE_DIR", "SY_DATA_DIR", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))
	r := &recorder{}
	old := agentServiceEnv
	t.Cleanup(func() { agentServiceEnv = old })
	agentServiceEnv = fakeServiceEnv(t, r)

	code, out, errs := runSyArgs("agent", "enable")
	if code != 0 {
		t.Fatalf("enable: %d %q %q", code, out, errs)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "switchyard-agent.service")
	b, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(b), "ExecStart=/opt/sy/bin/sy agent run\n") {
		t.Fatalf("unit: %v %s", err, b)
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "systemctl --user enable --now switchyard-agent.service") {
		t.Errorf("calls %v", r.calls)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, "sy", "config", "config.toml"))
	if !strings.Contains(string(cfg), "enabled = true") {
		t.Errorf("config.toml: %q", cfg)
	}
	if code, out, _ = runSyArgs("agent", "status"); code != 0 || !strings.Contains(out, "service: installed, active") || !strings.Contains(out, "agent: not running") {
		t.Errorf("status: %d %q", code, out)
	}

	if code, out, errs = runSyArgs("agent", "disable"); code != 0 {
		t.Fatalf("disable: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Errorf("unit left: %v", err)
	}
	cfg, _ = os.ReadFile(filepath.Join(home, "sy", "config", "config.toml"))
	if !strings.Contains(string(cfg), "enabled = false") {
		t.Errorf("config.toml after disable: %q", cfg)
	}
	if code, _, _ := runSyArgs("agent"); code != 2 {
		t.Errorf("bare sy agent: exit %d", code)
	}
}

// sy uninstall removes the agent's unit. It runs the real binary in a temp HOME whose PATH
// starts with fake systemctl and loginctl scripts, so no real service manager is touched.
func TestUninstallRemovesAgentUnit(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	bin := t.TempDir()
	log := filepath.Join(s.syHome, "systemctl.log")
	script := "#!/bin/sh\necho \"$(basename $0) $*\" >> " + log + "\nexit 0\n"
	for _, n := range []string{"systemctl", "loginctl"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.env[0] = "PATH=" + bin + ":" + strings.TrimPrefix(s.env[0], "PATH=")
	s.env = append(s.env, "XDG_CONFIG_HOME=", "XDG_RUNTIME_DIR="+filepath.Join(s.syHome, "run"))
	s.mustSy(t, "agent", "enable")
	unit := h.Abs(".config/systemd/user/switchyard-agent.service")
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("enable wrote no unit: %v", err)
	}
	if out := s.mustSy(t, "uninstall", "--dry-run"); !strings.Contains(out, "sy agent service switchyard-agent") {
		t.Errorf("dry run does not list the agent: %s", out)
	}
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("dry run removed the unit: %v", err)
	}
	s.mustSy(t, "uninstall", "--yes")
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Errorf("uninstall left the unit: %v", err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "systemctl --user disable --now switchyard-agent.service") {
		t.Errorf("fake systemctl log: %q", b)
	}
}
