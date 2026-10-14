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

// Matrix is the hook point for the signed matrix document (peers.md 5): the keys/matrix
// package satisfies it once it has accepted a document. thread is the open thread between
// the two sessions, or nil; 5.3 step 4 (replies) needs it. Implementations must not retry or
// widen anything: a "no" is final.
type Matrix interface {
	// Allowed is peers.Allowed(m, from_account, to_account, thread) of peers.md 5.3.
	Allowed(from, to Party, thread *Thread) Decision
	// MaxTurns is the loop guard limit (matrix `max_turns`, 2-500).
	MaxTurns() int
}

// NoMatrix is the matrix of a machine that has none configured: it denies every message.
// It is what `sy` uses until the keys and matrix package is wired in.
type NoMatrix struct{}

func (NoMatrix) Allowed(from, to Party, _ *Thread) Decision {
	return Decision{Reason: CodeNoMatrix, Detail: "the matrix is not configured on this machine, so nothing is allowed"}
}

func (NoMatrix) MaxTurns() int { return DefaultMaxTurns }
