// Package statusline implements `sy statusline --wrap <cmd>`: print the account label,
// 5-hour and 7-day use and context %, run and print the wrapped command, and write a
// limit_sample record at most once a minute per session when a value changes.
//
// Contract: docs/spec/record.md 4.2.
//
// Depends on: record, outbox, identity, config, paths.
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/atomicfile"
	"github.com/mraj602/switchyard/internal/clientenv"
	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/record"
)

const (
	// WrapTimeout is how long the wrapped command may run before it is dropped.
	WrapTimeout = 2 * time.Second
	// SampleEvery is the least time between two limit_sample records of one session.
	SampleEvery = time.Minute

	maxStdin  = 4 << 20
	stateName = "last.json"
	keepFor   = 48 * time.Hour

	dim   = "\x1b[2m"
	reset = "\x1b[0m"
)

// Run is `sy statusline [--wrap <cmd>]`. It reads the status-line JSON from stdin and
// prints one line to stdout. Whatever goes wrong inside, the line is still printed.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) error {
	fs := flag.NewFlagSet("statusline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	wrap := fs.String("wrap", "", "command whose output is printed first (it gets the same stdin)")
	parseErr := fs.Parse(args)
	if parseErr != nil {
		// A bad flag must not blank the user's status line: find --wrap by hand.
		*wrap = findWrap(args)
	}
	in, _ := io.ReadAll(io.LimitReader(stdin, maxStdin))

	var wrapped <-chan []byte
	if *wrap != "" {
		ch := make(chan []byte, 1)
		wrapped = ch
		go func() { ch <- runWrapped(*wrap, in, getenv) }()
	}

	part, show := switchyardPart(in, getenv)

	var out []byte
	if wrapped != nil {
		out = trimNewlines(<-wrapped)
	}
	switch {
	case !show: // statusline.show = false: the wrapped line alone
		out = append(out, '\n')
	case len(out) > 0:
		out = append(out, reset+" "+dim+"│ "...)
		out = append(out, part+reset+"\n"...)
	default:
		out = append(out, dim+part+reset+"\n"...)
	}
	if _, err := stdout.Write(out); err != nil {
		return err
	}
	return parseErr
}

// findWrap digs the --wrap value out of arguments the flag parser rejected.
func findWrap(args []string) string {
	for i, a := range args {
		for _, pre := range []string{"--wrap", "-wrap"} {
			if a == pre && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, pre+"=") {
				return strings.TrimPrefix(a, pre+"=")
			}
		}
	}
	return ""
}

// trimNewlines drops all trailing line endings of the wrapped output (a script that
// prints a blank line after its text must not push our part onto its own line); every
// other byte, trailing spaces included, is kept as it was.
func trimNewlines(b []byte) []byte {
	return bytes.TrimRight(b, "\r\n")
}

// shellCommand: Claude Code runs status-line commands through a POSIX shell (Git Bash on
// Windows), so sh is used wherever it is on PATH; cmd /c is only the Windows fallback.
//
// On Unix the shell runs in its own process group and a timeout kills the whole group, so
// grandchildren do not outlive it (see procgroup_unix.go).
func shellCommand(ctx context.Context, cmdline string) *exec.Cmd {
	var c *exec.Cmd
	if sh, err := exec.LookPath("sh"); err == nil {
		c = exec.CommandContext(ctx, sh, "-c", cmdline)
	} else if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/c", cmdline)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", cmdline)
	}
	ownProcessGroup(c)
	return c
}

const maxCache = 64 << 10

// cachePath is <state>/statusline/wrapped-<session>.txt, or "" when there is no usable
// state folder or session id.
func cachePath(getenv func(string) string, session string) string {
	if session == "" {
		return ""
	}
	state, err := clientenv.StateDir(getenv)
	if err != nil || state == "" {
		return ""
	}
	var id strings.Builder
	for _, r := range session {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			id.WriteRune(r)
		}
	}
	if id.Len() == 0 || id.Len() > 80 {
		return ""
	}
	return filepath.Join(state, "statusline", "wrapped-"+id.String()+".txt")
}

// storeCache keeps the last good wrapped output and drops caches older than keepFor.
func storeCache(path string, b []byte) {
	if len(b) > maxCache {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	_ = atomicfile.Write(path, b, 0o600)
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), "wrapped-") {
				continue
			}
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > keepFor {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// runWrapped runs cmd through the shell with stdin as given and returns its stdout. A
// command that fails still has its output used (a status line script may exit 1 after
// printing), and so does one that left a background child holding the pipe open
// (exec.ErrWaitDelay). One that takes more than WrapTimeout yields the last good output
// of this session, cached in the state folder; one that cannot start yields nothing.
func runWrapped(cmdline string, stdin []byte, getenv func(string) string) []byte {
	cache := cachePath(getenv, ParseValues(stdin).SessionID)
	ctx, cancel := context.WithTimeout(context.Background(), WrapTimeout)
	defer cancel()
	c := shellCommand(ctx, cmdline)
	c.Stdin = bytes.NewReader(stdin)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = nil
	// A shell that was killed leaves its children holding the pipe; do not wait for them.
	c.WaitDelay = 200 * time.Millisecond
	err := c.Run()
	if ctx.Err() != nil {
		if cache != "" {
			if b, err := os.ReadFile(cache); err == nil {
				return b
			}
		}
		return nil
	}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) && !errors.Is(err, exec.ErrWaitDelay) {
			return nil // could not start
		}
	}
	if cache != "" && buf.Len() > 0 {
		storeCache(cache, buf.Bytes())
	}
	return buf.Bytes()
}

// Values are the numbers the status line shows. A nil field was absent or null.
type Values struct {
	SessionID string
	FiveHour  *float64
	SevenDay  *float64
	Context   *float64
}

// ParseValues reads the fields of record.md 4.2 from the status-line JSON; a payload that
// is not JSON gives empty Values.
func ParseValues(in []byte) Values {
	var p struct {
		SessionID  string `json:"session_id"`
		RateLimits struct {
			FiveHour struct {
				Used *float64 `json:"used_percentage"`
			} `json:"five_hour"`
			SevenDay struct {
				Used *float64 `json:"used_percentage"`
			} `json:"seven_day"`
		} `json:"rate_limits"`
		ContextWindow struct {
			Used *float64 `json:"used_percentage"`
		} `json:"context_window"`
	}
	if json.Unmarshal(in, &p) != nil {
		return Values{}
	}
	return Values{SessionID: p.SessionID, FiveHour: p.RateLimits.FiveHour.Used,
		SevenDay: p.RateLimits.SevenDay.Used, Context: p.ContextWindow.Used}
}

// Format renders the Switchyard part: "label · 5h 24% · 7d 41% · ctx 9%". Values that are
// absent are left out.
func Format(label string, v Values) string {
	parts := []string{label}
	add := func(name string, p *float64) {
		if p != nil {
			parts = append(parts, fmt.Sprintf("%s %d%%", name, int(math.Round(*p))))
		}
	}
	add("5h", v.FiveHour)
	add("7d", v.SevenDay)
	add("ctx", v.Context)
	return strings.Join(parts, " · ")
}

// switchyardPart computes the text and whether to show it (statusline.show) and, as a side
// effect, records a limit_sample when the rules say so.
func switchyardPart(in []byte, getenv func(string) string) (string, bool) {
	v := ParseValues(in)
	env, err := clientenv.Load(getenv)
	if err != nil {
		return Format(defaultLabel(getenv("CLAUDE_CONFIG_DIR")), v), true
	}
	label := env.Config.Slots.Labels[env.Slot.Name]
	if label == "" {
		label = defaultLabel(env.Slot.Dir)
	}
	if len(in) > 0 {
		sample(env, in, v, time.Now())
	}
	return Format(label, v), env.Config.Statusline.Show
}

// defaultLabel is the folder name without its leading dot: "~/.claude-2" -> "claude-2".
func defaultLabel(dir string) string {
	if dir == "" {
		return "claude"
	}
	return strings.TrimLeft(filepath.Base(dir), ".")
}

type seen struct {
	At       string   `json:"at"`
	FiveHour *float64 `json:"five_hour_pct"`
	SevenDay *float64 `json:"seven_day_pct"`
}

// alertMarks are the percentages the hub alerts on (push.md: limit_80, limit_95) and the
// limit itself. A sample that reaches one of them for the first time is written at once.
var alertMarks = []float64{80, 95, 100}

// crosses reports whether cur reached an alert mark that prev was below. Without this a
// session whose last status lines of a turn go from 81% to 96% within the minute would leave
// the hub at 81% (and no 95% alert) until it next draws its status line.
func crosses(prev, cur *float64) bool {
	if prev == nil || cur == nil {
		return false
	}
	for _, m := range alertMarks {
		if *prev < m && *cur >= m {
			return true
		}
	}
	return false
}

func equal(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sample writes a limit_sample on the first run of a session and then at most once per
// SampleEvery, only when the 5-hour or 7-day value changed. State: <state>/statusline/last.json.
// Every error is swallowed: the line must still print.
func sample(env *clientenv.Env, in []byte, v Values, now time.Time) {
	id := v.SessionID
	if id == "" {
		id = "unknown"
	}
	dir := filepath.Join(env.Dirs.State.Path, "statusline")
	path := filepath.Join(dir, stateName)
	last := map[string]seen{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	prev, known := last[id]
	if known {
		at, err := time.Parse(time.RFC3339Nano, prev.At)
		crossed := crosses(prev.FiveHour, v.FiveHour) || crosses(prev.SevenDay, v.SevenDay)
		if err == nil && now.Sub(at) < SampleEvery && now.Sub(at) >= 0 && !crossed {
			return
		}
		if equal(prev.FiveHour, v.FiveHour) && equal(prev.SevenDay, v.SevenDay) {
			return
		}
	}

	rec, err := record.NewLimitSample(env.Meta(nil), in)
	if err != nil {
		return
	}
	ob := outbox.New(env.OutboxDir())
	ob.NoFsync = !env.Config.Outbox.Fsync
	if _, err := ob.Append(rec); err != nil {
		return
	}

	last[id] = seen{At: record.FormatTime(now), FiveHour: v.FiveHour, SevenDay: v.SevenDay}
	for k, s := range last {
		if t, err := time.Parse(time.RFC3339Nano, s.At); err != nil || now.Sub(t) > keepFor {
			delete(last, k)
		}
	}
	if b, err := json.Marshal(last); err == nil {
		if os.MkdirAll(dir, 0o700) == nil {
			_ = atomicfile.Write(path, append(b, '\n'), 0o600)
		}
	}
}
