// Package hubkeys is the hub's side of keys.md: the hub signing key in <state>/hub_sign.key,
// owner-key proposals and their confirmation, device certificates, revocation lists, and the
// re-verification of every stored document when it is loaded. The formats and checks are in
// internal/keys; this package adds the database and the hub key.
//
// Contract: docs/spec/keys.md 3.3-3.4, 4, 6, 7.1. Depends on: keys, hub/store, paths.
package hubkeys

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/paths"
)

// KeyFile is the hub signing key's file name in the state folder (keys.md 3.4).
const KeyFile = "hub_sign.key"

// HubKey is the hub's Ed25519 signing key. The private half never leaves this type.
type HubKey struct {
	priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
	FP   string
}

// PublicKey is the JSON form of a public key (api.yaml PublicKey).
type PublicKey struct {
	Alg string `json:"alg"`
	Pub string `json:"pub"`
	FP  string `json:"fp"`
}

// FromSeed makes a hub key from a 32-byte seed (tests and the shared vectors).
func FromSeed(seed []byte) (*HubKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("hub key: want %d bytes, have %d", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return &HubKey{priv: priv, Pub: pub, FP: keys.Fingerprint(pub)}, nil
}

// DefaultPath is <state>/hub_sign.key.
func DefaultPath() (string, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return "", err
	}
	return filepath.Join(d.State.Path, KeyFile), nil
}

// Load reads the key file. It refuses a file other users can read (Unix).
func Load(path string) (*HubKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users (mode %v); run chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(b)
	k, err := FromSeed(b)
	if err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", path, err)
	}
	return k, nil
}

// LoadOrCreate reads the key file, making it (32 random bytes, mode 0600) at first use. A
// concurrent creator (the CLI and the server) cannot expose a half-written key: the file is
// written privately and linked into place, and the first link wins.
func LoadOrCreate(path string) (*HubKey, error) {
	if k, err := Load(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return k, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	seed := make([]byte, ed25519.SeedSize)
	defer clear(seed)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), KeyFile+".tmp*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o600) // CreateTemp already makes it 0600; Windows has no modes
	if _, err := tmp.Write(seed); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	err = os.Link(tmp.Name(), path)
	switch {
	case err == nil, errors.Is(err, fs.ErrExist):
		// Ours, or another creator's that won: either way the file at path is the key, and
		// an existing key file is never replaced.
	default:
		// No hard links on this file system: create the file exclusively instead.
		if werr := writeExclusive(path, seed); werr != nil && !errors.Is(werr, fs.ErrExist) {
			return nil, werr
		}
	}
	return Load(path)
}

// writeExclusive writes b to a new file at path (mode 0600); fs.ErrExist when there is one.
func writeExclusive(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Sign adds the hub's signature to a document.
func (k *HubKey) Sign(d *keys.Doc) error { return d.Sign(keys.ByHub, k.priv) }

// SignLink signs a link input (the challenge, welcome and frames, link.md 3.1, 4.3), which
// the link package builds. Anything not starting "wy-link-v1" is refused, so this can never
// make a document signature (keys.md 4.1's domain separation).
func (k *HubKey) SignLink(input []byte) ([]byte, error) {
	if !bytes.HasPrefix(input, []byte("wy-link-v1")) {
		return nil, errors.New("hub key: SignLink only signs wy-link-v1 inputs")
	}
	return ed25519.Sign(k.priv, input), nil
}

// Public is the key's JSON form.
func (k *HubKey) Public() PublicKey {
	return PublicKey{Alg: keys.AlgEd25519, Pub: keys.B64(k.Pub), FP: k.FP}
}

// Trust is a verifier holding only the hub key.
func (k *HubKey) Trust() keys.Trust { return keys.Trust{Hub: k.Pub} }
