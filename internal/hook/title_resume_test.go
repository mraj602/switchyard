package hook

import (
	"strings"
	"testing"
	"time"
)

// SessionStart prints even when the title equals the last one printed for the session: a
// resumed session in a new tab needs it.
func TestTitleSessionStartAlwaysPrints(t *testing.T) {
	sb := titleSandbox(t, "")
	p := payload("s1", "/x/api", "SessionStart", map[string]any{"source": "resume"})
	if got := sb.title("SessionStart", p); got != seq("work · api · done") {
		t.Fatalf("first: %q", got)
	}
	if got := sb.title("SessionStart", p); got != seq("work · api · done") {
		t.Errorf("second SessionStart printed %q", got)
	}
}

// A compaction while a turn is working keeps "working".
func TestTitleCompactKeepsTheWord(t *testing.T) {
	sb := titleSandbox(t, "")
	sb.title("UserPromptSubmit", payload("s1", "/x/api", "UserPromptSubmit", nil))
	got := sb.title("SessionStart", payload("s1", "/x/api", "SessionStart", map[string]any{"source": "compact"}))
	if got != seq("work · api · working") {
		t.Errorf("compact mid-turn: %q", got)
	}
	sb.title("Stop", payload("s1", "/x/api", "Stop", nil))
	got = sb.title("SessionStart", payload("s1", "/x/api", "SessionStart", map[string]any{"source": "compact"}))
	if got != seq("work · api · done") {
		t.Errorf("compact when done: %q", got)
	}
}

type neverEOF struct{ done chan struct{} }

func (n neverEOF) Read([]byte) (int, error) { <-n.done; return 0, nil }

// The title hook gives up on a stdin that never closes well inside the hook's 1 s timeout.
func TestTitleStdinWaitUnderHookTimeout(t *testing.T) {
	sb := titleSandbox(t, "")
	done := make(chan struct{})
	defer close(done)
	var out strings.Builder
	start := time.Now()
	RunTitle([]string{"Stop"}, neverEOF{done}, &out, sb.getenv)
	if d := time.Since(start); d >= time.Second {
		t.Errorf("waited %s for stdin", d)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q", out.String())
	}
}
