package identity

import (
	"path/filepath"
	"reflect"
	"testing"
)

// CLAUDE_CONFIG_DIR explicitly set to ~/.claude: Claude Code reads ~/.claude/.claude.json
// (testdata/README.md), so the default-slot rule must not apply.
func TestExplicitClaudeDirIsNotTheDefaultSlot(t *testing.T) {
	home := fakeHome(t)
	l := Locator{Home: home, FS: OSFS{}}
	def := filepath.Join(home, ".claude")
	env := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return def
		}
		return ""
	}
	s := l.Current(env)
	if s.Default || !s.Explicit {
		t.Fatalf("explicit ~/.claude must not be Default: %+v", s)
	}
	if want := filepath.Join(def, ".claude.json"); s.StateFile != want {
		t.Errorf("state file %q, want %q", s.StateFile, want)
	}
	if s.Name != "~/.claude" {
		t.Errorf("name %q", s.Name)
	}
	if got := AuthEnv([]string{"A=1"}, s); !reflect.DeepEqual(got, []string{"A=1", "CLAUDE_CONFIG_DIR=" + def}) {
		t.Errorf("AuthEnv keeps the variable for the explicit view: %v", got)
	}
	// Unset keeps the default rule.
	if u := l.Current(noEnv); !u.Default || u.Explicit || u.StateFile != filepath.Join(home, ".claude.json") {
		t.Errorf("unset: %+v", u)
	}
	if s.Key() == l.Current(noEnv).Key() {
		t.Error("the two views need separate login cache entries")
	}
	// Discover still lists ~/.claude once, as the default slot.
	slots, err := l.Discover(nil, env)
	if err != nil || !slots[0].Default || slots[0].Explicit {
		t.Fatalf("discover: %+v %v", slots[0], err)
	}
}

func TestExplicitAndDefaultViewsKeepSeparateLogins(t *testing.T) {
	home := fakeHome(t)
	// ~/.claude/.claude.json is a different login (oauth-2) from ~/.claude.json (oauth-1).
	write(t, filepath.Join(home, ".claude", ".claude.json"), fixture(t, "oauth-2.json"))
	l := Locator{Home: home, FS: OSFS{}}
	st := &Store{StateDir: t.TempDir(), FS: OSFS{}}
	explicit := l.Current(func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(home, ".claude")
		}
		return ""
	})
	def := l.Current(noEnv)
	for i := 0; i < 2; i++ { // twice: the second round reads the caches
		a, err := st.Login(def)
		if err != nil {
			t.Fatal(err)
		}
		b, err := st.Login(explicit)
		if err != nil {
			t.Fatal(err)
		}
		if !a.HasSeat || !b.HasSeat || a.Seat.Account == b.Seat.Account {
			t.Fatalf("round %d: logins mixed up: %+v %+v", i, a.Seat, b.Seat)
		}
	}
}
