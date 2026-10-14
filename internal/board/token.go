package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mraj602/switchyard/internal/atomicfile"
)

// The board token (docs/spec/terminal.md T3): a read-only desktop-kind device token that
// `sy board connect` stores in <state>/board/token.json (mode 0600) and the board reads. It
// is bound to the hub URL it was issued for: a token whose URL differs from hub.url is
// dropped, as the desktop app does (desktop.md C6). The token never leaves this file: it is
// not printed, not put in argv or the environment, and not logged.

// TokenName is the device name `sy board connect` asks the hub for. T3 says
// "terminal · <machine name>", but the name travels as one word of a remote command through
// the admin channel (ssh), which accepts only letters, digits and `_ . - ` (and desktop.NameRE
// refuses the middle dot), so it is "terminal-<machine>" with every other character of the
// machine name replaced by "-". At most 40 characters.
func TokenName(machine string) string {
	var b strings.Builder
	for _, r := range machine {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := "terminal-" + b.String()
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

// TokenFile is the token's path under the state folder.
func TokenFile(stateDir string) string { return filepath.Join(stateDir, "board", "token.json") }

// Token is the content of token.json.
type Token struct {
	V      int    `json:"v"`
	URL    string `json:"url"`
	Device string `json:"device"`
	Token  string `json:"token"`
}

// String never shows the secret (a stray %v or %+v in a log line must not leak it).
func (t Token) String() string { return fmt.Sprintf("board token for %s (device %s)", t.URL, t.Device) }

// GoString is String for %#v.
func (t Token) GoString() string { return t.String() }

// ErrNoToken is returned by LoadToken when there is no usable token for the hub URL.
var ErrNoToken = errors.New("board: no board token for this hub")

// SaveToken writes <state>/board/token.json atomically with mode 0600.
func SaveToken(stateDir string, t Token) error {
	if t.URL == "" || t.Token == "" || t.Device == "" {
		return errors.New("board: a token needs url, device and token")
	}
	t.V = 1
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	path := TokenFile(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// LoadToken returns the stored token when it was issued for hubURL. A missing file returns
// ErrNoToken. A file that does not parse, or is bound to another URL, also returns
// ErrNoToken and is deleted (the person runs `sy board connect` again).
func LoadToken(stateDir, hubURL string) (Token, error) {
	path := TokenFile(stateDir)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Token{}, ErrNoToken
		}
		return Token{}, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil || t.V != 1 || t.Token == "" || t.URL == "" || !SameHub(t.URL, hubURL) {
		_ = os.Remove(path)
		return Token{}, ErrNoToken
	}
	return t, nil
}

// RemoveToken deletes token.json and reports whether a file was there.
func RemoveToken(stateDir string) (bool, error) {
	err := os.Remove(TokenFile(stateDir))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// RemoveTokenIf deletes token.json only while it still holds secret: a board that was
// refused (401) with an old token must not delete one `sy board connect` has saved since.
func RemoveTokenIf(stateDir, secret string) (bool, error) {
	cur, ok := PeekToken(stateDir)
	if !ok || cur.Token != secret {
		return false, nil
	}
	return RemoveToken(stateDir)
}

// SameHub compares two hub URLs ignoring case and a trailing slash.
func SameHub(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	return norm(a) == norm(b)
}

// PeekToken reads token.json without checking the hub URL or deleting anything. `sy board
// disconnect` uses it to name the device to revoke. ok is false when there is no readable
// file.
func PeekToken(stateDir string) (Token, bool) {
	b, err := os.ReadFile(TokenFile(stateDir))
	if err != nil {
		return Token{}, false
	}
	var t Token
	if json.Unmarshal(b, &t) != nil || t.Token == "" {
		return Token{}, false
	}
	return t, true
}
