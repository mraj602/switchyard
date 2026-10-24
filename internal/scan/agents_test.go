package scan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../testdata/agents", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeClaude answers `claude agents --json` from the real redacted outputs, by the slot in
// CLAUDE_CONFIG_DIR (unset or the default folder = ~/.claude).
type fakeClaude struct {
	home string
	mu   sync.Mutex
	envs []string // CLAUDE_CONFIG_DIR of each call ("" = unset)
	fail map[string]bool
	t    *testing.T
}

func (f *fakeClaude) run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	if name != "claude" || strings.Join(args, " ") != "agents --json" {
		f.t.Errorf("unexpected command %s %v", name, args)
	}
	cfg := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			cfg = v
		}
	}
	f.mu.Lock()
	f.envs = append(f.envs, cfg)
	f.mu.Unlock()
	if f.fail[cfg] {
		return nil, errors.New("exit status 1\nboom")
	}
	switch cfg {
	case "":
		return fixture(f.t, "claude-agents-1-default.json"), nil
	case filepath.Join(f.home, ".claude"):
		return fixture(f.t, "claude-agents-1-explicit.json"), nil
	case filepath.Join(f.home, ".claude-2"):
		return fixture(f.t, "claude-agents-2.json"), nil
	case filepath.Join(f.home, ".claude-3"):
		return fixture(f.t, "claude-agents-3.json"), nil
	}
	return []byte("[]"), nil
}

func agentsHome(t *testing.T) (string, []identity.Slot) {
	t.Helper()
	home := t.TempDir()
	for _, d := range []string{".claude", ".claude-2", ".claude-3"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, d, "settings.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loc := identity.Locator{Home: home, FS: identity.OSFS{}}
	slots, err := loc.Discover(nil, func(string) string { return "" })
	if err != nil || len(slots) != 3 {
		t.Fatalf("slots %v %v", slots, err)
	}
	return home, slots
}

func sessionLists(t *testing.T, ob *outbox.Outbox) map[string]*record.Record {
	t.Helper()
	out := map[string]*record.Record{}
	for _, r := range readAll(t, ob) {
		if r.Kind != record.KindSessionList {
			t.Fatalf("kind %s", r.Kind)
		}
		out[r.Slot] = r
	}
	return out
}

func TestAgentsFromClaude(t *testing.T) {
	home, slots := agentsHome(t)
	fc := &fakeClaude{home: home, t: t}
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	seat := &record.Seat{Account: "a", Org: "o", Src: "claude_json"}
	res, err := Agents(context.Background(), AgentsOptions{Slots: slots, Run: fc.run, Outbox: ob,
		Machine: "m_x", Client: "wy/test", SeatOf: func(identity.Slot) *record.Seat { return seat }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("%d lists", len(res))
	}
	lists := sessionLists(t, ob)
	for slot, want := range map[string]int{"~/.claude": 1, "~/.claude-2": 5, "~/.claude-3": 2} {
		r := lists[slot]
		if r == nil {
			t.Fatalf("no list for %s", slot)
		}
		var p record.SessionListPayload
		if err := r.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if !p.Complete || p.Source != "claude_agents" || p.Error != "" || len(p.Sessions) != want {
			t.Errorf("%s: complete=%v source=%s error=%q sessions=%d (want %d)", slot, p.Complete, p.Source, p.Error, len(p.Sessions), want)
		}
		if r.Seat == nil || r.Seat.Account != "a" || r.Slot != slot || r.Machine != "m_x" {
			t.Errorf("%s envelope %+v", slot, r)
		}
	}
	// The default slot is asked twice: variable unset, and set to itself.
	var gotUnset, gotExplicit bool
	for _, e := range fc.envs {
		if e == "" {
			gotUnset = true
		}
		if e == filepath.Join(home, ".claude") {
			gotExplicit = true
		}
	}
	if !gotUnset || !gotExplicit {
		t.Errorf("default slot must be run unset and explicit, got %q", fc.envs)
	}
	// Items are kept whole, background ones included.
	var p record.SessionListPayload
	_ = lists["~/.claude-2"].DecodePayload(&p)
	var bg, inter int
	for _, raw := range p.Sessions {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		switch m["kind"] {
		case "background":
			bg++
			if m["state"] != "blocked" || m["id"] == nil {
				t.Errorf("background item changed: %v", m)
			}
		case "interactive":
			inter++
			if m["pid"] == nil || m["status"] == nil {
				t.Errorf("interactive item changed: %v", m)
			}
		}
	}
	if bg != 2 || inter != 3 {
		t.Errorf("kinds: %d background, %d interactive", bg, inter)
	}
}

func TestAgentsFallbackNeverOpensKeys(t *testing.T) {
	home, slots := agentsHome(t)
	sdir := filepath.Join(home, ".claude-2", "sessions")
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir("../../testdata/agents/sessions-dir")
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join("../../testdata/agents/sessions-dir", e.Name()))
		if err := os.WriteFile(filepath.Join(sdir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sdir, strings.TrimSuffix(e.Name(), ".json")+".abc123.key"), []byte("SECRET-KEY-MATERIAL"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recordingFS{FS: identity.OSFS{}}
	fc := &fakeClaude{home: home, t: t, fail: map[string]bool{filepath.Join(home, ".claude-2"): true}}
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	if _, err := Agents(context.Background(), AgentsOptions{Slots: slots, Run: fc.run, FS: rec, Outbox: ob, Machine: "m_x"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range rec.opened {
		if strings.HasSuffix(name, ".key") {
			t.Fatalf("opened a key file: %s", name)
		}
	}
	if len(rec.opened) != 3 {
		t.Errorf("opened %v, want the three sessions/*.json", rec.opened)
	}
	r := sessionLists(t, ob)["~/.claude-2"]
	var p record.SessionListPayload
	if err := r.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.Complete || p.Source != "sessions_dir" || len(p.Sessions) != 3 || !strings.Contains(p.Error, "claude agents failed") {
		t.Fatalf("fallback payload: complete=%v source=%s sessions=%d error=%q", p.Complete, p.Source, len(p.Sessions), p.Error)
	}
	// messagingSocketPath is dropped and listed as redacted; nothing of the key leaked.
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "messagingSocketPath\":") || strings.Contains(string(raw), "SECRET-KEY") {
		t.Errorf("record leaks: %s", raw)
	}
	if len(r.Redacted) == 0 || r.Redacted[0] != "sessions[].messagingSocketPath" {
		t.Errorf("the drop is not listed in redacted: %v", r.Redacted)
	}
	// A slot where claude agents works is still complete.
	var q record.SessionListPayload
	_ = sessionLists(t, outbox.New(ob.Dir))["~/.claude-3"].DecodePayload(&q)
	if !q.Complete {
		t.Error("slot 3 should be complete")
	}
}

func TestAgentsDefaultSlotIncompleteIfOneRunFails(t *testing.T) {
	home, slots := agentsHome(t)
	fc := &fakeClaude{home: home, t: t, fail: map[string]bool{filepath.Join(home, ".claude"): true}}
	got := CollectList(context.Background(), slots[0], nil, fc.run, identity.OSFS{})
	if got.Complete || got.Source != "sessions_dir" {
		t.Fatalf("%+v", got)
	}
}

func TestAgentsBadOutput(t *testing.T) {
	_, slots := agentsHome(t)
	bad := func(context.Context, []string, string, ...string) ([]byte, error) { return []byte("not json"), nil }
	got := CollectList(context.Background(), slots[1], nil, bad, identity.OSFS{})
	if got.Complete || !strings.Contains(got.Error, "not a JSON array") {
		t.Fatalf("%+v", got)
	}
}

// recordingFS remembers every Open (even those the inner FS would refuse).
type recordingFS struct {
	identity.FS
	opened []string
}

func (r *recordingFS) Open(name string) (io.ReadCloser, error) {
	r.opened = append(r.opened, name)
	return r.FS.Open(name)
}
