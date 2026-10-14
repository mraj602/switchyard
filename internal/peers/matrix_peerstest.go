//go:build peerstest

package peers

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mraj602/switchyard/internal/keys"
)

// TestAllowEnv names the file an unsigned local allow list is read from in `peerstest`
// builds. It exists only for tests; scripts/ci/check-release-seams.sh proves a release
// binary contains no trace of it.
const TestAllowEnv = "SY_PEERS_TEST_ALLOW"

// DefaultMatrix reads an unsigned allow file ({"paused","same_account","max_turns","allow"}):
// $SY_PEERS_TEST_ALLOW, else <state>/peers/test-allow.json, evaluated like a signed document
// (no signatures). With no file it is the signed document, as in a release build.
func DefaultMatrix(state string) Matrix {
	p := os.Getenv(TestAllowEnv)
	if p == "" {
		p = filepath.Join(state, "peers", "test-allow.json")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return LocalMatrix(state)
	}
	var f keys.MatrixBody
	if json.Unmarshal(b, &f) != nil {
		return NoMatrix{}
	}
	allow := f.Allow[:0]
	for _, pair := range f.Allow {
		if len(pair) == 2 {
			allow = append(allow, pair)
		}
	}
	f.Allow = allow
	return NewDocMatrix(f)
}
