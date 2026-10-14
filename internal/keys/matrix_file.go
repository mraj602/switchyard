package keys

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// The newest accepted matrix document, <state>/keys/matrix.json (keys.md 10): one place that
// the CLI (after it fetched or signed a document), the agent's link client (on a `matrix`
// frame) and the local evaluator (peers.DefaultMatrix) all read and write.

// LoadAcceptedMatrix reads matrix.json and verifies it against this machine's pins. It
// returns (nil, nil) when there is no file or no hub pin yet, and an error for a file that
// does not verify (it is ignored by callers: nothing is allowed then).
func LoadAcceptedMatrix(stateDir string) (*Signed[MatrixBody], error) {
	b, err := os.ReadFile(Path(stateDir, MatrixFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := PinnedTrust(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s is damaged", Path(stateDir, MatrixFile))
	}
	s, err := VerifyMatrix(d, t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Path(stateDir, MatrixFile), err)
	}
	return &s, nil
}

// SaveAcceptedMatrix verifies a received matrix document with this machine's pins and
// stores it when AcceptMatrix takes it (a higher version than the stored one; a hub-signed
// widening only with hubMayWiden). accepted is false, with no error, for a version not above
// the stored one. A document that fails (bad signature, another hub, ErrHubWidening) is an
// error and changes nothing. The stored file is rewritten atomically, mode 0600.
func SaveAcceptedMatrix(stateDir string, d Doc, hubMayWiden bool) (s Signed[MatrixBody], accepted bool, err error) {
	t, err := PinnedTrust(stateDir)
	if err != nil {
		return s, false, fmt.Errorf("this machine has not pinned a hub key: %w", err)
	}
	s, err = VerifyMatrix(d, t)
	if err != nil {
		return s, false, err
	}
	var cur *MatrixBody
	if c, err := LoadAcceptedMatrix(stateDir); err == nil && c != nil {
		cur = &c.Body
	}
	ok, err := AcceptMatrix(cur, s, hubMayWiden)
	if err != nil || !ok {
		return s, false, err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return s, false, err
	}
	return s, true, writePrivate(Path(stateDir, MatrixFile), append(b, '\n'))
}
