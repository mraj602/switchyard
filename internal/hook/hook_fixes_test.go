package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (sb *sandbox) refreshes() int {
	n := 0
	for _, a := range sb.spawns {
		if len(a) == 2 && a[0] == "hook" && a[1] == refreshArg {
			n++
		}
	}
	return n
}

// The auth-refresh debounce is per slot: a refresh started for one slot must not silence
// another slot's.
func TestRefreshDebounceIsPerSlot(t *testing.T) {
	sb := newSandbox(t)
	in, _ := os.ReadFile(filepath.Join(fixtures, "hooks", "by-event", "SessionStart.json"))
	sb.run("SessionStart", in)
	sb.run("SessionStart", in)
	if n := sb.refreshes(); n != 1 {
		t.Fatalf("slot A: %d refreshes, want 1", n)
	}
	other := filepath.Join(sb.home, ".claude-9")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	sb.env["CLAUDE_CONFIG_DIR"] = other
	sb.run("SessionStart", in)
	sb.run("SessionStart", in)
	if n := sb.refreshes(); n != 2 {
		t.Fatalf("after slot B: %d refreshes, want 2 (one per slot)", n)
	}
}

// The hook debounces on <state>/outbox/flush.stamp (outbox.md), the file the flusher
// touches, not on a second stamp of its own.
func TestFlushStampIsTheOutboxOne(t *testing.T) {
	sb := newSandbox(t)
	in, _ := os.ReadFile(filepath.Join(fixtures, "hooks", "by-event", "Stop.json"))
	sb.run("Stop", in)
	if _, err := os.Stat(filepath.Join(sb.state(), "outbox", "flush.stamp")); err != nil {
		t.Fatalf("no outbox/flush.stamp: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sb.state(), "flush.stamp")); err == nil {
		t.Fatal("stray <state>/flush.stamp")
	}
}

// A config error is logged, and an unrelated bad key does not re-enable last_message.
func TestConfigErrorLoggedAndPrivacyKept(t *testing.T) {
	sb := newSandbox(t)
	cfg := filepath.Join(sb.home, "wy", "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[privacy]\nlast_message = false\n[outbox]\nmax_mb = \"lots\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, _ := os.ReadFile(filepath.Join(fixtures, "hooks", "by-event", "Stop.json"))
	sb.run("Stop", in)
	recs := sb.records()
	if len(recs) != 1 {
		t.Fatalf("records: %d", len(recs))
	}
	if _, ok := recs[0].Derived["last_message"]; ok {
		t.Error("last_message captured despite privacy.last_message = false")
	}
	if got := sb.errorLog(); !strings.Contains(got, "config") || !strings.Contains(got, "max_mb") {
		t.Errorf("config error not logged: %q", got)
	}
}
