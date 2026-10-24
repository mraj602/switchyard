package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mraj602/watchyard/internal/peers"
)

// inheritParent gives the sandbox the environment a `claude -p` started from inside another
// session's Bash tool inherits: the parent's session markers. None of them may decide which
// session a hook is about; the payload does.
func inheritParent(sb *sandbox) {
	for k, v := range map[string]string{
		"CLAUDECODE":                   "1",
		"CLAUDE_CODE_ENTRYPOINT":       "cli",
		"CLAUDE_CODE_SESSION_ID":       "sess-parent",
		"CLAUDE_SESSION_ID":            "sess-parent",
		"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/never/cc-socks/1.sock",
	} {
		sb.env[k] = v
	}
}

func TestHookPayloadSessionWinsOverInheritedEnv(t *testing.T) {
	sb := newSandbox(t)
	enablePeers(t, sb)
	inheritParent(sb)
	child := filepath.Join(sb.home, ".claude-3", "projects", "p", "sess-child.jsonl")
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		b, _ := json.Marshal(map[string]any{"session_id": "sess-child", "transcript_path": child, "cwd": sb.home,
			"permission_mode": "default", "prompt": "hi", "last_assistant_message": "done", "source": "startup"})
		sb.run(ev, b)
	}
	recs := sb.ofKind("hook_event")
	if len(recs) != 4 {
		t.Fatalf("%d hook records, want 4", len(recs))
	}
	for _, r := range recs {
		var p struct {
			SessionID      string `json:"session_id"`
			TranscriptPath string `json:"transcript_path"`
		}
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.SessionID != "sess-child" || p.TranscriptPath != child {
			t.Errorf("record %s is about %q (%s), want the payload's sess-child", r.ID, p.SessionID, p.TranscriptPath)
		}
	}
	if _, ok := peers.ReadSessionFile(sb.state(), "sess-child"); !ok {
		t.Error("the peers session file must be the payload's session")
	}
	if _, ok := peers.ReadSessionFile(sb.state(), "sess-parent"); ok {
		t.Error("the inherited parent session id must never get a session file")
	}
}

func TestTitleHookPayloadSessionWinsOverInheritedEnv(t *testing.T) {
	sb := titleSandbox(t, "")
	inheritParent(sb)
	sb.title("UserPromptSubmit", payload("sess-child", sb.home, "UserPromptSubmit", nil))
	dir := filepath.Join(sb.state(), "term")
	if _, err := os.Stat(filepath.Join(dir, "title-sess-parent.json")); err == nil {
		t.Error("the title hook folded the event onto the inherited parent session")
	}
	if _, err := os.Stat(filepath.Join(dir, "title-sess-child.json")); err != nil {
		t.Errorf("no title file for the payload's session: %v", err)
	}
}
