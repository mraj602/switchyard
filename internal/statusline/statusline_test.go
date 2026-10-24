package statusline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

const fixtures = "../../testdata/statusline"

type sandbox struct {
	t    *testing.T
	home string
	env  map[string]string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("WY_HOME", filepath.Join(home, "wy"))
	t.Setenv("WY_STATE_DIR", "")
	t.Setenv("WY_CONFIG_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	slot := filepath.Join(home, ".claude-2")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	oauth, err := os.ReadFile("../../testdata/auth/oauth-2.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, ".claude.json"), oauth, 0o600); err != nil {
		t.Fatal(err)
	}
	return &sandbox{t: t, home: home, env: map[string]string{"CLAUDE_CONFIG_DIR": slot, "HOME": home, "WY_HOME": filepath.Join(home, "wy")}}
}

func (sb *sandbox) getenv(k string) string { return sb.env[k] }

func (sb *sandbox) run(in []byte, args ...string) string {
	sb.t.Helper()
	var out bytes.Buffer
	if err := Run(args, bytes.NewReader(in), &out, os.Stderr, sb.getenv); err != nil {
		sb.t.Fatal(err)
	}
	return out.String()
}

func (sb *sandbox) samples() []*record.Record {
	var out []*record.Record
	files, _ := filepath.Glob(filepath.Join(sb.home, "wy", "state", "outbox", "*.jsonl"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var r record.Record
			if err := json.Unmarshal(line, &r); err != nil {
				sb.t.Fatal(err)
			}
			out = append(out, &r)
		}
	}
	return out
}

func (sb *sandbox) backdate(d time.Duration) {
	p := filepath.Join(sb.home, "wy", "state", "statusline", stateName)
	b, err := os.ReadFile(p)
	if err != nil {
		sb.t.Fatal(err)
	}
	m := map[string]seen{}
	if err := json.Unmarshal(b, &m); err != nil {
		sb.t.Fatal(err)
	}
	for k, v := range m {
		v.At = record.FormatTime(time.Now().Add(-d))
		m[k] = v
	}
	b, _ = json.Marshal(m)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		sb.t.Fatal(err)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// plain removes the dim/reset escapes so tests can compare text.
func plain(s string) string {
	for _, e := range []string{dim, reset} {
		s = strings.ReplaceAll(s, e, "")
	}
	return s
}

func TestFormatOnFixtures(t *testing.T) {
	sb := newSandbox(t)
	got := plain(sb.run(fixture(t, "full.json")))
	if got != "claude-2 · 5h 24% · 7d 41% · ctx 9%\n" {
		t.Errorf("full: %q", got)
	}
	got = plain(sb.run(fixture(t, "early-session.json")))
	if got != "claude-2\n" {
		t.Errorf("early: %q", got)
	}
	got = plain(sb.run(fixture(t, "limit-reached-1m-context.json")))
	if !strings.HasPrefix(got, "claude-2 · 5h 100% · 7d ") || !strings.Contains(got, "ctx ") {
		t.Errorf("limit reached: %q", got)
	}
}

func TestLabelFromConfig(t *testing.T) {
	sb := newSandbox(t)
	cfg := filepath.Join(sb.home, "wy", "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("version = 1\n[slots]\nlabels = { \"~/.claude-2\" = \"work\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := plain(sb.run(fixture(t, "full.json")))
	if !strings.HasPrefix(got, "work · 5h") {
		t.Errorf("label: %q", got)
	}
}

func TestWrapOutputIsKept(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	sb := newSandbox(t)
	in := fixture(t, "full.json")

	// The wrapped output (colours and all) comes first, unchanged, on the same line.
	wrapped := "\x1b[32mmodel Opus\x1b[0m $1.23"
	out := sb.run(in, "--wrap", `printf '\033[32mmodel Opus\033[0m $1.23\n'`)
	if !strings.HasPrefix(out, wrapped) {
		t.Fatalf("wrapped prefix lost: %q", out)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("want exactly one line: %q", out)
	}
	if !strings.Contains(plain(out), "$1.23 │ claude-2 · 5h 24%") {
		t.Fatalf("separator: %q", plain(out))
	}

	// The wrapped command got the same stdin.
	out = sb.run(in, "--wrap", `cat | head -c 40 | tr -d '\n '`)
	if !strings.HasPrefix(plain(out), `{"session_id":"11111`) {
		t.Fatalf("stdin not passed: %q", out)
	}

	// Bytes that are not text survive.
	out = sb.run(in, "--wrap", `printf 'a\000b\377c'`)
	if !strings.HasPrefix(out, "a\x00b\xffc") {
		t.Fatalf("binary bytes: %q", out)
	}
}

func TestWrapFailureDoesNotBreakTheLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	sb := newSandbox(t)
	in := fixture(t, "full.json")
	want := "claude-2 · 5h 24% · 7d 41% · ctx 9%\n"

	if got := plain(sb.run(in, "--wrap", "exit 3")); got != want {
		t.Errorf("exit 3: %q", got)
	}
	if got := plain(sb.run(in, "--wrap", "/definitely/not/a/command")); got != want {
		t.Errorf("missing command: %q", got)
	}
	// Output printed before a non-zero exit is still shown.
	if got := plain(sb.run(in, "--wrap", "printf partial; exit 1")); !strings.HasPrefix(got, "partial │ claude-2") {
		t.Errorf("partial: %q", got)
	}
	start := time.Now()
	// A session with no cached good output shows nothing of the wrapped part.
	fresh := bytes.Replace(in, []byte("11111111-2222"), []byte("99999999-2222"), 1)
	got := plain(sb.run(fresh, "--wrap", "sleep 10; echo late"))
	if got != want {
		t.Errorf("timeout: %q", got)
	}
	if d := time.Since(start); d > WrapTimeout+time.Second {
		t.Errorf("timeout took %v", d)
	}
}

func TestNoWrapPrintsOnlyOurPart(t *testing.T) {
	sb := newSandbox(t)
	got := sb.run(fixture(t, "full.json"))
	if strings.Contains(got, "│") || strings.Count(got, "\n") != 1 {
		t.Errorf("%q", got)
	}
}

func TestGarbageInputStillPrintsALine(t *testing.T) {
	sb := newSandbox(t)
	if got := plain(sb.run([]byte("nope"))); got != "claude-2\n" {
		t.Errorf("%q", got)
	}
	if got := plain(sb.run(nil)); got != "claude-2\n" {
		t.Errorf("%q", got)
	}
}

func TestLimitSampleRules(t *testing.T) {
	sb := newSandbox(t)
	in := fixture(t, "full.json")

	sb.run(in)
	recs := sb.samples()
	if len(recs) != 1 {
		t.Fatalf("first run: %d records", len(recs))
	}
	r := recs[0]
	if r.Kind != record.KindLimitSample || r.Slot != "~/.claude-2" || r.Seat == nil || r.Seat.Account != "aaaaaaaa-0000-4000-8000-000000000002" {
		t.Fatalf("record %+v", r)
	}
	var d struct {
		Five  float64 `json:"five_hour_pct"`
		Seven float64 `json:"seven_day_pct"`
		Ctx   float64 `json:"context_pct"`
	}
	b, _ := json.Marshal(r.Derived)
	_ = json.Unmarshal(b, &d)
	if d.Five != 23.5 || d.Seven != 41.2 || d.Ctx != 9 {
		t.Fatalf("derived %s", b)
	}

	// Same values, a minute later: nothing. Within the minute with new values: nothing.
	sb.backdate(2 * time.Minute)
	sb.run(in)
	if n := len(sb.samples()); n != 1 {
		t.Fatalf("unchanged value wrote a record (%d)", n)
	}
	changed := bytes.Replace(in, []byte(`"used_percentage": 23.5`), []byte(`"used_percentage": 25`), 1)
	sb.backdate(10 * time.Second)
	sb.run(changed)
	if n := len(sb.samples()); n != 1 {
		t.Fatalf("record inside the minute (%d)", n)
	}
	// Changed and older than a minute: one more.
	sb.backdate(2 * time.Minute)
	sb.run(changed)
	if n := len(sb.samples()); n != 2 {
		t.Fatalf("changed value after a minute: %d records", n)
	}
	// Inside the minute, a value that reaches an alert mark (80, 95, 100) is written at once;
	// one that moves without reaching a new mark still waits.
	at80 := bytes.Replace(in, []byte(`"used_percentage": 23.5`), []byte(`"used_percentage": 81`), 1)
	sb.backdate(10 * time.Second)
	sb.run(at80)
	if n := len(sb.samples()); n != 3 {
		t.Fatalf("crossing 80%% inside the minute: %d records, want 3", n)
	}
	at90 := bytes.Replace(in, []byte(`"used_percentage": 23.5`), []byte(`"used_percentage": 90`), 1)
	sb.backdate(10 * time.Second)
	sb.run(at90)
	if n := len(sb.samples()); n != 3 {
		t.Fatalf("81 -> 90 inside the minute: %d records, want still 3", n)
	}
	at96 := bytes.Replace(in, []byte(`"used_percentage": 23.5`), []byte(`"used_percentage": 96`), 1)
	sb.backdate(10 * time.Second)
	sb.run(at96)
	if n := len(sb.samples()); n != 4 {
		t.Fatalf("crossing 95%% inside the minute: %d records, want 4", n)
	}
	// A different session samples at once.
	other := bytes.Replace(in, []byte("11111111-2222-4333-8444-555555555555"), []byte("99999999-2222-4333-8444-555555555555"), 1)
	sb.run(other)
	if n := len(sb.samples()); n != 5 {
		t.Fatalf("new session: %d records", n)
	}
}

func TestParseValues(t *testing.T) {
	v := ParseValues(fixture(t, "early-session.json"))
	if v.FiveHour != nil || v.SevenDay != nil || v.Context != nil || v.SessionID == "" {
		t.Errorf("%+v", v)
	}
	if v := ParseValues([]byte(`{"rate_limits":{"five_hour":{"used_percentage":"x"}}}`)); v != (Values{}) {
		t.Errorf("wrong type should give empty values: %+v", v)
	}
}
