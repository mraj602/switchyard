package api_test

import (
	"testing"
	"time"
)

func TestFutureTimestampDoesNotFreezeSession(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	sid := "11111111-2222-3333-4444-555555555555"
	now := time.Now().UTC()
	h.ingest(tok, simpleHook(t, h.machine, sid, "SessionStart", now.Add(-time.Minute)))
	// clock briefly 1 day ahead
	h.ingest(tok, simpleHook(t, h.machine, sid, "Stop", now.Add(24*time.Hour)))
	// clock corrected: user sends a prompt, Claude is working
	h.ingest(tok, simpleHook(t, h.machine, sid, "UserPromptSubmit", now.Add(time.Second)))
	h.ingest(tok, simpleHook(t, h.machine, sid, "PreToolUse", now.Add(2*time.Second)))
	var s struct {
		State string `json:"state"`
	}
	h.get("/v1/sessions/"+sid, h.token("desktop"), &s)
	t.Logf("state after real UserPromptSubmit+PreToolUse: %s", s.State)
	if s.State != "working" {
		t.Errorf("state wedged by a future-dated record: %s", s.State)
	}
}
