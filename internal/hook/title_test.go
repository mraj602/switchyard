package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

// titleSandbox is a sandbox with titles on and the slot labelled "work".
func titleSandbox(t *testing.T, toml string) *sandbox {
	t.Helper()
	sb := newSandbox(t)
	cfg := filepath.Join(sb.home, "wy", "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if toml == "" {
		toml = "[terminal]\ntitles = true\n[slots.labels]\n\"~/.claude-3\" = \"work\"\n"
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	return sb
}

func (sb *sandbox) title(event string, payload string) string {
	var out bytes.Buffer
	RunTitle([]string{event}, strings.NewReader(payload), &out, sb.getenv)
	return out.String()
}

func payload(sid, cwd, event string, extra map[string]any) string {
	m := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": event}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func seq(title string) string {
	return `{"terminalSequence":"\u001b]2;` + title + `\u0007"}` + "\n"
}

func TestTitleGoldenPerState(t *testing.T) {
	cases := []struct {
		name, event string
		extra       map[string]any
		want        string
	}{
		{"start", "SessionStart", nil, "work · api · done"},
		{"prompt", "UserPromptSubmit", nil, "work · api · working"},
		{"perm", "PermissionRequest", nil, "work · api · permission"},
		{"notif perm", "Notification", map[string]any{"notification_type": "permission_prompt"}, "work · api · permission"},
		{"notif elicit", "Notification", map[string]any{"notification_type": "elicitation_dialog"}, "work · api · asked"},
		{"notif idle", "Notification", map[string]any{"notification_type": "idle_prompt"}, "work · api · done"},
		{"stop", "Stop", map[string]any{"last_assistant_message": "All done."}, "work · api · done"},
		{"stop question", "Stop", map[string]any{"last_assistant_message": "Done with part one.\n\nShould I continue with the migration?"}, "work · api · asked"},
		{"fail", "StopFailure", map[string]any{"error": "model_not_found"}, "work · api · failed"},
		{"limit", "StopFailure", map[string]any{"error": "rate_limit"}, "work · api · limit"},
		{"end", "SessionEnd", nil, "work · api · ended"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb := titleSandbox(t, "")
			got := sb.title(c.event, payload("s1", "/home/u/code/api", c.event, c.extra))
			if got != seq(c.want) {
				t.Errorf("got %q, want %q", got, seq(c.want))
			}
		})
	}
}

func TestTitleSilentCases(t *testing.T) {
	p := payload("s1", "/x/api", "UserPromptSubmit", nil)

	sb := titleSandbox(t, "[terminal]\ntitles = false\n[slots.labels]\n\"~/.claude-3\" = \"work\"\n")
	if got := sb.title("UserPromptSubmit", p); got != "" {
		t.Errorf("titles off printed %q", got)
	}
	if _, err := os.Stat(filepath.Join(sb.state(), "term")); err == nil {
		t.Error("titles off wrote state")
	}

	sb = titleSandbox(t, "[terminal]\ntitles = true\n")
	if got := sb.title("UserPromptSubmit", p); got != "" {
		t.Errorf("no label printed %q", got)
	}

	sb = titleSandbox(t, "")
	if got := sb.title("UserPromptSubmit", p); got != seq("work · api · working") {
		t.Fatalf("first: %q", got)
	}
	if got := sb.title("UserPromptSubmit", p); got != "" {
		t.Errorf("unchanged title printed %q", got)
	}
	if got := sb.title("Stop", payload("s1", "/x/api", "Stop", nil)); got != seq("work · api · done") {
		t.Errorf("changed: %q", got)
	}
	// Another session is tracked on its own.
	if got := sb.title("UserPromptSubmit", payload("s2", "/x/api", "UserPromptSubmit", nil)); got == "" {
		t.Error("second session silent")
	}
	// An unknown notification keeps the word: silent.
	if got := sb.title("Notification", payload("s1", "/x/api", "Notification", map[string]any{"notification_type": "auth_success"})); got != "" {
		t.Errorf("auth_success printed %q", got)
	}
	// Garbage in, nothing out.
	for _, in := range []string{"", "not json", "[]", `{"session_id":""}`, "null"} {
		if got := sb.title("Stop", in); got != "" {
			t.Errorf("input %q printed %q", in, got)
		}
	}
	st, err := os.Stat(filepath.Join(sb.state(), "term", "title-s1.json"))
	if err != nil || st.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("title file: %v %v", st, err)
	}
	if len(sb.records()) != 0 {
		t.Error("the title hook must not write records")
	}
}

// The output is exactly one JSON object or empty for every fixture event, and the title
// hook never creates an outbox record or a hook error.
func TestTitleOutputForEveryFixture(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(fixtures, "hooks", "by-event", "*.json"))
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			sb := titleSandbox(t, "")
			in, _ := os.ReadFile(f)
			event := strings.SplitN(name, ".", 2)[0]
			got := sb.title(event, string(in))
			if got == "" {
				return
			}
			var o map[string]string
			if err := json.Unmarshal([]byte(got), &o); err != nil || len(o) != 1 || !strings.HasPrefix(o["terminalSequence"], "\x1b]2;") {
				t.Fatalf("not one terminalSequence object: %q (%v)", got, err)
			}
			if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "}\n") {
				t.Errorf("framing: %q", got)
			}
			if sb.errorLog() != "" {
				t.Errorf("error log: %s", sb.errorLog())
			}
		})
	}
}

func TestTitleHostileCwdAndLabel(t *testing.T) {
	sb := titleSandbox(t, "[terminal]\ntitles = true\n[slots.labels]\n\"~/.claude-3\" = \"w;o\\u0007rk\\u009d\"\n")
	got := sb.title("Stop", payload("s1", "/x/a;b\x07c\x1b]0;pwn\u009d", "Stop", nil))
	var o map[string]string
	if err := json.Unmarshal([]byte(got), &o); err != nil {
		t.Fatalf("%q: %v", got, err)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(o["terminalSequence"], "\x1b]2;"), "\x07")
	if strings.ContainsAny(body, ";\x07\x1b\u009d") {
		t.Errorf("hostile bytes in payload %q", body)
	}
}

func TestTitleHookLatency(t *testing.T) {
	sb := titleSandbox(t, "")
	var d []time.Duration
	for i := 0; i < 100; i++ {
		in := payload(fmt.Sprintf("lat-%d", i), "/x/api", "UserPromptSubmit", nil)
		var out bytes.Buffer
		start := time.Now()
		RunTitle([]string{"UserPromptSubmit"}, strings.NewReader(in), &out, sb.getenv)
		d = append(d, time.Since(start))
		if out.Len() == 0 {
			t.Fatal("no output")
		}
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	p95 := d[94]
	t.Logf("p95 %v, max %v", p95, d[99])
	budget := 15 * time.Millisecond
	if raceEnabled {
		budget *= 4
	}
	if p95 > budget {
		t.Errorf("p95 %v over %v", p95, budget)
	}
}

func TestTermHost(t *testing.T) {
	const laptop = "m_01HZX3K9Q2M8R5T7V9WBCD4EFG"
	const self = "m_01HZX3K9Q2M8R5T7V9WBCD4EFH"
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	g := Term(env(map[string]string{"TERM_PROGRAM": "WarpTerminal", "LC_SY_HOST": laptop}), self)
	if g == nil || g.Host != laptop {
		t.Fatalf("host from LC_SY_HOST: %+v", g)
	}
	for _, v := range []string{self, "garbage", "m_short", strings.ToLower(laptop), laptop + "x", "m_" + strings.Repeat("U", 26), "../" + laptop, ""} {
		g := Term(env(map[string]string{"TERM_PROGRAM": "WarpTerminal", "LC_SY_HOST": v}), self)
		if g == nil || g.Host != "" {
			t.Errorf("LC_SY_HOST %q: %+v", v, g)
		}
	}
}

// End to end through Run: a hook on a server with LC_SY_HOST of another machine records
// term.host; its own id leaves it absent.
func TestRunRecordsTermHost(t *testing.T) {
	const laptop = "m_01HZX3K9Q2M8R5T7V9WBCD4EFG"
	in, _ := os.ReadFile(filepath.Join(fixtures, "hooks", "by-event", "Stop.json"))

	sb := newSandbox(t)
	sb.env["LC_SY_HOST"] = laptop
	sb.run("Stop", in)
	recs := sb.records()
	if len(recs) != 1 || recs[0].Term == nil || recs[0].Term.Host != laptop {
		t.Fatalf("term.host: %+v", recs)
	}

	sb = newSandbox(t)
	sb.run("Stop", in) // creates the machine id
	own := sb.records()[0].Machine
	sb.env["LC_SY_HOST"] = own
	sb.run("Stop", in)
	var last *record.Record
	for _, r := range sb.records() {
		last = r
	}
	if last.Term != nil && last.Term.Host != "" {
		t.Errorf("own id kept as host: %+v", last.Term)
	}
}

func TestHelpIsNotAPayload(t *testing.T) {
	sb := newSandbox(t)
	for _, a := range []string{"--help", "-h", "help"} {
		Run([]string{a}, strings.NewReader(""), sb.getenv)
	}
	if _, err := os.Stat(sb.state()); err == nil {
		t.Errorf("help touched state: %s", sb.errorLog())
	}
}
