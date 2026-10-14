package link

import (
	"crypto/ecdh"
	"crypto/hpke"
	"fmt"
)

// SealInfo is HPKE's info for a machine's queued body (link.md 4.2): it binds the body to the
// device and the frame type.
func SealInfo(device, t string) []byte { return lines("sy-link-v1 seal", device, t) }

// SealMachine seals a queued body to a machine's X25519 box key (link.md 4.2): HPKE base
// mode, DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305, single shot. The result
// is enc (32 bytes) ‖ ciphertext.
func SealMachine(boxPub []byte, device, t string, plaintext []byte) ([]byte, error) {
	return SealHPKE(boxPub, SealInfo(device, t), plaintext)
}

// OpenMachine opens a body sealed by SealMachine with the machine's box key.
func OpenMachine(priv *ecdh.PrivateKey, device, t string, body []byte) ([]byte, error) {
	return OpenHPKE(priv, SealInfo(device, t), body)
}

// SealHPKE is the machine suite of link.md 4.2 with the caller's info: frame bodies use
// SealInfo, peer envelopes their own (peers.md 8.2).
func SealHPKE(boxPub, info, plaintext []byte) ([]byte, error) {
	pk, err := ecdh.X25519().NewPublicKey(boxPub)
	if err != nil {
		return nil, fmt.Errorf("link: box key: %w", err)
	}
	hp, err := hpke.NewDHKEMPublicKey(pk)
	if err != nil {
		return nil, err
	}
	return hpke.Seal(hp, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), info, plaintext)
}

// OpenHPKE opens a body sealed by SealHPKE with the same info; ErrOpen when it does not open.
func OpenHPKE(priv *ecdh.PrivateKey, info, body []byte) ([]byte, error) {
	sk, err := hpke.NewDHKEMPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	b, err := hpke.Open(sk, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), info, body)
	if err != nil {
		return nil, ErrOpen
	}
	return b, nil
}
