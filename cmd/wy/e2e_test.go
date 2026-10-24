package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/record"
)

// TestE2EClaudeCode runs the real `claude` once, headless, with the plugin loaded for that
// run only (--plugin-dir), the freshly built `wy` first on PATH, and WY_HOME in a temp
// folder, then checks the outbox. It uses the account Claude Code is logged in with in
// the current environment (a few tokens) and writes nothing to any settings or config:
// the only real folder Claude touches is its own session store, as in any run.
//
//	WY_E2E_CLAUDE=1 go test ./cmd/wy -run E2E -v
func TestE2EClaudeCode(t *testing.T) {
	if os.Getenv("WY_E2E_CLAUDE") != "1" {
		t.Skip("set WY_E2E_CLAUDE=1 to run Claude Code for real")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the plugin hook commands need sh")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}
	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugin", "watchyard"))
	if err != nil {
		t.Fatal(err)
	}

	// A bin dir with only wy, first on PATH.
	bin := t.TempDir()
	data, err := os.ReadFile(syBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "wy"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	syHome := t.TempDir()
	proj := t.TempDir()

	env := append([]string{}, os.Environ()...)
	env = setEnv(env, "PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = setEnv(env, "WY_HOME", syHome)
	for _, k := range []string{"WY_STATE_DIR", "WY_CONFIG_DIR", "WY_DATA_DIR"} {
		env = setEnv(env, k, "")
	}

	// What the records must say: derived from the same environment the hook sees.
	getenv := func(k string) string {
		for i := len(env) - 1; i >= 0; i-- {
			if v, ok := strings.CutPrefix(env[i], k+"="); ok {
				return v
			}
		}
		return ""
	}
	home, _ := os.UserHomeDir()
	slot := identity.Locator{Home: home, FS: identity.OSFS{}}.Current(getenv)
	oa, err := identity.ReadOAuth(identity.OSFS{}, slot.StateFile)
	if err != nil || oa == nil {
		t.Fatalf("no login in %s: %v (log in first)", slot.StateFile, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, "-p", "Reply with the single word OK",
		"--plugin-dir", plugin, "--max-turns", "1")
	cmd.Dir = proj
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("claude: %v\nstdout: %s\nstderr: %s", err, out.String(), errb.String())
	}
	t.Logf("claude finished in %v, replied %q", time.Since(start).Round(time.Millisecond), strings.TrimSpace(out.String()))

	want := []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}
	var recs []*record.Record
	deadline := time.Now().Add(15 * time.Second) // async hooks can finish after claude exits
	for {
		recs = readOutbox(t, filepath.Join(syHome, "state", "outbox"))
		if missing(recs, want) == "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if m := missing(recs, want); m != "" {
		t.Fatalf("no %s record; got events %v\nhook-errors.log: %s", m, eventNames(recs), readFile(filepath.Join(syHome, "state", "hook-errors.log")))
	}

	mb, err := os.ReadFile(filepath.Join(syHome, "state", "machine.json"))
	if err != nil {
		t.Fatal(err)
	}
	var machine identity.Machine
	if err := json.Unmarshal(mb, &machine); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Kind != record.KindHookEvent {
			continue
		}
		if r.Machine != machine.ID {
			t.Errorf("%s: machine %q, want %q", r.ID, r.Machine, machine.ID)
		}
		if r.Slot != slot.Name {
			t.Errorf("%s: slot %q, want %q", r.ID, r.Slot, slot.Name)
		}
		if r.Seat == nil || r.Seat.Account != oa.AccountUUID || r.Seat.Org != oa.OrganizationUUID {
			t.Errorf("%s: seat %+v, want account %s org %s", r.ID, r.Seat, oa.AccountUUID, oa.OrganizationUUID)
		}
		if _, ok := r.Derived["hook_ms"]; !ok {
			t.Errorf("%s: no derived.hook_ms", r.ID)
		}
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", r.ID, err)
		}
	}
	t.Logf("events in order: %v", eventNames(recs))
	if log := readFile(filepath.Join(syHome, "state", "hook-errors.log")); log != "" {
		t.Errorf("hook-errors.log is not empty:\n%s", log)
	}
}

func setEnv(env []string, k, v string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, k+"=") {
			out = append(out, kv)
		}
	}
	return append(out, k+"="+v)
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func readOutbox(t *testing.T, dir string) []*record.Record {
	t.Helper()
	var recs []*record.Record
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var r record.Record
			if err := json.Unmarshal(line, &r); err != nil {
				continue // a line still being written
			}
			recs = append(recs, &r)
		}
	}
	return recs
}

func eventNames(recs []*record.Record) []string {
	var out []string
	for _, r := range recs {
		var p struct {
			Name string `json:"hook_event_name"`
		}
		_ = json.Unmarshal(r.Payload, &p)
		out = append(out, p.Name)
	}
	return out
}

func missing(recs []*record.Record, want []string) string {
	have := map[string]bool{}
	for _, n := range eventNames(recs) {
		have[n] = true
	}
	for _, w := range want {
		if !have[w] {
			return w
		}
	}
	return ""
}
