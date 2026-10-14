package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Sealed-file parameters (keys.md 3.1).
const (
	// DefaultIter is the PBKDF2-SHA256 iteration count of a new sealed file (OWASP 2023).
	DefaultIter = 600_000
	// MinPassphrase is the shortest passphrase sy takes, in characters.
	MinPassphrase = 12
	// maxIter bounds what a damaged or hostile file can make us compute.
	maxIter   = 50_000_000
	ownerAAD  = "sy-owner-v1\n"
	kdfPBKDF2 = "pbkdf2-sha256"
)

// Errors of the owner key file and the recovery code.
var (
	ErrShortPassphrase = fmt.Errorf("the passphrase must be at least %d characters (four or more random words are best)", MinPassphrase)
	ErrWrongPassphrase = errors.New("wrong passphrase (or the file was changed)")
	ErrRecoveryCode    = errors.New("that is not a recovery code (SYR1-…)")
	ErrRecoveryCheck   = errors.New("the recovery code has a typo: its check group does not match")
)

// SealedOwner is <state>/keys/owner.sealed (keys.md 3.1).
type SealedOwner struct {
	V   int    `json:"v"`
	Alg string `json:"alg"`
	Pub string `json:"pub"`
	FP  string `json:"fp"`
	KDF struct {
		ID   string `json:"id"`
		Iter int    `json:"iter"`
		Salt string `json:"salt"`
	} `json:"kdf"`
	Nonce     string `json:"nonce"`
	CT        string `json:"ct"`
	CreatedAt string `json:"created_at"`
}

// CheckPassphrase refuses passphrases shorter than MinPassphrase characters.
func CheckPassphrase(pass string) error {
	if utf8.RuneCountInString(pass) < MinPassphrase {
		return ErrShortPassphrase
	}
	return nil
}

func ownerKEK(pass string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, salt, iter, 32)
}

func gcm(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// SealOwner seals an Ed25519 seed under pass: PBKDF2-HMAC-SHA256 (iter rounds, 16-byte salt)
// then AES-256-GCM with aad "sy-owner-v1\n" ‖ fp, so a file whose pub or fp was changed does
// not open. rnd nil = crypto/rand.
func SealOwner(seed []byte, pass string, iter int, now time.Time, rnd io.Reader) (SealedOwner, error) {
	if err := CheckPassphrase(pass); err != nil {
		return SealedOwner{}, err
	}
	if len(seed) != ed25519.SeedSize {
		return SealedOwner{}, errors.New("owner seed must be 32 bytes")
	}
	if iter < 1 || iter > maxIter {
		return SealedOwner{}, fmt.Errorf("iteration count %d out of range", iter)
	}
	rnd = randOr(rnd)
	var salt [16]byte
	var nonce [12]byte
	if _, err := io.ReadFull(rnd, salt[:]); err != nil {
		return SealedOwner{}, err
	}
	if _, err := io.ReadFull(rnd, nonce[:]); err != nil {
		return SealedOwner{}, err
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	fp := Fingerprint(pub)
	kek, err := ownerKEK(pass, salt[:], iter)
	if err != nil {
		return SealedOwner{}, err
	}
	defer clear(kek)
	a, err := gcm(kek)
	if err != nil {
		return SealedOwner{}, err
	}
	s := SealedOwner{V: 1, Alg: AlgEd25519, Pub: B64(pub), FP: fp, Nonce: B64(nonce[:]),
		CT: B64(a.Seal(nil, nonce[:], seed, []byte(ownerAAD+fp))), CreatedAt: FormatTime(now)}
	s.KDF.ID, s.KDF.Iter, s.KDF.Salt = kdfPBKDF2, iter, B64(salt[:])
	return s, nil
}

// Open unseals the owner key with pass. The caller zeroes the key when done (best effort).
func (s SealedOwner) Open(pass string) (ed25519.PrivateKey, error) {
	if s.V != 1 || s.Alg != AlgEd25519 || s.KDF.ID != kdfPBKDF2 {
		return nil, errors.New("owner.sealed: unknown format")
	}
	if s.KDF.Iter < 1 || s.KDF.Iter > maxIter {
		return nil, errors.New("owner.sealed: iteration count out of range")
	}
	salt, err := unB64Len(s.KDF.Salt, 16, "owner.sealed salt")
	if err != nil {
		return nil, err
	}
	nonce, err := unB64Len(s.Nonce, 12, "owner.sealed nonce")
	if err != nil {
		return nil, err
	}
	ct, err := unB64Len(s.CT, ed25519.SeedSize+16, "owner.sealed ct")
	if err != nil {
		return nil, err
	}
	kek, err := ownerKEK(pass, salt, s.KDF.Iter)
	if err != nil {
		return nil, err
	}
	defer clear(kek)
	a, err := gcm(kek)
	if err != nil {
		return nil, err
	}
	seed, err := a.Open(nil, nonce, ct, []byte(ownerAAD+s.FP))
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	defer clear(seed)
	priv := ed25519.NewKeyFromSeed(seed)
	if pub, err := UnB64(s.Pub); err != nil || !priv.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(pub)) || Fingerprint(pub) != s.FP {
		clear(priv)
		return nil, ErrWrongPassphrase
	}
	return priv, nil
}

// PubKey is the sealed key's public half (not secret; the file holds it in the clear).
func (s SealedOwner) PubKey() (ed25519.PublicKey, error) {
	b, err := unB64Len(s.Pub, ed25519.PublicKeySize, "owner.sealed pub")
	return b, err
}

// ReadSealedOwner reads owner.sealed.
func ReadSealedOwner(path string) (SealedOwner, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return SealedOwner{}, err
	}
	var s SealedOwner
	if err := json.Unmarshal(b, &s); err != nil {
		return SealedOwner{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// WriteSealedOwner writes owner.sealed with mode 0600, its folder 0700.
func WriteSealedOwner(path string, s SealedOwner) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(b, '\n'))
}

// ---- recovery code (keys.md 3.2) ----

const (
	recoveryPrefix = "SYR1-"
	recoverySalt   = "sy-owner-v1"
	recoveryInfo   = "ed25519 seed"
	recoveryCheck  = "sy-recovery-v1"
	recoveryChars  = 52 // base32 of 32 bytes, unpadded
)

// NewRecoverySecret draws the 32-byte secret R the owner key is made from.
func NewRecoverySecret(rnd io.Reader) ([32]byte, error) {
	var r [32]byte
	_, err := io.ReadFull(randOr(rnd), r[:])
	return r, err
}

// SeedFromRecovery is HKDF-SHA256(secret R, salt "sy-owner-v1", info "ed25519 seed", 32).
func SeedFromRecovery(r [32]byte) ([]byte, error) {
	return hkdf.Key(sha256.New, r[:], []byte(recoverySalt), recoveryInfo, ed25519.SeedSize)
}

func recoveryCheckGroup(r [32]byte) string {
	h := sha256.Sum256(append([]byte(recoveryCheck), r[:]...))
	return b32.EncodeToString(h[:])[:4]
}

// RecoveryCode is "SYR1-" + base32(R) in groups of 4 + "-" + the 4-character check.
func RecoveryCode(r [32]byte) string {
	enc := b32.EncodeToString(r[:])
	var groups []string
	for i := 0; i < len(enc); i += 4 {
		groups = append(groups, enc[i:min(i+4, len(enc))])
	}
	return recoveryPrefix + strings.Join(groups, "-") + "-" + recoveryCheckGroup(r)
}

// ParseRecoveryCode reads a recovery code typed back: any case, spaces and dashes ignored,
// 0 read as O, 1 as I and 8 as B. A wrong check group is ErrRecoveryCheck.
func ParseRecoveryCode(s string) ([32]byte, error) {
	var r [32]byte
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(s))
	if !strings.HasPrefix(s, "SYR1") && !strings.HasPrefix(s, "SYRI") {
		return r, ErrRecoveryCode
	}
	s = strings.NewReplacer("0", "O", "1", "I", "8", "B").Replace(s[4:])
	if len(s) != recoveryChars+4 {
		return r, ErrRecoveryCode
	}
	b, err := b32.DecodeString(s[:recoveryChars])
	if err != nil || len(b) != 32 {
		return r, ErrRecoveryCode
	}
	copy(r[:], b)
	clear(b)
	if recoveryCheckGroup(r) != s[recoveryChars:] {
		return [32]byte{}, ErrRecoveryCheck
	}
	return r, nil
}

// OwnerFromRecovery rebuilds the owner key from R.
func OwnerFromRecovery(r [32]byte) (ed25519.PrivateKey, error) {
	seed, err := SeedFromRecovery(r)
	if err != nil {
		return nil, err
	}
	defer clear(seed)
	return ed25519.NewKeyFromSeed(seed), nil
}
