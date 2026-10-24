package identity

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// Machine is the content of <state>/machine.json (identity.md section 2).
type Machine struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
}

// MachineOptions injects the sources of randomness, time and hostname; the zero value
// uses the real ones.
type MachineOptions struct {
	Rand     io.Reader
	Now      func() time.Time
	Hostname func() (string, error)
}

const (
	machineFile = "machine.json"
	lockFile    = "machine.lock"
)

// LoadOrCreateMachine returns the machine of this state folder, creating machine.json
// under <state>/machine.lock on first use so that concurrent first runs agree on one id.
// The id is random (never derived from hardware). A corrupt machine.json is an error: it
// is never silently replaced, because that would turn this machine into a new one.
func LoadOrCreateMachine(stateDir string, opt MachineOptions) (Machine, error) {
	path := filepath.Join(stateDir, machineFile)
	if m, err := readMachine(path); err == nil {
		return m, nil // fast path: no lock once the file exists
	} else if !errors.Is(err, os.ErrNotExist) {
		return Machine{}, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return Machine{}, err
	}
	unlock, err := lock(filepath.Join(stateDir, lockFile))
	if err != nil {
		return Machine{}, fmt.Errorf("lock machine id: %w", err)
	}
	defer unlock()
	// Someone may have created it while we waited.
	if m, err := readMachine(path); err == nil {
		return m, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Machine{}, err
	}

	r, now, host := opt.Rand, opt.Now, opt.Hostname
	if r == nil {
		r = rand.Reader
	}
	if now == nil {
		now = time.Now
	}
	if host == nil {
		host = os.Hostname
	}
	t := now().UTC()
	id, err := newULID(r, t)
	if err != nil {
		return Machine{}, err
	}
	m := Machine{ID: "m_" + id, Name: shortHostname(host), Created: t.Format("2006-01-02T15:04:05.000Z")}
	data, err := json.Marshal(m)
	if err != nil {
		return Machine{}, err
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return Machine{}, err
	}
	return m, nil
}

func readMachine(path string) (Machine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Machine{}, err
	}
	var m Machine
	if err := json.Unmarshal(data, &m); err != nil || !strings.HasPrefix(m.ID, "m_") || len(m.ID) != 28 {
		return Machine{}, fmt.Errorf("%s is not a valid machine file; move it away to make this a new machine", path)
	}
	return m, nil
}

// shortHostname: lower case, up to the first dot; "machine" if unknown.
func shortHostname(host func() (string, error)) string {
	h, err := host()
	if err != nil {
		return "machine"
	}
	h, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(h)), ".")
	if h == "" {
		return "machine"
	}
	return ClampMachineName(h)
}

// ClampMachineName cuts a name to MaxMachineName characters on a rune boundary (the hub
// counts runes, pair.MaxNameLen), trimming space left at the cut. A name that is already
// short enough comes back unchanged.
func ClampMachineName(n string) string {
	if utf8.RuneCountInString(n) <= MaxMachineName {
		return n
	}
	r := []rune(n)
	return strings.TrimSpace(string(r[:MaxMachineName]))
}

// newULID makes a 26-character Crockford base32 ULID: 48-bit millisecond time, 80 random
// bits.
func newULID(r io.Reader, t time.Time) (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", fmt.Errorf("random source: %w", err)
	}
	// 128 bits -> 26 groups of 5 bits (the first holds 3 bits).
	var out [26]byte
	var acc, bits int
	idx := 25
	for i := 15; i >= 0; i-- {
		acc |= int(b[i]) << bits
		bits += 8
		for bits >= 5 && idx >= 0 {
			out[idx] = alphabet[acc&31]
			idx--
			acc >>= 5
			bits -= 5
		}
	}
	if idx >= 0 {
		out[idx] = alphabet[acc&31]
	}
	return string(out[:]), nil
}
