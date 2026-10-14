package peers

import (
	"path/filepath"
	"sync"
	"time"
)

// Remote is the agent's path to other machines (PE2, peers.md 3.1 hub source, 8.2): the hub's
// session directory and the envelope relay. internal/peerlink implements it over the link.
// Env.Remote nil means this machine only (the direct path of 8.5, or an agent with no hub).
type Remote interface {
	// Sessions are the live sessions of other machines (Remote true). An error means the hub
	// could not be asked now.
	Sessions() ([]Session, error)
	// Send seals, signs and hands one checked message to the hub, and returns once the hub
	// answered (peer_ack): queued, or a refusal. Pending.Wait then waits for the final status.
	Send(o Outgoing) (*Pending, *Refusal)
	// Log reports a same-machine delivery for the hub's audit (peer_log), spooling it while
	// the hub is unreachable (peers.md 9.3).
	Log(l LocalLog)
}

// Outgoing is a message to a session on another machine that passed the sender's checks.
type Outgoing struct {
	Msg, Thread string
	Kind        Kind
	From, To    Session
	// HumanAt is the newest human prompt in the sending session (zero: none known).
	HumanAt time.Time
	Text    string
	TTL     time.Duration
	Now     time.Time
}

// Pending is a message the hub queued.
type Pending struct {
	Expires time.Time
	// Offline: the target machine's link is not connected (peers.md 4.3's "queued" line).
	Offline bool
	// Wait blocks up to d for the final status (peer_status); ok is false when none came.
	Wait func(d time.Duration) (f Final, ok bool)
}

// Final is a message's final status: delivered, rejected, expired, target_gone or failed,
// with the reason word.
type Final struct {
	Status, Detail string
}

// LocalLog is a same-machine delivery as the hub's audit records it (link.md 6.2 peer_log).
type LocalLog struct {
	Msg, Thread            string
	FromSession, ToSession string
	Kind                   Kind
	Status, Detail         string
	Size                   int
	At                     time.Time
}

// SendOpts are the optional parts of a send.
type SendOpts struct {
	// TTL of a message to another machine (default 1 h, 1 min to 24 h).
	TTL time.Duration
}

// stateMu and <state>/peers/state.lock serialise the read-change-write of the state files
// (threads.json, limits.json): the mutex between the agent's sends and its deliveries of
// remote messages, the file lock with `sy peers send` processes that write the inbox
// themselves (no agent running), which would otherwise each start from the same budget.
// Neither is held while waiting on the hub.
var stateMu sync.Mutex

func lockState(state string) func() {
	stateMu.Lock()
	unlockFile := lockFile(filepath.Join(state, "peers", "state.lock"))
	return func() {
		unlockFile()
		stateMu.Unlock()
	}
}
