package keys

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// The newest accepted revocation list, <state>/keys/revocations.json (keys.md 6, 10): the
// agent's link client stores it on a `revoke` frame; anything that checks a peer's
// certificate reads it.

// LoadAcceptedRevocations reads revocations.json and verifies it against this machine's
// pins. (nil, nil) when there is no file or no hub pin yet.
func LoadAcceptedRevocations(stateDir string) (*Signed[RevocationBody], error) {
	b, err := os.ReadFile(Path(stateDir, RevocationsFile))
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
		return nil, fmt.Errorf("%s is damaged", Path(stateDir, RevocationsFile))
	}
	s, err := VerifyRevocations(d, t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Path(stateDir, RevocationsFile), err)
	}
	return &s, nil
}

// SaveAcceptedRevocations verifies a received revocation list with this machine's pins and
// stores it when NextRevocations takes it (a newer version that keeps every stored entry).
// accepted is false, with no error, for a version not above the stored one.
func SaveAcceptedRevocations(stateDir string, d Doc) (s Signed[RevocationBody], accepted bool, err error) {
	t, err := PinnedTrust(stateDir)
	if err != nil {
		return s, false, fmt.Errorf("this machine has not pinned a hub key: %w", err)
	}
	s, err = VerifyRevocations(d, t)
	if err != nil {
		return s, false, err
	}
	var cur *RevocationBody
	if c, err := LoadAcceptedRevocations(stateDir); err == nil && c != nil {
		cur = &c.Body
	}
	ok, err := NextRevocations(cur, s.Body)
	if err != nil || !ok {
		return s, false, err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return s, false, err
	}
	return s, true, writePrivate(Path(stateDir, RevocationsFile), append(b, '\n'))
}
