package peers

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Session is one live session in the directory (peers.md 3.1): of this machine (the local
// source), or of another machine from the hub's directory (Remote, through Env.Remote).
type Session struct {
	Entry        RegistryEntry
	Account      string
	AccountLabel string
	Handle       string
	Name         string
	Project      string
	Mode         Mode // from the session file; prompting when there is none
	File         SessionFile
	HasFile      bool
	MachineName  string
	// Remote: the session runs on another machine; Device is that machine's agent device
	// ("" when it runs none, so it cannot be reached) and State the hub's session state.
	Remote bool
	Device string
	State  string
	// Blocked is why messages to this remote session cannot go now although the matrix may
	// allow them (Reachability: certificates; "" when they can).
	Blocked string
}

// ListEntry is one line of `sy peers list --json` (peers.md 3.2).
type ListEntry struct {
	Session      string  `json:"session"`
	Handle       string  `json:"handle"`
	Name         string  `json:"name"`
	Account      string  `json:"account"`
	AccountLabel string  `json:"account_label"`
	Machine      string  `json:"machine"`
	Project      string  `json:"project"`
	State        string  `json:"state"`
	Local        bool    `json:"local"`
	Can          string  `json:"can"` // start | reply | none
	Device       *string `json:"device"`
	// MayAsk marks a pair whose delivery Claude Code will likely hold for approval because
	// the two sessions' permission classes differ (peers.md 8.5).
	MayAsk bool `json:"may_ask_approval,omitempty"`
	// Blocked is why a session on another machine cannot be reached now although `can`
	// says the matrix allows it (link keys not owner-certified, a revoked machine; peers.md
	// 3.2). A send to it is refused with the same reason and the fix.
	Blocked string `json:"blocked,omitempty"`
}

// AccountLabel is the label shown for an account on this machine: the slots.labels entry for
// the slot, else the folder's name without its dot ("claude-2"). (The hub's seat label wins
// once the agent has the hub source.)
func (e Env) accountLabel(slotName string) string {
	if l := HandlePart(e.SlotLabels[slotName]); l != "" {
		return l
	}
	return HandlePart(strings.TrimPrefix(filepath.Base(slotName), "."))
}

// Directory lists the live local sessions: every managed slot's registry entries whose pid
// is alive with the same start time and whose socket accepts a connection (peers.md 3.1).
// Handles are `<name>@<account label>.<machine name>`, made unique by appending -2, -3 to
// the name part (so the `@label.machine` suffix stays addressable).
func (e Env) Directory() []Session {
	var out []Session
	login := e.loginReader()
	for _, slot := range e.Slots() {
		label := e.accountLabel(slot.Name)
		acct := ""
		if l, err := login(slot); err == nil && l.HasSeat {
			acct = l.Seat.Account
		}
		for _, ent := range ReadRegistry(e.fs(), slot) {
			if e.Proc != nil {
				live, err := e.Proc.StartTime(ent.PID)
				if err != nil || !ProcStartMatches(ent.ProcStartString(), live) {
					continue
				}
			}
			if !e.alive(ent.SocketOf(e.Runtime)) {
				continue
			}
			s := Session{Entry: ent, Account: acct, AccountLabel: label, Project: e.tildeDir(ent.CWD), MachineName: e.Machine.Name}
			s.Name = HandlePart(ent.Name)
			if s.Name == "" {
				s.Name = HandlePart(filepath.Base(ent.CWD))
			}
			if s.Name == "" || s.Name == "." || s.Name == "/" {
				s.Name = "session-" + strconv.Itoa(ent.PID)
			}
			s.File, s.HasFile = ReadSessionFile(e.State, ent.SessionID)
			s.Mode = ModeClass(s.File, s.HasFile)
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Entry, out[j].Entry
		if a.Slot.Dir != b.Slot.Dir {
			return a.Slot.Dir < b.Slot.Dir
		}
		return a.PID < b.PID
	})
	// Unique handles.
	count := map[string]int{}
	for i := range out {
		base := out[i].Name + "@" + out[i].AccountLabel + "." + HandlePart(e.Machine.Name)
		count[base]++
		if n := count[base]; n > 1 {
			out[i].Name += "-" + strconv.Itoa(n)
			base = out[i].Name + "@" + out[i].AccountLabel + "." + HandlePart(e.Machine.Name)
		}
		out[i].Handle = base
	}
	return out
}

// Entries builds the list entries as seen from `from` (nil at a terminal, with all true).
// Without all, only sessions `from` may start a thread with, or reply to in an open thread,
// are returned.
func (e Env) Entries(dir []Session, from *Session, all bool) []ListEntry {
	m := e.matrix()
	var threads *ThreadSet
	if from != nil {
		threads = LoadThreads(e.ThreadsPath())
	}
	var out []ListEntry
	for _, s := range dir {
		if from != nil && s.Entry.SessionID == from.Entry.SessionID {
			continue
		}
		le := ListEntry{
			Session: s.Entry.SessionID, Handle: s.Handle, Name: s.Name, Account: s.Account,
			AccountLabel: s.AccountLabel, Machine: s.MachineName, Project: s.Project,
			State: s.Entry.Status, Local: !s.Remote && from != nil && from.Account != "" && from.Account == s.Account,
			Can: "none",
		}
		if s.Remote {
			le.State, le.Blocked = s.State, s.Blocked
			if s.Device != "" {
				d := s.Device
				le.Device = &d
			}
		}
		if from != nil && (!s.Remote || s.Device != "") {
			le.Can = e.can(m, threads, *from, s)
			le.MayAsk = !s.Remote && from.Mode != s.Mode
		}
		if from != nil && !all && le.Can == "none" {
			continue
		}
		out = append(out, le)
	}
	return out
}

// MatrixConfigured reports whether a matrix is in force, as opposed to NoMatrix, which denies
// everything (the list command explains an empty result with it).
func (e Env) MatrixConfigured() bool {
	return e.matrix().Allowed(Party{}, Party{}, nil).Reason != CodeNoMatrix
}

func (e Env) matrix() Matrix {
	if e.Matrix != nil {
		return e.Matrix
	}
	return DefaultMatrix(e.State)
}

// can is "start" when the matrix lets from begin a thread with to, "reply" when an open
// thread that to opened lets from answer it, else "none".
func (e Env) can(m Matrix, ts *ThreadSet, from, to Session) string {
	fp, tp := party(from), party(to)
	th := ts.FindOpen(from.Entry.SessionID, to.Entry.SessionID)
	if th == nil || th.A == from.Entry.SessionID {
		if m.Allowed(fp, tp, th).Allowed {
			return "start"
		}
		return "none"
	}
	if m.Allowed(fp, tp, th).Allowed {
		return "reply"
	}
	return "none"
}

func party(s Session) Party {
	return Party{Session: s.Entry.SessionID, Account: s.Account, Label: s.AccountLabel}
}

// Resolve finds the session a `sy peers send <to>` names (peers.md 3.3): a handle, a unique
// prefix of one, a session id or a unique prefix of at least 8 characters, or `@<name>`.
// Several matches are an ambiguity: the candidates' handles are returned.
func Resolve(dir []Session, to string) (Session, []string, *Refusal) {
	to = strings.TrimSpace(to)
	if to == "" {
		return Session{}, nil, Refuse(CodeUnknownTarget, "no target given")
	}
	pick := func(match func(Session) bool) []Session {
		var m []Session
		for _, s := range dir {
			if match(s) {
				m = append(m, s)
			}
		}
		return m
	}
	lower := strings.ToLower(to)
	steps := []func() []Session{
		func() []Session { return pick(func(s Session) bool { return strings.ToLower(s.Handle) == lower }) },
		func() []Session { return pick(func(s Session) bool { return s.Entry.SessionID == to }) },
		func() []Session {
			if strings.HasPrefix(to, "@") && len(to) > 1 {
				n := strings.ToLower(to[1:])
				return pick(func(s Session) bool { return strings.ToLower(s.Name) == n })
			}
			return nil
		},
		func() []Session {
			return pick(func(s Session) bool { return strings.HasPrefix(strings.ToLower(s.Handle), lower) })
		},
		func() []Session {
			if len(to) < 8 {
				return nil
			}
			return pick(func(s Session) bool { return strings.HasPrefix(s.Entry.SessionID, to) })
		},
	}
	for _, step := range steps {
		switch m := step(); len(m) {
		case 0:
		case 1:
			return m[0], nil, nil
		default:
			var hs []string
			for _, s := range m {
				hs = append(hs, s.Handle)
			}
			return Session{}, hs, Refuse(CodeAmbiguous, "%q matches %s", to, strings.Join(hs, ", "))
		}
	}
	return Session{}, nil, Refuse(CodeUnknownTarget, "no live session matches %q (see sy peers list)", to)
}
