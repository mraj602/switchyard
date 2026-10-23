package statusline

import (
	"encoding/json"
	"testing"
)

// A `claude -p` started from inside another session inherits that session's environment.
// The status line's session is the one in its stdin JSON, never an inherited variable.
func TestSamplePayloadSessionWinsOverInheritedEnv(t *testing.T) {
	sb := newSandbox(t)
	sb.env["CLAUDECODE"] = "1"
	sb.env["CLAUDE_CODE_SESSION_ID"] = "sess-parent"
	sb.env["CLAUDE_SESSION_ID"] = "sess-parent"
	var in map[string]any
	if err := json.Unmarshal(fixture(t, "full.json"), &in); err != nil {
		t.Fatal(err)
	}
	in["session_id"] = "sess-child"
	b, _ := json.Marshal(in)
	sb.run(b)
	recs := sb.samples()
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(recs[0].Payload, &p); err != nil || p.SessionID != "sess-child" {
		t.Fatalf("sample is about %q (%v), want the payload's sess-child", p.SessionID, err)
	}
	if got := ParseValues(b).SessionID; got != "sess-child" {
		t.Errorf("ParseValues session = %q", got)
	}
}
