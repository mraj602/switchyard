package apply

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLocked means another wy command that changes the setup holds <state>/setup.lock.
var ErrLocked = errors.New("another wy setup, undo, account or uninstall command is running (setup.lock)")

// LockPath returns <state>/setup.lock.
func LockPath(stateDir string) string { return filepath.Join(stateDir, "setup.lock") }

// Lock takes <state>/setup.lock without waiting; ErrLocked when another process holds it.
// Every command that changes the setup or its backup sets takes it first (setup, its
// recovery of an interrupted run, --undo, account remove, uninstall); `wy tick` skips its
// share sync while it is held. The lock dies with the process.
func Lock(stateDir string) (unlock func(), err error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	unlock, held, err := tryLock(LockPath(stateDir))
	if err != nil {
		return nil, err
	}
	if held {
		return nil, ErrLocked
	}
	return unlock, nil
}
