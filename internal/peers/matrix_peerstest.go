//go:build peerstest

package peers

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// TestAllowEnv names the file an unsigned local allow list is read from in `peerstest`
// builds. It exists only for tests; scripts/ci/check-release-seams.sh proves a release
// binary contains no trace of it.
const TestAllowEnv = "SY_PEERS_TEST_ALLOW"

type allowFile struct {
	Paused      bool       `json:"paused"`
	SameAccount bool       `json:"same_account"`
	MaxTurnsN   int        `json:"max_turns"`
	Allow       [][]string `json:"allow"`
}

// DefaultMatrix reads an unsigned allow file: $SY_PEERS_TEST_ALLOW, else
// <state>/peers/test-allow.json; none means NoMatrix. It evaluates peers.md 5.3 and does
// nothing else (no signatures).
func DefaultMatrix(state string) Matrix {
	p := os.Getenv(TestAllowEnv)
	if p == "" {
		p = filepath.Join(state, "peers", "test-allow.json")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return NoMatrix{}
	}
	var f allowFile
	if json.Unmarshal(b, &f) != nil {
		return NoMatrix{}
	}
	return f
}

func (f allowFile) MaxTurns() int {
	if f.MaxTurnsN > 0 {
		return f.MaxTurnsN
	}
	return DefaultMaxTurns
}

func (f allowFile) has(from, to string) bool {
	for _, p := range f.Allow {
		if len(p) == 2 && p[0] == from && p[1] == to {
			return true
		}
	}
	return false
}

func (f allowFile) Allowed(from, to Party, th *Thread) Decision {
	no := Decision{Reason: CodeNotAllowed, Detail: "the matrix does not allow " + from.Label + " → " + to.Label}
	switch {
	case f.Paused:
		return Decision{Reason: CodePaused, Detail: "peers are paused"}
	case from.Account != "" && from.Account == to.Account:
		if f.SameAccount {
			return Decision{Allowed: true}
		}
		return no
	case f.has(from.Account, to.Account):
		return Decision{Allowed: true}
	case th != nil && th.A == to.Session && f.has(to.Account, from.Account):
		return Decision{Allowed: true} // reply rule
	}
	return no
}
