package state

import (
	"sort"
	"time"
)

// State is a session state (states.md section 1).
type State string

const (
	Working         State = "working"
	NeedsPermission State = "needs_permission"
	Asked           State = "asked"
	Done            State = "done"
	Failed          State = "failed"
	Ended           State = "ended"
	Gone            State = "gone"
)

// MinMissAge is how long after the session's last event a complete list must be before
// a miss counts toward gone; GoneMisses is how many misses make it gone.
const (
	MinMissAge = 60 * time.Second
	GoneMisses = 2
)

// Transition is one entry of a session's history: its state or detail changed.
type Transition struct {
	At     time.Time
	From   State // "" for the first one
	To     State
	Detail string
	Cause  string // event name, "session_list", or "gone"
}

// Session is the folded state of one session. The zero value is a session with no records.
type Session struct {
	State       State
	Detail      string    // sub-reason: failed:model_not_found -> "model_not_found"
	Since       time.Time // when State last changed (Detail changes do not move it)
	LastMessage string    // tail of the last Stop / StopFailure message, "" if none
	Question    string    // set while asked via the detector or AskUserQuestion
	Detector    string    // DetectorVersion
	History     []Transition

	LastEventAt  time.Time // newest hook event
	LastListedAt time.Time // newest session_list that listed it
	MissedLists  int       // complete lists since LastListedAt/LastEventAt that did not list it
	EndedAt      time.Time // set while ended or gone
	EndReason    string    // SessionEnd reason, or "gone"
}

// Fold folds inputs into a Session. Inputs are applied in (TS, ID) order, so callers may
// pass them in any order.
func Fold(inputs []Input) Session {
	in := append([]Input(nil), inputs...)
	sort.SliceStable(in, func(i, j int) bool {
		if !in[i].TS.Equal(in[j].TS) {
			return in[i].TS.Before(in[j].TS)
		}
		return in[i].ID < in[j].ID
	})
	var s Session
	for _, x := range in {
		s.Apply(x)
	}
	return s
}

// NeedsYou reports whether the session wants the user (states.md section 1).
func (s *Session) NeedsYou() bool {
	switch s.State {
	case NeedsPermission, Asked:
		return true
	case Failed:
		return s.Detail != "overloaded" && s.Detail != "server_error"
	}
	return false
}

// Apply applies one input to the session. Inputs must arrive in (TS, ID) order; use Fold
// for unordered input.
func (s *Session) Apply(in Input) {
	switch {
	case in.Hook != nil:
		s.applyHook(in.TS, in.Hook)
	case in.List != nil:
		s.applyList(in.TS, in.List)
	}
}

func (s *Session) closed() bool { return s.State == Ended || s.State == Gone }

// set moves to (to, detail) and records history on change.
func (s *Session) set(at time.Time, to State, detail, cause string) {
	if s.State == to && s.Detail == detail {
		return
	}
	if s.State != to {
		s.Since = at
	}
	s.History = append(s.History, Transition{At: at, From: s.State, To: to, Detail: detail, Cause: cause})
	s.State, s.Detail = to, detail
	if to != Asked {
		s.Question = ""
	}
	if s.closed() {
		s.EndedAt, s.EndReason = at, detail
	} else {
		s.EndedAt, s.EndReason = time.Time{}, ""
	}
	s.Detector = DetectorVersion
}

// rule is the result of a transition-table cell: change to (to, detail), or (to == "") no
// change. reopen marks a "·" cell where an ended/gone session comes back as done even
// though the cell itself changes nothing.
type rule struct {
	to       State
	detail   string
	question string
	reopen   bool
}

func (s *Session) applyHook(ts time.Time, h *Hook) {
	s.LastEventAt = ts
	s.MissedLists = 0
	cur := s.State
	if cur == "" || s.closed() {
		cur = Done
	}
	if h.Name == "SessionEnd" && s.closed() {
		return
	}
	r := hookRule(cur, h)
	if h.Name == "StopFailure" || (h.Name == "Stop" && !h.StopHookActive) {
		s.LastMessage = ""
		if h.HasLastMessage {
			s.LastMessage = h.LastMessage
		}
	}
	switch {
	case r.to != "":
		s.set(ts, r.to, r.detail, h.Name)
		if r.to == Asked {
			s.Question = r.question
		}
	case r.reopen && s.closed():
		s.set(ts, Done, h.Source, h.Name)
	}
	if s.State == "" {
		s.set(ts, Done, "", h.Name) // first record changed nothing: a fresh session is done
	}
}

func isAskTool(n string) bool { return n == "AskUserQuestion" || n == "ExitPlanMode" }

// hookRule is the transition table of states.md section 3 for the effective current state
// cur (ended, gone and "no state yet" are passed as Done).
func hookRule(cur State, h *Hook) rule {
	switch h.Name {
	case "SessionStart":
		switch h.Source {
		case "compact":
			return rule{reopen: true}
		case "resume":
			return rule{to: Done, detail: "resumed"}
		}
		return rule{to: Done, detail: "started"}
	case "UserPromptSubmit":
		return rule{to: Working}
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
		if h.Name == "PreToolUse" && isAskTool(h.ToolName) {
			if cur == Asked {
				return rule{}
			}
			return rule{to: Asked, detail: "tool", question: h.Question}
		}
		if cur == Working {
			return rule{}
		}
		return rule{to: Working}
	case "PermissionRequest":
		return needsPermission(cur, "permission_request")
	case "Notification":
		switch h.NotificationType {
		case "permission_prompt":
			return needsPermission(cur, "permission_prompt")
		case "elicitation_dialog":
			return elicitation(cur)
		case "idle_prompt":
			if cur == Working {
				return rule{to: Done, detail: "repaired"}
			}
		}
		return rule{}
	case "Elicitation":
		return elicitation(cur)
	case "Stop":
		if h.StopHookActive {
			return rule{}
		}
		if h.HasLastMessage {
			if q, text := ReadsAsQuestion(h.LastMessage); q {
				return rule{to: Asked, detail: "question", question: text}
			}
		}
		return rule{to: Done}
	case "StopFailure":
		e := h.Error
		if e == "" {
			e = "unknown"
		}
		return rule{to: Failed, detail: e}
	case "PermissionDenied":
		if cur == NeedsPermission {
			return rule{to: Working}
		}
		return rule{}
	case "SessionEnd":
		r := h.Reason
		if r == "" {
			r = "other"
		}
		return rule{to: Ended, detail: r}
	}
	return rule{}
}

func needsPermission(cur State, detail string) rule {
	if cur == NeedsPermission {
		return rule{}
	}
	return rule{to: NeedsPermission, detail: detail}
}

func elicitation(cur State) rule {
	if cur == Asked {
		return rule{}
	}
	return rule{to: Asked, detail: "elicitation"}
}

func (s *Session) applyList(ts time.Time, l *ListObs) {
	if l.Listed {
		s.LastListedAt = ts
		s.MissedLists = 0
		// A list that arrives after a newer hook event describes the session as it was
		// before that event (ts is when `claude agents` ran): repairing from it would undo
		// the event. Hook events win; only a list taken after the last event may repair.
		if !s.LastEventAt.IsZero() && s.LastEventAt.After(ts) {
			return
		}
		cur := s.State
		if cur == "" || s.closed() {
			cur = Done
		}
		switch {
		case l.Status == "busy" && cur == Done:
			s.set(ts, Working, "repaired", "session_list")
		case l.Status == "idle" && cur == Working:
			s.set(ts, Done, "repaired", "session_list")
		}
		if s.closed() || s.State == "" { // busy/idle handled above; waiting or unknown: back as done
			s.set(ts, Done, "repaired", "session_list")
		}
		return
	}
	if !l.Complete || s.State == "" || s.closed() {
		return
	}
	if !s.LastEventAt.IsZero() && ts.Sub(s.LastEventAt) < MinMissAge {
		return
	}
	s.MissedLists++
	if s.MissedLists >= GoneMisses {
		s.set(ts, Gone, "gone", "gone")
	}
}
