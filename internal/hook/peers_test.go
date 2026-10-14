package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/peers"
)

func enablePeers(t *testing.T, sb *sandbox) {
	t.Helper()
	cfg := filepath.Join(sb.home, "sy", "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[peers]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHookWritesPeersSessionFileOnlyWhenEnabled(t *testing.T) {
	sb := newSandbox(t)
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-p1","permission_mode":"bypassPermissions","prompt":"do the thing SECRET-PROMPT"}`))
	if _, err := os.Stat(peers.SessionsDir(sb.state())); err == nil {
		t.Fatal("peers disabled: the hook must not write a session file")
	}

	enablePeers(t, sb)
	sb.run("SessionStart", []byte(`{"session_id":"sess-p1","permission_mode":"default"}`))
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-p1","permission_mode":"bypassPermissions","prompt":"do the thing SECRET-PROMPT"}`))
	sf, ok := peers.ReadSessionFile(sb.state(), "sess-p1")
	if !ok {
		t.Fatal("no session file")
	}
	if sf.Mode != "bypassPermissions" || sf.HumanAt == nil || sf.ConfigDir != "~/.claude-3" || sf.V != 1 {
		t.Errorf("session file: %+v", sf)
	}
	b, _ := os.ReadFile(filepath.Join(peers.SessionsDir(sb.state()), "sess-p1.json"))
	if strings.Contains(string(b), "SECRET-PROMPT") {
		t.Error("the prompt must never be stored")
	}
	// A cross-session message that started a turn is not a human prompt.
	before := *sf.HumanAt
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-p1","permission_mode":"bypassPermissions","prompt":"<cross-session-message from=\"x\">"}`))
	sf, _ = peers.ReadSessionFile(sb.state(), "sess-p1")
	if !sf.HumanAt.Equal(before) {
		t.Error("a peer message moved human_at")
	}
	sb.run("SessionEnd", []byte(`{"session_id":"sess-p1"}`))
	if sf, _ = peers.ReadSessionFile(sb.state(), "sess-p1"); sf.EndedAt == nil {
		t.Error("SessionEnd must set ended_at")
	}
}

func TestHookIgnoresUnsafeSessionID(t *testing.T) {
	sb := newSandbox(t)
	enablePeers(t, sb)
	sb.run("SessionStart", []byte(`{"session_id":"../../evil","permission_mode":"default"}`))
	if _, err := os.Stat(peers.SessionsDir(sb.state())); err == nil {
		t.Error("an unsafe session id must write nothing")
	}
}
