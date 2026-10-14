package keys

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// Matrix limits (peers.md 5.1).
const (
	MinMaxTurns     = 2
	MaxMaxTurns     = 500
	DefaultMaxTurns = 30
)

// MatrixBody is the body of a matrix document (peers.md 5.1). Evaluating it (who may message
// whom) is the peers package's job; this package signs, verifies and compares bodies.
type MatrixBody struct {
	V           int               `json:"v"`
	Version     int               `json:"version"`
	Hub         string            `json:"hub"`
	IssuedAt    string            `json:"issued_at"`
	Paused      bool              `json:"paused"`
	SameAccount bool              `json:"same_account"`
	MaxTurns    int               `json:"max_turns"`
	Accounts    map[string]string `json:"accounts"`
	Allow       [][]string        `json:"allow"`
}

// BaselineMatrix is version 1 for hub hubFP: nothing allowed, same_account on, not paused,
// 30 turns. A machine with no document compares against it (keys.md 5.4).
func BaselineMatrix(hubFP string, now time.Time) MatrixBody {
	return MatrixBody{V: 1, Version: 1, Hub: hubFP, IssuedAt: FormatTime(now), SameAccount: true,
		MaxTurns: DefaultMaxTurns, Accounts: map[string]string{}, Allow: [][]string{}}
}

// Allows reports whether the directed pair [from, to] is in allow.
func (m MatrixBody) Allows(from, to string) bool {
	return slices.ContainsFunc(m.Allow, func(p []string) bool { return p[0] == from && p[1] == to })
}

// Check validates the fields: version from 1, max_turns 2-500, allow pairs of two non-empty
// account ids, sorted, no duplicates.
func (m MatrixBody) Check() error {
	bad := func(f string) error { return fmt.Errorf("%w: matrix %s", ErrBody, f) }
	if m.Version < 1 {
		return bad("version")
	}
	if !validTime(m.IssuedAt) {
		return bad("issued_at")
	}
	if m.MaxTurns < MinMaxTurns || m.MaxTurns > MaxMaxTurns {
		return bad("max_turns")
	}
	for i, p := range m.Allow {
		if len(p) != 2 || p[0] == "" || p[1] == "" {
			return bad("allow pair")
		}
		if i > 0 && comparePair(m.Allow[i-1], p) >= 0 {
			return bad("allow (must be sorted, no duplicates)")
		}
	}
	return nil
}

func comparePair(a, b []string) int {
	if c := cmp.Compare(a[0], b[0]); c != 0 {
		return c
	}
	return cmp.Compare(a[1], b[1])
}

// SortAllow sorts pairs and removes duplicates, the form Check wants.
func SortAllow(pairs [][]string) [][]string {
	out := slices.Clone(pairs)
	slices.SortFunc(out, comparePair)
	return slices.CompactFunc(out, func(a, b []string) bool { return comparePair(a, b) == 0 })
}

// Widens reports whether next widens compared with old (keys.md 5.2): an allowed pair is
// added, same_account goes from false to true, paused from true to false, or max_turns
// grows. Everything else narrows or is neutral. The hub, the agent and the CLI all use this.
func Widens(old, next MatrixBody) bool {
	for _, p := range next.Allow {
		if len(p) == 2 && !old.Allows(p[0], p[1]) {
			return true
		}
	}
	return (!old.SameAccount && next.SameAccount) || (old.Paused && !next.Paused) || next.MaxTurns > old.MaxTurns
}

// VerifyMatrix checks a matrix document: one signature, by the owner or the hub, the body's
// fields, and that it is for this hub.
func VerifyMatrix(d Doc, t Trust) (Signed[MatrixBody], error) {
	raw, err := Verify(d, TypeMatrix, t.ring())
	if err != nil {
		return Signed[MatrixBody]{}, err
	}
	var b MatrixBody
	if err := decodeBody(raw, &b); err != nil {
		return Signed[MatrixBody]{}, err
	}
	if err := b.Check(); err != nil {
		return Signed[MatrixBody]{}, err
	}
	if b.Hub != t.HubFP() {
		return Signed[MatrixBody]{}, fmt.Errorf("%w: matrix of hub %s", ErrWrongHub, GroupFP(b.Hub))
	}
	return Signed[MatrixBody]{Body: b, Raw: raw, Signer: d.SignedBy()[0], Doc: d}, nil
}

// AcceptMatrix is a machine's rule for a verified matrix document (keys.md 5.4, peers.md
// 5.1). cur is its newest accepted body (nil: none, compared as the baseline). A version not
// above cur's is ignored (accept false, no error). With hubMayWiden false a hub-signed
// document that widens what this machine has is refused (ErrHubWidening); owner-signed and
// narrowing documents are always taken.
func AcceptMatrix(cur *MatrixBody, next Signed[MatrixBody], hubMayWiden bool) (bool, error) {
	base := BaselineMatrix(next.Body.Hub, time.Time{})
	if cur != nil {
		if next.Body.Version <= cur.Version {
			return false, nil
		}
		base = *cur
	}
	if next.Signer == ByHub && !hubMayWiden && Widens(base, next.Body) {
		return false, ErrHubWidening
	}
	return true, nil
}

// HubMaySignMatrix is the hub's rule before it signs a matrix change itself (keys.md 5.1,
// 5.4): an owner session may make any change; any other authenticated device only one that
// does not widen. next must be the next version.
func HubMaySignMatrix(cur, next MatrixBody, ownerSession bool) error {
	if next.Version != cur.Version+1 {
		return fmt.Errorf("%w: matrix %d after %d", ErrVersion, next.Version, cur.Version)
	}
	if !ownerSession && Widens(cur, next) {
		return fmt.Errorf("%w: widening the matrix needs the owner key or an owner session", ErrHubWidening)
	}
	return nil
}

// HubTakesOwnerMatrix is the hub's rule for an owner-signed matrix uploaded by any
// authenticated caller (keys.md 5.3): the signature is the authority, and the version must be
// the current one + 1 (cur nil: none yet, so 1).
func HubTakesOwnerMatrix(cur *MatrixBody, next Signed[MatrixBody]) error {
	want := 1
	if cur != nil {
		want = cur.Version + 1
	}
	if next.Signer != ByOwner {
		return fmt.Errorf("%w: not owner-signed", ErrSigners)
	}
	if next.Body.Version != want {
		return fmt.Errorf("%w: matrix %d, want %d", ErrVersion, next.Body.Version, want)
	}
	return nil
}

// HubMayRevoke is who may have the hub sign a revocation (keys.md 5.1): an owner session any
// device, any other device only itself.
func HubMayRevoke(caller, target string, ownerSession bool) bool {
	return ownerSession || caller == target
}
