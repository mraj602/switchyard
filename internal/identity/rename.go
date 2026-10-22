package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mraj602/switchyard/internal/atomicfile"
)

// MaxMachineName is the longest machine name (characters), as the hub's PATCH /v1/machines
// accepts (machines.md 6.2).
const MaxMachineName = 60

// CleanMachineName trims a machine name and checks it: 1-60 characters, no control
// characters.
func CleanMachineName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || !utf8.ValidString(n) {
		return "", errors.New("the machine name is empty")
	}
	if utf8.RuneCountInString(n) > MaxMachineName {
		return "", fmt.Errorf("the machine name is longer than %d characters", MaxMachineName)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", errors.New("the machine name has a control character")
		}
	}
	return n, nil
}

// RenameMachine changes the name in <state>/machine.json (identity.md 2, `sy machine
// rename`), creating the file first when it does not exist. The id never changes. The write
// is atomic and holds <state>/machine.lock, like the first creation.
func RenameMachine(stateDir, name string, opt MachineOptions) (old Machine, renamed Machine, err error) {
	n, err := CleanMachineName(name)
	if err != nil {
		return Machine{}, Machine{}, err
	}
	if _, err := LoadOrCreateMachine(stateDir, opt); err != nil {
		return Machine{}, Machine{}, err
	}
	unlock, err := lock(filepath.Join(stateDir, lockFile))
	if err != nil {
		return Machine{}, Machine{}, fmt.Errorf("lock machine id: %w", err)
	}
	defer unlock()
	path := filepath.Join(stateDir, machineFile)
	m, err := readMachine(path)
	if err != nil {
		return Machine{}, Machine{}, err
	}
	old = m
	if m.Name == n {
		return old, m, nil
	}
	m.Name = n
	data, err := json.Marshal(m)
	if err != nil {
		return Machine{}, Machine{}, err
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return Machine{}, Machine{}, err
	}
	return old, m, nil
}
