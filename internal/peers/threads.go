package peers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/mraj602/switchyard/internal/atomicfile"
)

// Thread rules of peers.md 6.
const (
	// ThreadIdle closes a thread after this long with no message (6.1).
	ThreadIdle = 24 * time.Hour
	// NoTurnLimit is the max_turns that switches the loop guard's turn count off (6.3, 5.1);
	// it is also the default of a new matrix.
	NoTurnLimit = 0
)

// Kind of a message in a thread (6.1).
type Kind string

const (
	KindStart Kind = "start"
	KindReply Kind = "reply"
)

// Thread is the conversation between two sessions (peers.md 6.1). ASession opened it.
type Thread struct {
	ID       string     `json:"id"`
	A        string     `json:"a_session"`
	B        string     `json:"b_session"`
	AAccount string     `json:"a_account,omitempty"`
	BAccount string     `json:"b_account,omitempty"`
	OpenedAt time.Time  `json:"opened_at"`
	LastAt   time.Time  `json:"last_at"`
	HumanAt  time.Time  `json:"human_at,omitzero"` // newest human prompt seen, either session
	Turns    int        `json:"turns"`             // messages since HumanAt
	PausedAt *time.Time `json:"paused_at,omitempty"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
	// Remote is the session of the two that runs on another machine ("" for a same-machine
	// thread): this machine cannot see it end, so only idleness closes such a thread.
	Remote string `json:"remote,omitempty"`
	// LastFrom is the session that sent the newest message ("" in a file from before it was
	// kept): the other session owes the next one (AwaitingReply).
	LastFrom string `json:"last_from,omitempty"`
}

var threadIDRE = regexp.MustCompile(`^t_[0-9a-f]{16}$`)

// ValidThreadID reports whether id has a thread id's form (6.1, api.yaml): "t_" + 16 lowercase hex.
func ValidThreadID(id string) bool { return threadIDRE.MatchString(id) }

// ThreadID is "t_" + the first 16 hex of SHA-256(a ‖ "\n" ‖ b ‖ "\n" ‖ decimal(opened_at ms)).
func ThreadID(a, b string, openedAt time.Time) string {
	sum := sha256.Sum256([]byte(a + "\n" + b + "\n" + strconv.FormatInt(openedAt.UnixMilli(), 10)))
	return "t_" + hex.EncodeToString(sum[:])[:16]
}

// Open reports whether the thread has not been closed.
func (t Thread) Open() bool { return t.ClosedAt == nil }

// Between reports whether the thread joins these two sessions (in either order).
func (t Thread) Between(x, y string) bool {
	return (t.A == x && t.B == y) || (t.A == y && t.B == x)
}

// KindFor is the kind of a message sent by `from` in this thread: from b_session it is a
// reply, from a_session another start.
func (t Thread) KindFor(from string) Kind {
	if from == t.B {
		return KindReply
	}
	return KindStart
}

// ThreadSet is the content of <state>/peers/threads.json (peers.md 6.2).
type ThreadSet struct {
	V       int      `json:"v"`
	Threads []Thread `json:"threads"`
}

// FindOpen returns the open thread between two sessions, or nil. At most one exists.
func (s *ThreadSet) FindOpen(x, y string) *Thread {
	for i := range s.Threads {
		if s.Threads[i].Open() && s.Threads[i].Between(x, y) {
			return &s.Threads[i]
		}
	}
	return nil
}

// Start opens a thread: from is a_session.
func (s *ThreadSet) Start(from, to, fromAccount, toAccount string, now time.Time) *Thread {
	return s.Adopt(ThreadID(from, to, now), from, to, fromAccount, toAccount, "", now)
}

// Adopt opens a thread whose id another machine made (a remote message, peers.md 6.1: the
// sender's agent makes the id and every envelope carries it). a is the opener; remote is the
// session on the other machine.
func (s *ThreadSet) Adopt(id, a, b, aAccount, bAccount, remote string, now time.Time) *Thread {
	s.Threads = append(s.Threads, Thread{ID: id, A: a, B: b, AAccount: aAccount, BAccount: bAccount, OpenedAt: now, LastAt: now,
		Remote: remote})
	return &s.Threads[len(s.Threads)-1]
}

// Expire closes threads idle for ThreadIdle or with a session that ended (live says whether a
// session id is still running; the remote session of a thread counts as live), and forgets
// threads closed for over 7 days.
func (s *ThreadSet) Expire(now time.Time, live func(session string) bool) {
	var keep []Thread
	for _, t := range s.Threads {
		alive := func(id string) bool { return live == nil || id == t.Remote || live(id) }
		if t.Open() && (now.Sub(t.LastAt) > ThreadIdle || !alive(t.A) || !alive(t.B)) {
			c := now
			t.ClosedAt = &c
		}
		if t.ClosedAt != nil && now.Sub(*t.ClosedAt) > 7*24*time.Hour {
			continue
		}
		keep = append(keep, t)
	}
	s.Threads = keep
}

// LoopGuard applies peers.md 6.3 to a thread that is about to carry one more message. human is
// the newest human prompt in either session. A human prompt newer than the thread's HumanAt
// resets the turn count and lifts the pause. It returns true (refuse) when the message would
// make Turns exceed maxTurns, and then marks the thread paused. maxTurns 0 (NoTurnLimit) never
// refuses and lifts an earlier stop; the count is still kept, so a limit set later applies to
// it. It does not count the message: RecordMessage does, after delivery. All three parties run
// this same function.
func LoopGuard(t *Thread, human time.Time, maxTurns int, now time.Time) (refuse bool) {
	if human.After(t.HumanAt) {
		t.HumanAt = human
		t.Turns = 0
		t.PausedAt = nil
	}
	if maxTurns <= NoTurnLimit {
		t.PausedAt = nil
		return false
	}
	if t.Turns+1 > maxTurns {
		if t.PausedAt == nil {
			p := now
			t.PausedAt = &p
		}
		return true
	}
	return false
}

// Resume lifts a loop-guard stop the way a new human prompt does (6.3): the turn count goes
// back to 0 and the pause is cleared. HumanAt is left as it is (no prompt was typed).
func (t *Thread) Resume() {
	t.Turns = 0
	t.PausedAt = nil
}

// Find returns the thread with this id, open or closed, or nil.
func (s *ThreadSet) Find(id string) *Thread {
	for i := range s.Threads {
		if s.Threads[i].ID == id {
			return &s.Threads[i]
		}
	}
	return nil
}

// RecordMessage counts one delivered message.
func (t *Thread) RecordMessage(now time.Time) {
	t.Turns++
	t.LastAt = now
}

// RecordMessageFrom counts one delivered message sent by session from.
func (t *Thread) RecordMessageFrom(from string, now time.Time) {
	t.RecordMessage(now)
	t.LastFrom = from
}

// AwaitingReply reports whether session is waiting on another session's reply (peers.md
// 6.4): it sent the newest message of an open thread that the loop guard has not stopped,
// after its own newest human prompt (humanAt; zero when unknown) and within ThreadIdle of
// now. A thread stopped by the loop guard needs a person, so it is not waiting.
func (s *ThreadSet) AwaitingReply(session string, humanAt, now time.Time) bool {
	for _, t := range s.Threads {
		if t.Open() && t.PausedAt == nil && t.LastFrom == session && t.LastAt.After(humanAt) &&
			now.Sub(t.LastAt) <= ThreadIdle {
			return true
		}
	}
	return false
}

// LoadThreads reads <state>/peers/threads.json; a missing or broken file is empty.
func LoadThreads(path string) *ThreadSet {
	s := &ThreadSet{V: 1}
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, s) != nil {
		return &ThreadSet{V: 1}
	}
	return s
}

// Save writes the file atomically (0600).
func (s *ThreadSet) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}
