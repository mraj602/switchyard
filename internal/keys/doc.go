// Package keys holds the key formats and checks shared by wy, the agent and the hub: base64url
// and fingerprints, signed documents (owner_key, device_cert, matrix, revocations,
// peer_envelope) and who may sign them, the passphrase-sealed owner key and its recovery
// code, this device's keys, the pins in <state>/keys, the QR registration seal, and the
// signing guard that refuses owner-key work under a Claude Code session.
//
// The hub's own key and its database rows are in internal/hub/keys. Everything here is
// standard library only (crypto/ed25519, crypto/ecdh, crypto/ecdsa, crypto/pbkdf2,
// crypto/hkdf, crypto/aes, crypto/cipher).
//
// Contract: docs/spec/keys.md. Depends on: atomicfile.
package keys
