package peers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mraj602/switchyard/internal/atomicfile"
)

// Thread rules of peers.md 6.
const (
	// ThreadIdle closes a thread after this long with no message (6.1).
	ThreadIdle = 24 * time.Hour
	// DefaultMaxTurns is the loop guard's limit when the matrix does not say (6.3).
	DefaultMaxTurns = 30
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
}

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
	s.Threads = append(s.Threads, Thread{
		ID: ThreadID(from, to, now), A: from, B: to, AAccount: fromAccount, BAccount: toAccount,
		OpenedAt: now, LastAt: now,
	})
	return &s.Threads[len(s.Threads)-1]
}

// Expire closes threads idle for ThreadIdle or with a session that ended (live says whether a
// session id is still running), and forgets threads closed for over 7 days.
func (s *ThreadSet) Expire(now time.Time, live func(session string) bool) {
	var keep []Thread
	for _, t := range s.Threads {
		if t.Open() && (now.Sub(t.LastAt) > ThreadIdle || (live != nil && (!live(t.A) || !live(t.B)))) {
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
// make Turns exceed maxTurns, and then marks the thread paused. It does not count the
// message: RecordMessage does, after delivery. All three parties run this same function.
func LoopGuard(t *Thread, human time.Time, maxTurns int, now time.Time) (refuse bool) {
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	if human.After(t.HumanAt) {
		t.HumanAt = human
		t.Turns = 0
		t.PausedAt = nil
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

// RecordMessage counts one delivered message.
func (t *Thread) RecordMessage(now time.Time) {
	t.Turns++
	t.LastAt = now
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
