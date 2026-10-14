package peers

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/identity"
)

// Env is everything the local peers code looks at, all injectable so tests run in a temp
// HOME and XDG_RUNTIME_DIR with a fake /proc and never touch the user's real sessions.
type Env struct {
	Home    string // user home folder (slots live directly in it)
	State   string // <state>
	Runtime string // $XDG_RUNTIME_DIR ("" means /run/user/<uid>)
	Machine identity.Machine
	// SlotExtra and SlotLabels are config.toml's slots.extra and slots.labels.
	SlotExtra  []string
	SlotLabels map[string]string
	MaxTurns   int // when the matrix does not say

	Proc     Proc
	StartPID int // where the ancestor walk starts: os.Getppid()
	FS       identity.FS
	Matrix   Matrix
	Now      func() time.Time
	// Alive reports whether a registry's socket accepts a connection (default: the 250 ms
	// connect of peers.md 3.1).
	Alive func(sock string) bool
	// NewID makes a message id (uuid v4); tests fix it.
	NewID func() string
	// Remote reaches other machines through the agent's link (PE2); nil: this machine only.
	Remote Remote
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) alive(sock string) bool {
	if e.Alive != nil {
		return e.Alive(sock)
	}
	return Alive(sock)
}

func (e Env) fs() identity.FS {
	if e.FS != nil {
		return e.FS
	}
	return identity.OSFS{}
}

func (e Env) locator() identity.Locator { return identity.Locator{Home: e.Home, FS: e.fs()} }

// Slots are the config folders sy manages (identity.md 3), without CLAUDE_CONFIG_DIR.
func (e Env) Slots() []identity.Slot {
	s, _ := e.locator().Discover(e.SlotExtra, func(string) string { return "" })
	return s
}

// SlotFor is the slot for a CLAUDE_CONFIG_DIR value as Claude Code sees it: unset means
// ~/.claude (the default view); set, even to ~/.claude, is the explicit view
// (identity.md 3).
func (e Env) SlotFor(value string, set bool) identity.Slot {
	loc := e.locator()
	if !set {
		return loc.SlotFor(filepath.Join(e.Home, ".claude"))
	}
	return loc.Current(func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return value
		}
		return ""
	})
}

// managed returns the managed slot with the same folder as s.
func (e Env) managed(s identity.Slot) (identity.Slot, bool) {
	for _, m := range e.Slots() {
		if m.Dir == s.Dir {
			return m, true
		}
		a, err1 := e.fs().EvalSymlinks(m.Dir)
		b, err2 := e.fs().EvalSymlinks(s.Dir)
		if err1 == nil && err2 == nil && a == b {
			return m, true
		}
	}
	return identity.Slot{}, false
}

// Caller is the session `sy peers send` runs in (peers.md 4.1).
type Caller struct {
	ClaudePID int
	Slot      identity.Slot
	Entry     RegistryEntry
}

// Whoami finds the calling session: the nearest ancestor of StartPID that is a Claude Code
// process with a registry file in a slot sy manages.
//
// Linux: the config folder is CLAUDE_CONFIG_DIR read from the claude process's environment
// (that one variable only; unset means ~/.claude), and `<config>/sessions/<pid>.json` must
// exist with a start time matching the live process. macOS cannot read another process's
// environment: the one managed slot whose registry holds `<pid>.json` with the matching
// start time is used, and more than one is a refusal. The calling process's own environment
// is never consulted. No candidate: not_a_session (exit 4).
func (e Env) Whoami() (Caller, *Refusal) {
	if e.Proc == nil {
		return Caller{}, Refuse(CodeUnsupported, "%v", ErrUnsupported)
	}
	cands := ClaudeCandidates(e.Proc, e.StartPID)
	if len(cands) == 0 {
		return Caller{}, Refuse(CodeNotASession, "sy peers send works only inside a Claude Code session")
	}
	reason := "no registry file for the Claude Code process above this command"
	for _, c := range cands {
		live, _ := e.Proc.StartTime(c.PID)
		dir, set, err := e.Proc.ConfigDir(c.PID)
		switch {
		case err == nil:
			slot, ok := e.managed(e.SlotFor(dir, set))
			if !ok {
				reason = "its config folder is not one Switchyard manages"
				continue
			}
			ent, err := ReadRegistryPID(e.fs(), slot, c.PID)
			if err != nil {
				continue
			}
			if !ProcStartMatches(ent.ProcStartString(), live) {
				reason = "its registry file belongs to an older process with the same pid"
				continue
			}
			return Caller{ClaudePID: c.PID, Slot: slot, Entry: ent}, nil
		case err == ErrNoEnv:
			var found []Caller
			for _, slot := range e.Slots() {
				ent, err := ReadRegistryPID(e.fs(), slot, c.PID)
				if err == nil && ProcStartMatches(ent.ProcStartString(), live) {
					found = append(found, Caller{ClaudePID: c.PID, Slot: slot, Entry: ent})
				}
			}
			if len(found) == 1 {
				return found[0], nil
			}
			if len(found) > 1 {
				return Caller{}, Refuse(CodeNotASession, "more than one config folder holds a registry file for pid %d", c.PID)
			}
		default:
			reason = "cannot read the Claude Code process's configuration folder"
		}
	}
	return Caller{}, Refuse(CodeNotASession, "not inside a session Switchyard can identify: %s", reason)
}

// OffPath is <state>/peers/off (peers.md 11).
func (e Env) OffPath() string { return filepath.Join(e.State, "peers", "off") }

// Off reports whether `sy peers off` is in force on this machine.
func (e Env) Off() bool {
	_, err := os.Stat(e.OffPath())
	return err == nil
}

// SetOff writes or removes the off file.
func (e Env) SetOff(off bool) error {
	if !off {
		err := os.Remove(e.OffPath())
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.OffPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(e.OffPath(), []byte(e.now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// ThreadsPath, RatesPath and AuditPath are the files of peers.md 6.2, 9.2 and 10.
func (e Env) ThreadsPath() string { return filepath.Join(e.State, "peers", "threads.json") }
func (e Env) RatesPath() string   { return filepath.Join(e.State, "peers", "limits.json") }
func (e Env) AuditPath() string   { return filepath.Join(e.State, "peers", "audit.jsonl") }

// loginReader reads a slot's login from the cache the hooks keep (identity.md 4): no claude
// process is run and no credential file is opened.
func (e Env) loginReader() func(identity.Slot) (identity.Login, error) {
	st := &identity.Store{StateDir: e.State, FS: e.fs()}
	return st.Login
}

// tildeDir writes a folder as the slot name form.
func (e Env) tildeDir(dir string) string {
	if dir == "" {
		return ""
	}
	return identity.Tilde(e.Home, dir)
}

func sanitizeHandlePart(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '@' || r == '"' || r == '<' || r == '>' || r == '\'' || r == '`':
			return -1
		case r <= ' ' || r == 0x7f:
			return '-'
		}
		return r
	}, strings.TrimSpace(s))
	return strings.Trim(s, "-.")
}
