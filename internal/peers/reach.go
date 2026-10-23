package peers

import (
	"errors"
	"strings"

	"github.com/mraj602/switchyard/internal/keys"
)

// CertFix is what the owner does when messages between machines are refused because link
// keys are not owner-certified (keys.md 3.3, 4.2): it is said in `sy peers list`, in the
// refusal line of `sy peers send` and by `sy doctor`, so the person reading any of them knows
// the next step. The commands need a terminal (the signing guard refuses them inside Claude
// Code).
const CertFix = "the owner certifies the machines in a terminal on the owner machine: sy keys init (once, when " +
	"sy keys status shows no owner key), confirm it (board Settings → Keys, or sy hub keys confirm-owner on the hub " +
	"machine), then sy keys certify --all; every other machine then runs sy agent enable to pin the owner key"

// AccountsFix is what the owner does when a machine's accounts are not bound to it
// (unbound_account, keys.md 4.3): the same terminal-only command that certifies machines.
const AccountsFix = "the owner binds each machine's accounts in a terminal on the owner machine: sy keys certify --all " +
	"(it lists the accounts the hub sees on each machine; compare them with sy keys status on that machine)"

// UnboundLine is the refusal detail of a message whose sender account the owner has not bound
// to its machine, at the hub or at the receiver.
const UnboundLine = "refused (unbound_account): the owner has not bound this session's account to its machine. To fix it, " +
	AccountsFix + ". Do not retry until then."

// Reasons a session on another machine cannot be reached although the matrix may allow it
// (ListEntry.Blocked). Uncertified ones are fixed by CertFix.
const (
	BlockedNoOwnerPin    = "this machine has no owner key pinned"
	BlockedSelfUncert    = "this machine's link keys are not owner-certified"
	blockedTargetUncert  = "'s link keys are not owner-certified"
	blockedTargetRevoked = " is revoked"
	blockedNoCert        = "the hub sent no certificate for "
)

// ErrBlocked carries a Blocked reason; Uncertified says CertFix applies.
type ErrBlocked struct {
	Reason      string
	Uncertified bool
}

func (e *ErrBlocked) Error() string { return e.Reason }

// Reachability is whether this machine can exchange messages with the machine of a remote
// session, from the certificates in the hub's directory reply (link.md 6.3): this machine
// must pin an owner key, its own certificate must carry the owner's signature (the receivers
// refuse it otherwise, 8.4), and the target's certificate must check (8.2). own is this
// machine's certificate as the hub has it (nil when the hub did not send it: not checked).
// It returns nil when messages can go.
func Reachability(trust keys.Trust, rev *keys.RevocationBody, own *keys.Doc, target *keys.Doc, device, machine string) *ErrBlocked {
	if trust.Owner == nil {
		return &ErrBlocked{Reason: BlockedNoOwnerPin, Uncertified: true}
	}
	if own != nil {
		if _, err := keys.VerifyCert(*own, trust, true); errors.Is(err, keys.ErrUncertified) {
			return &ErrBlocked{Reason: BlockedSelfUncert, Uncertified: true}
		}
	}
	if target == nil {
		return &ErrBlocked{Reason: blockedNoCert + machine}
	}
	if _, err := CheckMachineCert(*target, trust, rev, device); err != nil {
		switch ReasonOf(err) {
		case ReasonUncertified:
			return &ErrBlocked{Reason: machine + blockedTargetUncert, Uncertified: true}
		case ReasonRevoked:
			return &ErrBlocked{Reason: machine + blockedTargetRevoked}
		}
		return &ErrBlocked{Reason: machine + "'s certificate does not check (" + ReasonOf(err) + ")"}
	}
	return nil
}

// FixedByCerts reports whether a Blocked reason is one CertFix removes.
func FixedByCerts(reason string) bool {
	return reason == BlockedNoOwnerPin || reason == BlockedSelfUncert || strings.HasSuffix(reason, blockedTargetUncert)
}

// BlockedLine is the refusal detail of a send to a blocked session.
func BlockedLine(handle string, b *ErrBlocked) string {
	s := "cannot reach " + handle + ": " + b.Reason
	if b.Uncertified {
		s += ". To fix it, " + CertFix
	}
	return s + ". Do not retry until then."
}
