package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
)

// Device kinds and their token letters and scopes (tokens.md 1 and 3).
var kinds = map[string]struct {
	letter string
	scopes []string
}{
	"machine": {"m", []string{"write"}},
	"desktop": {"d", []string{"write", "read"}},
	"phone":   {"p", []string{"read", "push"}},
	"browser": {"b", []string{"read", "push"}},
}

// Kinds lists the device kinds a token can be made for.
func Kinds() []string { return []string{"machine", "desktop", "phone", "browser"} }

// ScopesFor returns the scopes of a device kind.
func ScopesFor(kind string) ([]string, bool) {
	k, ok := kinds[kind]
	return append([]string(nil), k.scopes...), ok
}

// NeedsMachine reports whether a kind is bound to one machine id.
func NeedsMachine(kind string) bool { return kind == "machine" || kind == "desktop" }

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// OwnerSessionScopes are the scopes of an owner session (pairing.md 4).
var OwnerSessionScopes = []string{"owner", "read", "push"}

// OwnerSessionAge is how long an owner session lives (pairing.md 4).
const OwnerSessionAge = 30 * 24 * time.Hour

// NewToken makes wy_<k>_<43 base62>: 32 random bytes, big-endian base62, left-padded. kind
// is a device kind, or "owner" for the wy_o_ token of an owner session.
func NewToken(kind string) (string, error) {
	letter := ""
	if kind == "owner" {
		letter = "o"
	} else if k, ok := kinds[kind]; ok {
		letter = k.letter
	} else {
		return "", fmt.Errorf("unknown device kind %q", kind)
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	n := new(big.Int).SetBytes(b[:])
	var digits []byte
	base := big.NewInt(62)
	mod := new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		digits = append(digits, base62[mod.Int64()])
	}
	for len(digits) < 43 {
		digits = append(digits, '0')
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return TokenPrefix + letter + "_" + string(digits), nil
}

// TokenPrefix starts every token the hub makes (tokens.md 1).
const TokenPrefix = "wy_"

// Hash is SHA-256 of the full token string (tokens.md 2).
func Hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// OwnerHash is the stored hash of an owner session's wy_o_ token: HMAC-SHA256(pepper,
// "token:" + token), with the pairing pepper (tokens.md 2, keys.md 5.4). Without the pepper
// file a database writer cannot make a row that a wy_o_ token of its choosing matches.
func OwnerHash(pepper []byte, token string) []byte {
	h := hmac.New(sha256.New, pepper)
	h.Write([]byte("token:"))
	h.Write([]byte(token))
	return h.Sum(nil)
}

// IsOwnerToken reports whether a token has the wy_o_ prefix of an owner session.
func IsOwnerToken(token string) bool {
	return strings.HasPrefix(token, TokenPrefix+"o_")
}

// Hint is the display form: kind prefix + "…" + last 4 characters.
func Hint(token string) string {
	if len(token) < 10 {
		return "…"
	}
	return token[:5] + "…" + token[len(token)-4:]
}

// LooksLikeToken checks the shape wy_<k>_<43 base62> (48 characters).
func LooksLikeToken(s string) bool {
	if len(s) != 48 || !strings.HasPrefix(s, TokenPrefix) || s[4] != '_' {
		return false
	}
	for _, c := range s[5:] {
		if !strings.ContainsRune(base62, c) {
			return false
		}
	}
	return true
}

// CreateRequest is `wy hub token create`.
type CreateRequest struct {
	Kind, Name, Machine string
	CreatedBy           string   // "cli" by default
	Parent              string   // board sessions: the device whose token was pasted; must not be revoked
	Scopes              []string // narrower than the kind's (board sessions); nil = the kind's
	// Owner makes an owner session instead: a browser device with scopes owner, read and
	// push, a wy_o_ token and Expires (default OwnerSessionAge). Kind must be "browser".
	Owner   bool
	Expires time.Duration
	// Pepper is the pairing pepper; required with Owner (the token hash is OwnerHash).
	Pepper []byte
}

// Created is a new device and its token (shown once).
type Created struct {
	Device store.Device
	Token  string
}

// Prepared is a device and token made in memory, ready to insert in a transaction (the
// pairing claim inserts it in the same transaction that consumes the code).
type Prepared struct {
	Created
	Hash      []byte
	ExpiresAt int64 // ms, 0 = never
	Parent    string
}

// Prepare validates req and makes the device and its token at time now. Nothing is stored.
func Prepare(req CreateRequest, now time.Time) (Prepared, error) {
	kind := req.Kind
	scopes, ok := ScopesFor(kind)
	if !ok {
		return Prepared{}, fmt.Errorf("--kind must be one of %s", strings.Join(Kinds(), ", "))
	}
	if req.Owner {
		if kind != "browser" {
			return Prepared{}, errors.New("an owner session is a browser device")
		}
		if len(req.Pepper) < 32 {
			return Prepared{}, errors.New("an owner session needs the pairing pepper")
		}
		scopes = append([]string(nil), OwnerSessionScopes...)
	}
	if strings.TrimSpace(req.Name) == "" {
		return Prepared{}, errors.New("--name is required")
	}
	if NeedsMachine(kind) && req.Machine == "" {
		return Prepared{}, fmt.Errorf("--machine is required for a %s token (the id in <state>/machine.json on that machine)", kind)
	}
	if !NeedsMachine(kind) && req.Machine != "" {
		return Prepared{}, fmt.Errorf("--machine is only for machine and desktop tokens")
	}
	if req.Scopes != nil && !req.Owner {
		scopes = req.Scopes
	}
	tk := kind
	if req.Owner {
		tk = "owner"
	}
	tok, err := NewToken(tk)
	if err != nil {
		return Prepared{}, err
	}
	d := store.Device{
		ID: store.NewID("dev_", now), Kind: kind, Name: req.Name, MachineID: req.Machine,
		TokenHint: Hint(tok), Scopes: scopes, CreatedAt: now.UnixMilli(), CreatedBy: req.CreatedBy,
	}
	if d.CreatedBy == "" {
		d.CreatedBy = "cli"
	}
	p := Prepared{Created: Created{Device: d, Token: tok}, Hash: Hash(tok), Parent: req.Parent}
	if req.Owner {
		p.Hash = OwnerHash(req.Pepper, tok)
		age := req.Expires
		if age <= 0 {
			age = OwnerSessionAge
		}
		p.ExpiresAt = now.Add(age).UnixMilli()
	}
	return p, nil
}

// Insert stores a prepared device in tx. For a board session (Parent set) it first checks
// that the parent is not revoked, in the same transaction, so a revoke that lands in
// between cannot leave a live child (ErrUnauthorized).
func (p Prepared) Insert(tx *store.Tx) error {
	if p.Parent != "" {
		if revoked, err := tx.DeviceRevoked(p.Parent); err != nil {
			return err
		} else if revoked {
			return ErrUnauthorized
		}
	}
	if p.Device.MachineID != "" {
		if _, err := tx.EnsureMachine(p.Device.MachineID, p.Device.Name, p.Device.CreatedAt); err != nil {
			return err
		}
	}
	if err := tx.InsertDevice(p.Device, p.Hash); err != nil {
		return err
	}
	if p.ExpiresAt != 0 {
		return tx.SetDeviceExpiry(p.Device.ID, p.ExpiresAt)
	}
	return nil
}

// Create makes a device and its token. For machine and desktop kinds the machine row is
// created if missing, named after the device.
func Create(ctx context.Context, s *store.Store, req CreateRequest) (Created, error) {
	p, err := Prepare(req, s.Now())
	if err != nil {
		return Created{}, err
	}
	if err := s.Write(ctx, p.Insert); err != nil {
		return Created{}, err
	}
	return p.Created, nil
}

// Find resolves a device by id, token hint or name (`wy hub token revoke`). Only devices
// not yet revoked match a hint or a name.
func Find(ctx context.Context, s *store.Store, q string) ([]store.Device, error) {
	if d, err := s.DeviceByID(ctx, q); err == nil {
		return []store.Device{d}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	all, err := s.Devices(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []store.Device
	for _, d := range all {
		if d.TokenHint == q || d.Name == q {
			out = append(out, d)
		}
	}
	return out, nil
}

// Revoke revokes a device by id, the sessions made from it, deletes their push targets and
// cancels their pending deliveries. It takes effect on the next request (no cache).
func Revoke(ctx context.Context, s *store.Store, id string) error {
	return s.Write(ctx, func(tx *store.Tx) error { return tx.RevokeDeviceCascade(id) })
}

// touchEvery limits last_used_at writes to one a minute per device (tokens.md 3).
const touchEvery = time.Minute
