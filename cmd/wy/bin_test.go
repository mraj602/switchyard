package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/sshx/sshxtest"
)

// wyBin is the wy binary built once for the tests that run the real executable.
var wyBin string

func TestMain(m *testing.M) {
	sshxtest.Main() // the test binary is also the fake ssh of the machine tests
	cleanup := isolateHome()
	dir, err := os.MkdirTemp("", "wy-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := "wy"
	if runtime.GOOS == "windows" {
		name = "wy.exe"
	}
	wyBin = filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", wyBin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build wy: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	cleanup()
	os.Exit(code)
}

// isolateHome points HOME and every Claude, Watchyard and XDG location at a temp folder
// for the whole test binary, so a test that forgets its own sandbox can never read or
// write the real home. The Go caches keep their real locations so builds stay warm.
func isolateHome() func() {
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if os.Getenv(k) == "" {
			if out, err := exec.Command("go", "env", k).Output(); err == nil {
				os.Setenv(k, strings.TrimSpace(string(out)))
			}
		}
	}
	home, err := os.MkdirTemp("", "wy-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE_") || strings.HasPrefix(k, "WY_") || strings.HasPrefix(k, "XDG_") {
			os.Unsetenv(k)
		}
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	os.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	os.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	return func() { os.RemoveAll(home) }
}

// sandboxEnv is a minimal environment for running wy: a temp HOME and WY_HOME, one logged
// in slot, nothing inherited that could point at real data.
func sandboxEnv(t *testing.T) (env []string, home string) {
	t.Helper()
	home = t.TempDir()
	slot := filepath.Join(home, ".claude-3")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	oauth, err := os.ReadFile(filepath.Join("..", "..", "testdata", "auth", "oauth-3.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, ".claude.json"), oauth, 0o600); err != nil {
		t.Fatal(err)
	}
	env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home, "USERPROFILE=" + home,
		"WY_HOME=" + filepath.Join(home, "wy"),
		"WY_NO_SPAWN=1", // a detached flush would outlive the test and race TempDir cleanup
		"CLAUDE_CONFIG_DIR=" + slot,
		"TERM_PROGRAM=WarpTerminal",
		"WARP_TERMINAL_SESSION_UUID=00000000000000000000000000000001",
		"WARP_FOCUS_URL=warp://session/00000000000000000000000000000001",
	}
	return env, home
}

func runWy(t *testing.T, env []string, stdin string, args ...string) (stdout, stderr []byte, d time.Duration) {
	t.Helper()
	cmd := exec.Command(wyBin, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	start := time.Now()
	err := cmd.Run()
	d = time.Since(start)
	if err != nil {
		t.Fatalf("wy %v: %v\nstderr: %s", args, err, e.String())
	}
	return o.Bytes(), e.Bytes(), d
}

func countLines(t *testing.T, dir string) int {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	n := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n += bytes.Count(b, []byte("\n"))
	}
	return n
}

// The whole `wy hook` process: exit 0, empty stdout and stderr, one record per call, and a
// latency report. Wall time includes process start; derived.hook_ms (inside the process)
// is lower. The 20 ms target is logged, not enforced: CI and busy machines vary.
func TestHookBinaryLatency(t *testing.T) {
	env, home := sandboxEnv(t)
	payload, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "by-event", "PostToolUse.Bash.json"))
	if err != nil {
		t.Fatal(err)
	}
	const n = 100
	// Warm-up: creates machine.json and the identity cache like a first run would.
	runWy(t, env, string(payload), "hook", "PostToolUse")

	ds := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		out, errOut, d := runWy(t, env, string(payload), "hook", "PostToolUse")
		if len(out) != 0 || len(errOut) != 0 {
			t.Fatalf("hook printed: stdout %q stderr %q", out, errOut)
		}
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p50, p95 := ds[n/2], ds[n*95/100]
	t.Logf("wy hook wall time over %d runs: p50 %v, p95 %v, max %v", n, p50, p95, ds[n-1])
	if p95 > 20*time.Millisecond {
		t.Logf("NOTE: p95 above the 20 ms target (machine load or a slow disk?)")
	}
	if p95 > 500*time.Millisecond {
		t.Errorf("p95 %v is far above target", p95)
	}
	if got := countLines(t, filepath.Join(home, "wy", "state", "outbox")); got != n+1 {
		t.Errorf("outbox has %d records, want %d", got, n+1)
	}
}

func TestHookBinaryReadOnlyOutbox(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
	env, home := sandboxEnv(t)
	state := filepath.Join(home, "wy", "state")
	ob := filepath.Join(state, "outbox")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ob, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ob, 0o700)
	out, errOut, _ := runWy(t, env, `{"hook_event_name":"Stop","session_id":"s"}`, "hook", "Stop")
	if len(out) != 0 || len(errOut) != 0 {
		t.Fatalf("stdout %q stderr %q", out, errOut)
	}
	log, _ := os.ReadFile(filepath.Join(state, "hook-errors.log"))
	if !strings.Contains(string(log), "Stop") {
		t.Fatalf("hook-errors.log = %q", log)
	}
}

func TestHookBinaryGarbageStdin(t *testing.T) {
	env, _ := sandboxEnv(t)
	out, errOut, _ := runWy(t, env, "\x00\x01 not json", "hook")
	if len(out) != 0 || len(errOut) != 0 {
		t.Fatalf("stdout %q stderr %q", out, errOut)
	}
}

func TestFlushQuietWithoutHubIsSilent(t *testing.T) {
	env, _ := sandboxEnv(t)
	out, errOut, _ := runWy(t, env, "", "flush", "--quiet")
	if len(out)+len(errOut) != 0 {
		t.Fatalf("flush printed %q %q", out, errOut)
	}
}

func TestStatuslineBinaryWrap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	env, home := sandboxEnv(t)
	in, err := os.ReadFile(filepath.Join("..", "..", "testdata", "statusline", "full.json"))
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := runWy(t, env, string(in), "statusline", "--wrap", `printf 'hello %s' "$(cat | head -c 5)"`)
	if !strings.HasPrefix(string(out), "hello {\n  ") && !strings.HasPrefix(string(out), "hello {") {
		t.Fatalf("wrapped output lost: %q", out)
	}
	if !strings.Contains(string(out), "claude-3 · 5h 24% · 7d 41% · ctx 9%") {
		t.Fatalf("watchyard part missing: %q", out)
	}
	if got := countLines(t, filepath.Join(home, "wy", "state", "outbox")); got != 1 {
		t.Fatalf("limit_sample records: %d", got)
	}
}
