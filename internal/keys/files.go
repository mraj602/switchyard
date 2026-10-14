package keys

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mraj602/switchyard/internal/atomicfile"
)

// Files under <state>/keys (keys.md 10, paths.md 3).
const (
	Dir             = "keys"
	DeviceFile      = "device.json"
	OwnerSealedFile = "owner.sealed"
	OwnerPinFile    = "owner.pub"
	HubPinFile      = "hub.pub"
	CertsFile       = "certs.json"
	MatrixFile      = "matrix.json"
	RevocationsFile = "revocations.json"
)

// Path is <state>/keys/<name>.
func Path(stateDir, name string) string { return filepath.Join(stateDir, Dir, name) }

func randOr(r io.Reader) io.Reader {
	if r == nil {
		return rand.Reader
	}
	return r
}

// writePrivate writes a file with mode 0600 in a folder with mode 0700.
func writePrivate(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// ---- this device's keys ----

// DeviceKeys is <state>/keys/device.json: this device's Ed25519 sign key and X25519 box key
// (keys.md 10). Machines and desktops only; phones keep theirs in the Android Keystore.
type DeviceKeys struct {
	V    int `json:"v"`
	Sign struct {
		Alg  string `json:"alg"`
		Seed string `json:"seed"`
	} `json:"sign"`
	Box struct {
		Alg  string `json:"alg"`
		Priv string `json:"priv"`
	} `json:"box"`
	Device    string `json:"device"` // the hub's device id, once registered
	CreatedAt string `json:"created_at"`
}

// NewDeviceKeys makes fresh keys. rnd nil = crypto/rand.
func NewDeviceKeys(now time.Time, rnd io.Reader) (DeviceKeys, error) {
	rnd = randOr(rnd)
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rnd, seed); err != nil {
		return DeviceKeys{}, err
	}
	box := make([]byte, 32)
	if _, err := io.ReadFull(rnd, box); err != nil {
		return DeviceKeys{}, err
	}
	if _, err := ecdh.X25519().NewPrivateKey(box); err != nil {
		return DeviceKeys{}, err
	}
	var k DeviceKeys
	k.V, k.CreatedAt = 1, FormatTime(now)
	k.Sign.Alg, k.Sign.Seed = AlgEd25519, B64(seed)
	k.Box.Alg, k.Box.Priv = AlgX25519, B64(box)
	return k, nil
}

// SignKey is the Ed25519 private key.
func (k DeviceKeys) SignKey() (ed25519.PrivateKey, error) {
	seed, err := unB64Len(k.Sign.Seed, ed25519.SeedSize, "device.json sign seed")
	if err != nil || k.Sign.Alg != AlgEd25519 {
		return nil, errors.New("device.json: bad sign key")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// BoxKey is the X25519 private key.
func (k DeviceKeys) BoxKey() (*ecdh.PrivateKey, error) {
	b, err := unB64Len(k.Box.Priv, 32, "device.json box key")
	if err != nil || k.Box.Alg != AlgX25519 {
		return nil, errors.New("device.json: bad box key")
	}
	return ecdh.X25519().NewPrivateKey(b)
}

// Public is the registration form of the keys (DeviceKeysNew).
func (k DeviceKeys) Public() (DeviceKeysNew, error) {
	s, err := k.SignKey()
	if err != nil {
		return DeviceKeysNew{}, err
	}
	b, err := k.BoxKey()
	if err != nil {
		return DeviceKeysNew{}, err
	}
	return DeviceKeysNew{
		Sign: KeySpec{Alg: AlgEd25519, Pub: B64(s.Public().(ed25519.PublicKey))},
		Box:  KeySpec{Alg: AlgX25519, Pub: B64(b.PublicKey().Bytes())},
	}, nil
}

// ReadDeviceKeys reads device.json (os.ErrNotExist when there is none).
func ReadDeviceKeys(stateDir string) (DeviceKeys, error) {
	b, err := os.ReadFile(Path(stateDir, DeviceFile))
	if err != nil {
		return DeviceKeys{}, err
	}
	var k DeviceKeys
	if err := json.Unmarshal(b, &k); err != nil || k.V != 1 {
		return DeviceKeys{}, fmt.Errorf("%s is damaged", Path(stateDir, DeviceFile))
	}
	if _, err := k.Public(); err != nil {
		return DeviceKeys{}, err
	}
	return k, nil
}

// WriteDeviceKeys writes device.json, mode 0600.
func WriteDeviceKeys(stateDir string, k DeviceKeys) error {
	b, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(Path(stateDir, DeviceFile), append(b, '\n'))
}

// LoadOrCreateDeviceKeys returns device.json, making and writing new keys when there is none.
func LoadOrCreateDeviceKeys(stateDir string, now time.Time) (DeviceKeys, bool, error) {
	k, err := ReadDeviceKeys(stateDir)
	if err == nil {
		return k, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return DeviceKeys{}, false, err
	}
	if k, err = NewDeviceKeys(now, nil); err != nil {
		return DeviceKeys{}, false, err
	}
	return k, true, WriteDeviceKeys(stateDir, k)
}

// ---- pins (keys.md 3.5) ----

// How a pin was set.
const (
	PinQR    = "qr"
	PinTOFU  = "tofu"
	PinSSH   = "ssh"
	PinLocal = "local"
	PinHand  = "hand" // sy keys trust-owner / sy agent enable --repin-hub
)

// Pin is <state>/keys/hub.pub or owner.pub.
type Pin struct {
	Pub      string `json:"pub"`
	FP       string `json:"fp"`
	PinnedAt string `json:"pinned_at"`
	How      string `json:"how"`
}

// ErrPinned means a pin is already there: nothing received replaces it (keys.md 3.5).
var ErrPinned = errors.New("a different key is already pinned; only sy keys trust-owner / sy agent enable --repin-hub at a terminal change it")

// Key is the pinned key, checked against its fingerprint.
func (p Pin) Key() (ed25519.PublicKey, error) {
	b, err := unB64Len(p.Pub, ed25519.PublicKeySize, "pinned key")
	if err != nil {
		return nil, err
	}
	if Fingerprint(b) != p.FP {
		return nil, errors.New("pinned key does not match its fingerprint")
	}
	return b, nil
}

// NewPin pins pub, set by how.
func NewPin(pub ed25519.PublicKey, how string, now time.Time) Pin {
	return Pin{Pub: B64(pub), FP: Fingerprint(pub), PinnedAt: FormatTime(now), How: how}
}

// ReadPin reads a pin file (os.ErrNotExist when none).
func ReadPin(path string) (Pin, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Pin{}, err
	}
	var p Pin
	if err := json.Unmarshal(b, &p); err != nil {
		return Pin{}, fmt.Errorf("%s is damaged", path)
	}
	if _, err := p.Key(); err != nil {
		return Pin{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// SetPin writes a pin only where there is none, or where the same key is pinned already (a
// no-op). A different pinned key is ErrPinned: this is the path for keys that arrive from
// the network or at pairing.
func SetPin(path string, p Pin) error {
	cur, err := ReadPin(path)
	if err == nil {
		if cur.FP == p.FP {
			return nil
		}
		return ErrPinned
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writePin(path, p)
}

// ReplacePin overwrites a pin. Only for commands typed at a terminal that showed the old and
// the new fingerprint and asked (sy keys trust-owner, sy agent enable --repin-hub), after the
// signing guard.
func ReplacePin(path string, p Pin) error { return writePin(path, p) }

func writePin(path string, p Pin) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(b, '\n'))
}

// PinnedTrust reads this machine's pins: the hub (required) and the owner (nil when none).
func PinnedTrust(stateDir string) (Trust, error) {
	hp, err := ReadPin(Path(stateDir, HubPinFile))
	if err != nil {
		return Trust{}, err
	}
	hub, _ := hp.Key()
	t := Trust{Hub: hub}
	op, err := ReadPin(Path(stateDir, OwnerPinFile))
	if err == nil {
		t.Owner, _ = op.Key()
	} else if !errors.Is(err, os.ErrNotExist) {
		return Trust{}, err
	}
	return t, nil
}
