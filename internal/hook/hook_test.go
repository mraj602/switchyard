package hook

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/record"
)

var update = flag.Bool("update", false, "rewrite testdata/golden")

const fixtures = "../../testdata"

// sandbox is a fake machine: HOME, SY_HOME and one slot (~/.claude-3) logged in as
// oauth-3.json. Nothing outside the temp folder is touched.
type sandbox struct {
	t      *testing.T
	home   string
	env    map[string]string
	spawns [][]string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SY_HOME", filepath.Join(home, "sy"))
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("SY_STATE_DIR", "")
	slot := filepath.Join(home, ".claude-3")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	oauth, err := os.ReadFile(filepath.Join(fixtures, "auth", "oauth-3.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, ".claude.json"), oauth, 0o600); err != nil {
		t.Fatal(err)
	}
	sb := &sandbox{t: t, home: home, env: map[string]string{}}
	var env map[string]string
	b, err := os.ReadFile(filepath.Join(fixtures, "hooks", "env.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	for k, v := range env {
		sb.env[k] = v
	}
	sb.env["CLAUDE_CONFIG_DIR"] = slot
	sb.env["HOME"] = home
	sb.env["SY_HOME"] = filepath.Join(home, "sy")

	origExe, origStart := executable, startProcess
	executable = func() (string, error) { return "/nonexistent/sy", nil }
	startProcess = func(exe string, args []string, env []string) error {
		sb.spawns = append(sb.spawns, args)
		return nil
	}
	t.Cleanup(func() { executable, startProcess = origExe, origStart })
	return sb
}

func (sb *sandbox) count(first string) int {
	n := 0
	for _, a := range sb.spawns {
		if a[0] == first {
			n++
		}
	}
	return n
}

func (sb *sandbox) getenv(k string) string { return sb.env[k] }

func (sb *sandbox) state() string { return filepath.Join(sb.home, "sy", "state") }

func (sb *sandbox) run(event string, stdin []byte) {
	Run([]string{event}, bytes.NewReader(stdin), sb.getenv)
}

func (sb *sandbox) records() []*record.Record {
	sb.t.Helper()
	var out []*record.Record
	files, _ := filepath.Glob(filepath.Join(sb.state(), "outbox", "*.jsonl"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			sb.t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var r record.Record
			if err := json.Unmarshal(line, &r); err != nil {
				sb.t.Fatalf("bad line %q: %v", line, err)
			}
			out = append(out, &r)
		}
	}
	return out
}

var contentFieldNames = []string{"prompt", "tool_input", "tool_response", "permission_suggestions", "last_assistant_message", "custom_instructions"}

func TestGoldenByEvent(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtures, "hooks", "by-event", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			sb := newSandbox(t)
			in, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			event := strings.SplitN(name, ".", 2)[0]
			sb.run(event, in)
			recs := sb.records()
			if len(recs) != 1 {
				t.Fatalf("want 1 record, got %d (errors: %s)", len(recs), sb.errorLog())
			}
			r := recs[0]
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
			if r.Kind != record.KindHookEvent || r.Slot != "~/.claude-3" || !strings.HasPrefix(r.Machine, "m_") {
				t.Fatalf("envelope: %+v", r)
			}
			if r.Seat == nil || r.Seat.Account != "aaaaaaaa-0000-4000-8000-000000000003" || r.Seat.Src != "claude_json" {
				t.Fatalf("seat: %+v", r.Seat)
			}
			if r.Term == nil || r.Term.App != "warp" || r.Term.ID != "00000000000000000000000000000001" ||
				r.Term.FocusURL != "warp://session/00000000000000000000000000000001" {
				t.Fatalf("term: %+v", r.Term)
			}
			if _, ok := r.Derived["hook_ms"]; !ok {
				t.Fatal("derived.hook_ms missing")
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(r.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			for _, f := range contentFieldNames {
				if _, ok := payload[f]; ok {
					// Notification.message and StopFailure.error stay; everything listed here goes.
					t.Errorf("content field %q survived in payload", f)
				}
			}
			var gotName string
			_ = json.Unmarshal(payload["hook_event_name"], &gotName)
			if gotName != event {
				t.Errorf("hook_event_name = %q, want %q", gotName, event)
			}
			// Redacted lists exactly the content fields that were present in the input.
			var orig map[string]json.RawMessage
			_ = json.Unmarshal(in, &orig)
			for _, f := range contentFieldNames {
				if _, was := orig[f]; was && !contains(r.Redacted, f) && !(f == "tool_input" && event == "Stop") {
					if contentOnEvent(f, event) {
						t.Errorf("%s not listed in redacted %v", f, r.Redacted)
					}
				}
			}

			// Golden: the record with run-specific values fixed.
			r.ID, r.TS = "ID", "TS"
			r.Machine = "m_MACHINE"
			r.Derived["hook_ms"] = json.RawMessage("0")
			got, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("record differs from %s:\n%s", golden, got)
			}
			// One flush per hook.
			if sb.count("flush") != 1 {
				t.Errorf("spawns = %v, want one flush", sb.spawns)
			}
		})
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func contentOnEvent(field, event string) bool {
	switch field {
	case "prompt":
		return event == "UserPromptSubmit"
	case "tool_input":
		return strings.HasPrefix(event, "PreToolUse") || strings.HasPrefix(event, "PostToolUse") || event == "PermissionRequest" || event == "PermissionDenied"
	case "tool_response":
		return event == "PostToolUse"
	case "permission_suggestions":
		return event == "PermissionRequest"
	case "last_assistant_message":
		return event == "Stop" || event == "SubagentStop" || event == "StopFailure"
	case "custom_instructions":
		return event == "PreCompact"
	}
	return false
}

func (sb *sandbox) errorLog() string {
	b, _ := os.ReadFile(filepath.Join(sb.state(), ErrorLogName))
	return string(b)
}

func TestEventNameFromArgWhenPayloadLacksIt(t *testing.T) {
	sb := newSandbox(t)
	sb.run("Stop", []byte(`{"session_id":"s1"}`))
	sb.run("Stop", []byte(`{}`))
	recs := sb.records()
	if len(recs) != 2 {
		t.Fatalf("got %d records: %s", len(recs), sb.errorLog())
	}
	for _, r := range recs {
		var p struct {
			Name string `json:"hook_event_name"`
		}
		_ = json.Unmarshal(r.Payload, &p)
		if p.Name != "Stop" {
			t.Errorf("payload %s", r.Payload)
		}
	}
}

func TestEventNameFromPayloadWithoutArg(t *testing.T) {
	sb := newSandbox(t)
	Run(nil, strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1"}`), sb.getenv)
	if len(sb.records()) != 1 {
		t.Fatalf("no record: %s", sb.errorLog())
	}
}

func TestUnknownEventIsRecorded(t *testing.T) {
	sb := newSandbox(t)
	sb.run("BrandNewEvent", []byte(`{"session_id":"s1","iguana_necktie":7}`))
	recs := sb.records()
	if len(recs) != 1 || !strings.Contains(string(recs[0].Payload), "iguana_necktie") {
		t.Fatalf("records: %v", recs)
	}
}

func TestBadInputLogsAndWritesNothing(t *testing.T) {
	sb := newSandbox(t)
	sb.run("Stop", []byte(`not json`))
	if len(sb.records()) != 0 {
		t.Fatal("record written for bad input")
	}
	if !strings.Contains(sb.errorLog(), "Stop") {
		t.Fatalf("error log = %q", sb.errorLog())
	}
}

func TestErrorLogIsCapped(t *testing.T) {
	sb := newSandbox(t)
	for i := 0; i < 3000; i++ {
		sb.run("Stop", []byte(`not json`))
		if i%500 == 0 {
			if fi, err := os.Stat(filepath.Join(sb.state(), ErrorLogName)); err == nil && fi.Size() > ErrorLogMax+1024 {
				t.Fatalf("log is %d bytes", fi.Size())
			}
		}
	}
	fi, err := os.Stat(filepath.Join(sb.state(), ErrorLogName))
	if err != nil || fi.Size() > ErrorLogMax+1024 {
		t.Fatalf("log: %v %v", fi, err)
	}
}

// A broken outbox folder must not change anything the caller sees: no panic, nothing on
// stdout (Run has no stdout at all), one line in the error log.
func TestReadOnlyOutboxStillExitsZero(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory mode bits do not make a folder read-only")
	}
	sb := newSandbox(t)
	ob := filepath.Join(sb.state(), "outbox")
	if err := os.MkdirAll(sb.state(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ob, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ob, 0o700) })
	in, _ := os.ReadFile(filepath.Join(fixtures, "hooks", "by-event", "Stop.json"))
	sb.run("Stop", in)
	if got := sb.errorLog(); !strings.Contains(got, "Stop") {
		t.Fatalf("error log = %q", got)
	}
}

func TestFlushIsDebounced(t *testing.T) {
	sb := newSandbox(t)
	in, _ := os.ReadFile(filepath.Join(fixtures, "hooks", "by-event", "Stop.json"))
	for i := 0; i < 5; i++ {
		sb.run("Stop", in)
	}
	if n := sb.count("flush"); n != 1 {
		t.Fatalf("%d flushes in 5 quick hooks, want 1", n)
	}
	// After the window a new hook may flush again.
	old := time.Now().Add(-3 * time.Second)
	if err := os.Chtimes(filepath.Join(sb.state(), "outbox", flushStamp), old, old); err != nil {
		t.Fatal(err)
	}
	sb.run("Stop", in)
	if n := sb.count("flush"); n != 2 {
		t.Fatalf("%d flushes after the window, want 2", n)
	}
}

func TestStaleLoginStartsBackgroundRefreshOnce(t *testing.T) {
	sb := newSandbox(t)
	in, _ := os.ReadFile(filepath.Join(fixtures, "hooks", "by-event", "SessionStart.json"))
	sb.run("SessionStart", in)
	sb.run("SessionStart", in)
	n := 0
	for _, a := range sb.spawns {
		if len(a) == 2 && a[0] == "hook" && a[1] == refreshArg {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("refresh spawned %d times, want 1 (spawns %v)", n, sb.spawns)
	}
}

func TestTermVariants(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if Term(env(nil), "") != nil {
		t.Error("empty env should give no term")
	}
	got := Term(env(map[string]string{"TERM_PROGRAM": "WarpTerminal", "WARP_FOCUS_URL": "warp://a", "LC_SY_FOCUS": "warp://laptop"}), "")
	if got.App != "warp" || got.FocusURL != "warp://laptop" {
		t.Errorf("LC_SY_FOCUS must win: %+v", got)
	}
	if g := Term(env(map[string]string{"TERM_PROGRAM": "iTerm.app"}), ""); g.App != "iterm" {
		t.Errorf("%+v", g)
	}
	if g := Term(env(map[string]string{"TMUX": "/tmp/tmux-1000/default,1,0"}), ""); g.App != "tmux" {
		t.Errorf("%+v", g)
	}
	if g := Term(env(map[string]string{"LC_SY_FOCUS": "x://y"}), ""); g.App != "unknown" || g.FocusURL != "x://y" {
		t.Errorf("%+v", g)
	}
}

func TestAppendedRecordsReadBack(t *testing.T) {
	sb := newSandbox(t)
	in, _ := os.ReadFile(filepath.Join(fixtures, "hooks", "by-event", "Stop.json"))
	sb.run("Stop", in)
	st, err := outbox.New(filepath.Join(sb.state(), "outbox")).Stat()
	if err != nil || st.DayFiles != 1 {
		t.Fatalf("stat %+v %v", st, err)
	}
}
