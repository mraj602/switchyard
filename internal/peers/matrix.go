package peers

// Party is one end of a message as the matrix sees it (peers.md 5.3): a session and the
// account (uuid) it belongs to.
type Party struct {
	Session string
	Account string
	Label   string // the account's display label, for messages only
}

// Decision is the matrix's answer. Reason is "" when allowed, else one of CodePaused,
// CodeNotAllowed, CodeNoMatrix.
type Decision struct {
	Allowed bool
	Reason  string
	Detail  string // human text for the refusal line, e.g. "work → personal"
}

// Matrix evaluates the account matrix (peers.md 5): DocMatrix for an accepted signed
// document, NoMatrix for none. thread is the open thread between the two sessions, or nil;
// 5.3 step 4 (replies) needs it. A "no" is final.
type Matrix interface {
	// Allowed is peers.Allowed(m, from_account, to_account, thread) of peers.md 5.3.
	Allowed(from, to Party, thread *Thread) Decision
	// MaxTurns is the loop guard limit (matrix `max_turns`, 2-500; 0 is no limit).
	MaxTurns() int
}

// pauser is implemented by a matrix that can say "paused" without a pair: the sender checks
// it first (peers.md 4.2 step 1), before the target, content or any rate token.
type pauser interface{ Paused() bool }

// NoMatrix is the matrix of a machine that has none configured: it denies every message.
type NoMatrix struct{}

func (NoMatrix) Allowed(from, to Party, _ *Thread) Decision {
	return Decision{Reason: CodeNoMatrix, Detail: "the matrix is not configured on this machine, so nothing is allowed"}
}

func (NoMatrix) MaxTurns() int { return NoTurnLimit }
