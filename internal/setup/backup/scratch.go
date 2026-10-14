package backup

import (
	"os"
	"path/filepath"
)

// Scratch returns a fresh absolute path inside the set (pending/NNNNNN, not created; its
// folder is) where an action can build its result before the journaled write moves it into
// place, so the intended state is known before the intent is written.
func (s *Set) Scratch() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.Dir, "pending")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, objName(s.nextObj())), nil
}
