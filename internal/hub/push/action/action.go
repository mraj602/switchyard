// Package action makes and checks the signed action tokens that let a push's Snooze and
// Seen buttons work without a session (push.md 8):
//
//	a1.<base64url(payload)>.<base64url(mac)>
//	payload = alert id ‖ 0x00 ‖ action ‖ 0x00 ‖ fire_count (uint32 BE) ‖ target id ‖ 0x00 ‖ expires (uint64 BE unix s)
//	mac     = first 16 bytes of HMAC-SHA256(key, "a1" ‖ payload)
//
// The token is one-time for snooze because the engine applies it only while the alert's
// fire_count equals the token's; no table of used tokens is needed.
package action

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// Actions.
const (
	Snooze = "snooze"
	Seen   = "seen"
)

// Version is the token prefix.
const Version = "a1"

// Lifetime is how long a token is valid after the fire it belongs to.
const Lifetime = 24 * time.Hour

const macLen = 16

// Token is a decoded action token.
type Token struct {
	Alert   string
	Action  string // Snooze or Seen
	Fire    uint32 // alert.fire_count the token was made for
	Target  string // target id the push went to
	Expires time.Time
}

var b64 = base64.RawURLEncoding

func mac(key, payload []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(Version))
	h.Write(payload)
	return h.Sum(nil)[:macLen]
}

// Encode signs t with key.
func Encode(key []byte, t Token) string {
	var p bytes.Buffer
	p.WriteString(t.Alert)
	p.WriteByte(0)
	p.WriteString(t.Action)
	p.WriteByte(0)
	_ = binary.Write(&p, binary.BigEndian, t.Fire)
	p.WriteString(t.Target)
	p.WriteByte(0)
	_ = binary.Write(&p, binary.BigEndian, uint64(t.Expires.Unix()))
	payload := p.Bytes()
	return Version + "." + b64.EncodeToString(payload) + "." + b64.EncodeToString(mac(key, payload))
}

// Decode checks the shape and the MAC (constant time) of s; it does not check the expiry
// (Verify does). Errors are model.ErrBadToken (malformed) and model.ErrBadMAC.
func Decode(key []byte, s string) (Token, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] != Version {
		return Token{}, model.ErrBadToken
	}
	payload, err1 := b64.DecodeString(parts[1])
	sum, err2 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || len(sum) != macLen || len(payload) > 1024 {
		return Token{}, model.ErrBadToken
	}
	if !hmac.Equal(sum, mac(key, payload)) {
		return Token{}, model.ErrBadMAC
	}
	var t Token
	alert, rest, ok := bytes.Cut(payload, []byte{0})
	if !ok {
		return Token{}, model.ErrBadToken
	}
	act, rest, ok := bytes.Cut(rest, []byte{0})
	if !ok || len(rest) < 4 {
		return Token{}, model.ErrBadToken
	}
	t.Alert, t.Action = string(alert), string(act)
	t.Fire = binary.BigEndian.Uint32(rest[:4])
	target, rest, ok := bytes.Cut(rest[4:], []byte{0})
	if !ok || len(rest) != 8 {
		return Token{}, model.ErrBadToken
	}
	t.Target = string(target)
	t.Expires = time.Unix(int64(binary.BigEndian.Uint64(rest)), 0).UTC()
	if t.Alert == "" || (t.Action != Snooze && t.Action != Seen) {
		return Token{}, model.ErrBadToken
	}
	return t, nil
}

// Verify is Decode plus the expiry check (model.ErrExpired once now is past Expires).
func Verify(key []byte, s string, now time.Time) (Token, error) {
	t, err := Decode(key, s)
	if err != nil {
		return t, err
	}
	if now.After(t.Expires) {
		return t, model.ErrExpired
	}
	return t, nil
}
