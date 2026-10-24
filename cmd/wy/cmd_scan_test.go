package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

// fakeHome makes a home with ~/.claude (settings.json, the capture transcripts) and points
// HOME and WY_HOME at throwaway folders. Returns the home and the outbox folder.
func fakeHome(t *testing.T) (home, ob string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	wy := t.TempDir()
	t.Setenv("WY_HOME", wy)
	for _, v := range []string{"WY_CONFIG_DIR", "WY_STATE_DIR", "WY_DATA_DIR"} {
		t.Setenv(v, "")
	}
	proj := filepath.Join(home, ".claude", "projects", "-home-user-p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob("../../testdata/transcripts/*.jsonl")
	for _, p := range m {
		b, _ := os.ReadFile(p)
		if err := os.WriteFile(filepath.Join(proj, filepath.Base(p)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, filepath.Join(wy, "state", "outbox")
}

// fakeClaudeOnPath puts a `claude` shell script first on PATH (unix only).
func fakeClaudeOnPath(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in for claude")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
}

func runCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := dispatch(args, &out, &errb)
	return code, out.String(), errb.String()
}

func outboxRecords(t *testing.T, dir string) []*record.Record {
	t.Helper()
	b, err := outbox.New(dir).Read(outbox.Cursor{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []*record.Record
	for _, it := range b.Items {
		var r record.Record
		if err := json.Unmarshal(it.Raw, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, &r)
	}
	return out
}

func TestScanCommand(t *testing.T) {
	home, ob := fakeHome(t)
	if err := os.WriteFile(filepath.Join(home, ".claude", "stats-cache.json"), []byte(`{"version":2,"dailyActivity":[],"dailyModelTokens":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runCmd(t, "scan", "--history")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "token rows") || !strings.Contains(out, "history: sent for ~/.claude") {
		t.Errorf("output %q", out)
	}
	var tokens, hist int
	for _, r := range outboxRecords(t, ob) {
		switch r.Kind {
		case record.KindTokenRows:
			tokens++
			if !strings.HasPrefix(r.Machine, "m_") || r.Slot != "~/.claude" {
				t.Errorf("envelope %+v", r)
			}
		case record.KindHistoryImport:
			hist++
		}
	}
	if tokens == 0 || hist != 1 {
		t.Fatalf("%d token_rows, %d history_import", tokens, hist)
	}
	// Second run: nothing new, history not sent again; --force sends it again.
	_, out, _ = runCmd(t, "scan", "--history")
	if !strings.Contains(out, "0 token rows") || !strings.Contains(out, "already sent for ~/.claude") {
		t.Errorf("second run %q", out)
	}
	runCmd(t, "scan", "--history", "--force")
	hist = 0
	for _, r := range outboxRecords(t, ob) {
		if r.Kind == record.KindHistoryImport {
			hist++
		}
	}
	if hist != 2 {
		t.Errorf("--force: %d history records", hist)
	}
}

func TestScanFlagsAndSlot(t *testing.T) {
	fakeHome(t)
	if code, _, _ := runCmd(t, "scan", "--force"); code != 2 {
		t.Errorf("--force without --history: exit %d", code)
	}
	code, _, errs := runCmd(t, "scan", "--slot", "~/.claude-9")
	if code != 1 || !strings.Contains(errs, "no such folder") {
		t.Errorf("unknown slot: %d %q", code, errs)
	}
	for _, name := range []string{"~/.claude", ".claude", "claude"} {
		if code, _, errs := runCmd(t, "scan", "--slot", name); code != 0 {
			t.Errorf("--slot %s: %d %s", name, code, errs)
		}
	}
}

func TestAgentsCommand(t *testing.T) {
	_, ob := fakeHome(t)
	fakeClaudeOnPath(t, `echo '[{"pid":1,"cwd":"/x","kind":"interactive","startedAt":1,"sessionId":"s1","name":"n","status":"idle"}]'`)
	code, out, errs := runCmd(t, "agents")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "~/.claude") || !strings.Contains(out, "complete=true") {
		t.Errorf("output %q", out)
	}
	recs := outboxRecords(t, ob)
	if len(recs) != 1 || recs[0].Kind != record.KindSessionList || recs[0].Slot != "~/.claude" {
		t.Fatalf("%+v", recs)
	}
	var p record.SessionListPayload
	if err := recs[0].DecodePayload(&p); err != nil || !p.Complete || p.Source != "claude_agents" || len(p.Sessions) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestStatusCommand(t *testing.T) {
	_, ob := fakeHome(t)
	o := outbox.New(ob)
	now := time.Now()
	for i, ev := range []string{
		`{"hook_event_name":"SessionStart","session_id":"sess-aaaa-1111","cwd":"/work/api","source":"startup"}`,
		`{"hook_event_name":"UserPromptSubmit","session_id":"sess-aaaa-1111","cwd":"/work/api","prompt":"hi"}`,
		`{"hook_event_name":"PermissionRequest","session_id":"sess-aaaa-1111","cwd":"/work/api","tool_name":"Bash"}`,
	} {
		r, err := record.NewHookEvent(record.Meta{Machine: "m_x", Slot: "~/.claude", Now: now.Add(time.Duration(i-10) * time.Second)}, []byte(ev), record.HookOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := o.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errs := runCmd(t, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	for _, w := range []string{"!needs_permission", "api", "~/.claude", "1 session, 1 needs you"} {
		if !strings.Contains(out, w) {
			t.Errorf("lacks %q:\n%s", w, out)
		}
	}
	code, out, _ = runCmd(t, "status", "--json")
	var js struct {
		Sessions []struct {
			State    string `json:"state"`
			NeedsYou bool   `json:"needs_you"`
			Project  string `json:"project"`
		} `json:"sessions"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &js) != nil || len(js.Sessions) != 1 || js.Sessions[0].State != "needs_permission" || !js.Sessions[0].NeedsYou {
		t.Fatalf("json: %d %q", code, out)
	}
	// An empty outbox prints the hint, and --json an empty list.
	fakeHome(t)
	if _, out, _ := runCmd(t, "status"); !strings.Contains(out, "No sessions") {
		t.Errorf("%q", out)
	}
	if _, out, _ := runCmd(t, "status", "--json"); !strings.Contains(out, `"sessions": []`) {
		t.Errorf("%q", out)
	}
}

func TestDoctorExitCode(t *testing.T) {
	fakeHome(t)
	// No claude on PATH: a failed check, exit 1, but the report is still printed.
	t.Setenv("PATH", t.TempDir())
	code, out, errs := runCmd(t, "doctor")
	if code != 1 || !strings.Contains(out, "FAIL  claude") || !strings.Contains(errs, "1 check(s) failed") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	code, out, _ = runCmd(t, "doctor", "--json")
	var rep struct {
		Checks []struct{ Name, Level string }
		Fail   int
	}
	if code != 1 || json.Unmarshal([]byte(out), &rep) != nil || rep.Fail != 1 {
		t.Fatalf("json: %d %q", code, out)
	}
	// With claude present only warnings remain (no hooks, no status line): exit 0.
	fakeClaudeOnPath(t, `echo "2.1.296 (Claude Code)"`)
	code, out, errs = runCmd(t, "doctor")
	if code != 0 || !strings.Contains(out, "WARN") || !strings.Contains(out, "2.1.296") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
}
