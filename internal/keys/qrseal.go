package keys

import (
	"errors"
	"io"
)

// SealKeyLen is the length of the QR's registration key k (keys.md 7).
const SealKeyLen = 16

const regAAD = "sy-reg-v1\n"

// ErrSeal means keys_sealed did not open with this code's k and secret.
var ErrSeal = errors.New("key registration invalid")

// SealKeys seals a registration (the keys JSON) with the QR's k: b64u(nonce ‖ AES-128-GCM(k,
// nonce, keys JSON, aad "sy-reg-v1\n" ‖ secret)) (keys.md 7.1). The phone does this; Go has it
// for tests and the shared vectors. rnd nil = crypto/rand.
func SealKeys(k []byte, secret string, keysJSON []byte, rnd io.Reader) (string, error) {
	if len(k) != SealKeyLen {
		return "", errors.New("seal key must be 16 bytes")
	}
	a, err := gcm(k)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := io.ReadFull(randOr(rnd), nonce); err != nil {
		return "", err
	}
	return B64(a.Seal(nonce, nonce, keysJSON, []byte(regAAD+secret))), nil
}

// OpenKeys opens keys_sealed with k and the claim's secret. Any failure is ErrSeal.
func OpenKeys(k []byte, secret, sealed string) ([]byte, error) {
	if len(k) != SealKeyLen {
		return nil, ErrSeal
	}
	b, err := UnB64(sealed)
	if err != nil {
		return nil, ErrSeal
	}
	a, err := gcm(k)
	if err != nil || len(b) < a.NonceSize()+a.Overhead() {
		return nil, ErrSeal
	}
	out, err := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte(regAAD+secret))
	if err != nil {
		return nil, ErrSeal
	}
	return out, nil
}
