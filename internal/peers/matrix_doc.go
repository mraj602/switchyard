package peers

import (
	"github.com/mraj602/switchyard/internal/keys"
)

// DocMatrix evaluates an accepted matrix document body (peers.md 5.3). It is the Matrix of a
// machine that holds a signed matrix; NoMatrix is the one of a machine that holds none.
type DocMatrix struct{ B keys.MatrixBody }

// NewDocMatrix wraps a verified body.
func NewDocMatrix(b keys.MatrixBody) DocMatrix { return DocMatrix{B: b} }

// MaxTurns is the loop guard limit of the document: 2-500, or 0 (NoTurnLimit) for none.
func (m DocMatrix) MaxTurns() int {
	if m.B.MaxTurns < 0 {
		return NoTurnLimit
	}
	return m.B.MaxTurns
}

func (m DocMatrix) label(p Party) string {
	switch {
	case p.Label != "":
		return p.Label
	case m.B.Accounts[p.Account] != "":
		return m.B.Accounts[p.Account]
	case p.Account != "":
		return p.Account
	}
	return "unknown"
}

// Paused reports the document's `paused` flag.
func (m DocMatrix) Paused() bool { return m.B.Paused }

// Allowed is peers.Allowed of 5.3, in order: paused; the diagonal (same_account); an allowed
// pair; a reply (an open thread the target opened, while [to, from] is still allowed); else
// no. An unknown account (empty uuid) matches no pair and is never "the same account".
func (m DocMatrix) Allowed(from, to Party, th *Thread) Decision {
	if m.B.Paused {
		return Decision{Reason: CodePaused, Detail: "peers are paused"}
	}
	no := Decision{Reason: CodeNotAllowed, Detail: "the matrix does not allow " + m.label(from) + " → " + m.label(to)}
	switch {
	case from.Account != "" && from.Account == to.Account:
		if m.B.SameAccount {
			return Decision{Allowed: true}
		}
		return no
	case from.Account == "" || to.Account == "":
		return no
	case m.B.Allows(from.Account, to.Account):
		return Decision{Allowed: true}
	case th != nil && th.Open() && th.A == to.Session && m.B.Allows(to.Account, from.Account):
		return Decision{Allowed: true}
	}
	return no
}

// LocalMatrix is the matrix this machine's agent and `sy peers send` use: the newest accepted
// document in <state>/keys/matrix.json, else NoMatrix. A file that no longer verifies is
// treated as none (everything denied).
func LocalMatrix(state string) Matrix {
	s, err := keys.LoadAcceptedMatrix(state)
	if err != nil || s == nil {
		return NoMatrix{}
	}
	return NewDocMatrix(s.Body)
}
