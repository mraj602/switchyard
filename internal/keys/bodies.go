package keys

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Trust is what a verifier holds: the hub key and, once pinned or active, the owner key. A
// nil Owner verifies nothing the owner signed.
type Trust struct {
	Hub   ed25519.PublicKey
	Owner ed25519.PublicKey
}

// HubFP is the fingerprint of the trusted hub key.
func (t Trust) HubFP() string { return Fingerprint(t.Hub) }

func (t Trust) ring() KeyRing {
	r := KeyRing{ByHub: t.Hub}
	if t.Owner != nil {
		r[ByOwner] = t.Owner
	}
	return r
}

// decodeBody parses verified body bytes into v and checks "v": 1 (keys.md 4.1: unknown
// fields are ignored, another version is a reject).
func decodeBody(b []byte, v any) error {
	var head struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return fmt.Errorf("%w: %v", ErrBody, err)
	}
	if head.V != 1 {
		return fmt.Errorf("%w: version %d, want 1", ErrBody, head.V)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %v", ErrBody, err)
	}
	return nil
}

func validTime(s string) bool {
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}

// ---- owner_key ----

// OwnerKeyBody is the body of an owner_key document (keys.md 4.1).
type OwnerKeyBody struct {
	V         int    `json:"v"`
	Pub       string `json:"pub"`
	Hub       string `json:"hub"`
	CreatedAt string `json:"created_at"`
}

// OwnerKey is a verified owner_key document.
type OwnerKey struct {
	Body OwnerKeyBody
	Pub  ed25519.PublicKey
	FP   string
	// Confirmed is true when the document also carries the hub's confirmation (3.3).
	Confirmed bool
	Doc       Doc
}

// NewOwnerKeyDoc makes the self-signed proposal of an owner key for the hub with
// fingerprint hubFP (keys.md 3.3 step 1).
func NewOwnerKeyDoc(priv ed25519.PrivateKey, hubFP string, now time.Time) (Doc, error) {
	pub := priv.Public().(ed25519.PublicKey)
	body, err := json.Marshal(OwnerKeyBody{V: 1, Pub: B64(pub), Hub: hubFP, CreatedAt: FormatTime(now)})
	if err != nil {
		return Doc{}, err
	}
	d := NewDoc(TypeOwnerKey, body)
	return d, d.Sign(ByOwner, priv)
}

// VerifyOwnerKey checks an owner_key document for the hub key hubPub: the self-signature
// with the key the body names, the hub's confirmation signature when present, and that the
// body is for this hub. The owner signer's key is the body's own pub, so this is the one
// type whose body is read (unverified) to find the key and then verified with it.
func VerifyOwnerKey(d Doc, hubPub ed25519.PublicKey) (OwnerKey, error) {
	raw, err := d.BodyBytes()
	if err != nil {
		return OwnerKey{}, err
	}
	var b OwnerKeyBody
	if err := decodeBody(raw, &b); err != nil {
		return OwnerKey{}, err
	}
	pub, err := unB64Len(b.Pub, ed25519.PublicKeySize, "owner_key pub")
	if err != nil {
		return OwnerKey{}, fmt.Errorf("%w: %v", ErrBody, err)
	}
	if _, err := Verify(d, TypeOwnerKey, KeyRing{ByOwner: pub, ByHub: hubPub}); err != nil {
		return OwnerKey{}, err
	}
	if b.Hub != Fingerprint(hubPub) {
		return OwnerKey{}, fmt.Errorf("%w: owner key proposed to hub %s", ErrWrongHub, GroupFP(b.Hub))
	}
	if !validTime(b.CreatedAt) {
		return OwnerKey{}, fmt.Errorf("%w: created_at", ErrBody)
	}
	return OwnerKey{Body: b, Pub: pub, FP: Fingerprint(pub), Confirmed: d.Has(ByHub), Doc: d}, nil
}

// ---- device_cert ----

// Device kinds that carry keys, and their algorithms (keys.md 4.2).
const (
	AlgEd25519 = "ed25519"
	AlgX25519  = "x25519"
	AlgP256    = "p256"
)

// Registration paths (keys.md 4.2 `reg`).
const (
	RegQR    = "qr"
	RegTOFU  = "tofu"
	RegSSH   = "ssh"
	RegLocal = "local"
)

// KeySpec is one public key: its algorithm and its bytes in base64url.
type KeySpec struct {
	Alg string `json:"alg"`
	Pub string `json:"pub"`
}

// CertBody is the body of a device_cert document (keys.md 4.2).
type CertBody struct {
	V        int     `json:"v"`
	Device   string  `json:"device"`
	Kind     string  `json:"kind"`
	Name     string  `json:"name"`
	Machine  *string `json:"machine"`
	Sign     KeySpec `json:"sign"`
	Box      KeySpec `json:"box"`
	Reg      string  `json:"reg"`
	Hub      string  `json:"hub"`
	IssuedAt string  `json:"issued_at"`
}

// MachineID is the certificate's machine, "" for a phone.
func (c CertBody) MachineID() string {
	if c.Machine == nil {
		return ""
	}
	return *c.Machine
}

// SignPub is the certificate's sign key bytes (checked by VerifyCert).
func (c CertBody) SignPub() []byte { b, _ := UnB64(c.Sign.Pub); return b }

// BoxPub is the certificate's box key bytes.
func (c CertBody) BoxPub() []byte { b, _ := UnB64(c.Box.Pub); return b }

// SignFP and BoxFP are the fingerprints of the two keys.
func (c CertBody) SignFP() string { return Fingerprint(c.SignPub()) }
func (c CertBody) BoxFP() string  { return Fingerprint(c.BoxPub()) }

// Check validates the fields of a certificate body (keys.md 4.2's table).
func (c CertBody) Check() error {
	bad := func(f string) error { return fmt.Errorf("%w: device_cert %s", ErrBody, f) }
	if c.Device == "" {
		return bad("device")
	}
	switch c.Kind {
	case "machine", "desktop":
		if c.Machine == nil || *c.Machine == "" {
			return bad("machine")
		}
	case "phone":
		if c.Machine != nil {
			return bad("machine (null for a phone)")
		}
	default:
		return bad("kind")
	}
	if err := CheckKeys(c.Kind, DeviceKeysNew{Sign: c.Sign, Box: c.Box}, false); err != nil {
		return fmt.Errorf("%w: %v", ErrBody, err)
	}
	if !slices.Contains([]string{RegQR, RegTOFU, RegSSH, RegLocal}, c.Reg) {
		return bad("reg")
	}
	if !ValidFP(c.Hub) {
		return bad("hub")
	}
	if !validTime(c.IssuedAt) {
		return bad("issued_at")
	}
	return nil
}

// Cert is a verified device certificate.
type Cert struct {
	Body CertBody
	// Raw is the body bytes as signed.
	Raw         []byte
	OwnerSigned bool
	Doc         Doc
}

// VerifyCert checks a device_cert document: the hub signature with t.Hub, the owner
// signature (when present) with t.Owner, the body's fields and its hub. With requireOwner a
// certificate without an owner signature is ErrUncertified (a machine checking a peer's
// certificate, peers.md 8.4).
func VerifyCert(d Doc, t Trust, requireOwner bool) (Cert, error) {
	raw, err := Verify(d, TypeDeviceCert, t.ring())
	if err != nil {
		return Cert{}, err
	}
	var b CertBody
	if err := decodeBody(raw, &b); err != nil {
		return Cert{}, err
	}
	if err := b.Check(); err != nil {
		return Cert{}, err
	}
	if b.Hub != t.HubFP() {
		return Cert{}, fmt.Errorf("%w: certificate of hub %s", ErrWrongHub, GroupFP(b.Hub))
	}
	c := Cert{Body: b, Raw: raw, OwnerSigned: d.Has(ByOwner), Doc: d}
	if requireOwner && !c.OwnerSigned {
		return Cert{}, ErrUncertified
	}
	return c, nil
}

// ---- device keys on the wire ----

// DeviceKeysNew is a device's public keys as it registers them (keys.md 7.1, api.yaml
// DeviceKeysNew).
type DeviceKeysNew struct {
	Sign KeySpec `json:"sign"`
	Box  KeySpec `json:"box"`
	Auth string  `json:"auth,omitempty"`
}

// CheckKeys validates keys for a device kind (keys.md 7.1 step 2): Ed25519 and X25519 keys
// of 32 bytes for machines and desktops, P-256 points on the curve plus a 16-byte auth
// secret for phones (withAuth: the auth secret must be there, as in a registration).
func CheckKeys(kind string, k DeviceKeysNew, withAuth bool) error {
	switch kind {
	case "machine", "desktop":
		if k.Sign.Alg != AlgEd25519 || k.Box.Alg != AlgX25519 {
			return fmt.Errorf("a %s's keys are ed25519 and x25519", kind)
		}
		if _, err := unB64Len(k.Sign.Pub, ed25519.PublicKeySize, "sign.pub"); err != nil {
			return err
		}
		b, err := unB64Len(k.Box.Pub, 32, "box.pub")
		if err != nil {
			return err
		}
		if _, err := ecdh.X25519().NewPublicKey(b); err != nil {
			return fmt.Errorf("box.pub: %v", err)
		}
		if k.Auth != "" {
			return fmt.Errorf("auth is for phones only")
		}
	case "phone":
		if k.Sign.Alg != AlgP256 || k.Box.Alg != AlgP256 {
			return fmt.Errorf("a phone's keys are p256 and p256")
		}
		s, err := unB64Len(k.Sign.Pub, 65, "sign.pub")
		if err != nil {
			return err
		}
		if _, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), s); err != nil {
			return fmt.Errorf("sign.pub: %v", err)
		}
		b, err := unB64Len(k.Box.Pub, 65, "box.pub")
		if err != nil {
			return err
		}
		if _, err := ecdh.P256().NewPublicKey(b); err != nil {
			return fmt.Errorf("box.pub: %v", err)
		}
		if withAuth {
			if _, err := unB64Len(k.Auth, 16, "auth"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("a %s device has no link keys", kind)
	}
	return nil
}

// NewCertBody builds the body the hub signs for a device's keys (keys.md 4.2 step 1).
func NewCertBody(device, kind, name, machine string, k DeviceKeysNew, reg, hubFP string, now time.Time) CertBody {
	c := CertBody{V: 1, Device: device, Kind: kind, Name: name, Sign: k.Sign, Box: k.Box, Reg: reg, Hub: hubFP,
		IssuedAt: FormatTime(now)}
	if kind != "phone" {
		c.Machine = &machine
	}
	return c
}

// ---- revocations ----

// RevokedDevice is one entry of a revocation list.
type RevokedDevice struct {
	Device string   `json:"device"`
	Keys   []string `json:"keys"` // fingerprints of its sign and box keys
	At     string   `json:"at"`
	Reason string   `json:"reason"`
}

// RevocationBody is the body of a revocations document (keys.md 6).
type RevocationBody struct {
	V        int             `json:"v"`
	Version  int             `json:"version"`
	Hub      string          `json:"hub"`
	IssuedAt string          `json:"issued_at"`
	Devices  []RevokedDevice `json:"devices"`
}

// RevokedKey reports whether a key fingerprint is listed.
func (r RevocationBody) RevokedKey(fp string) bool {
	for _, d := range r.Devices {
		if slices.Contains(d.Keys, fp) {
			return true
		}
	}
	return false
}

// RevokedDevice reports whether a device id is listed.
func (r RevocationBody) RevokedDevice(id string) bool {
	return slices.ContainsFunc(r.Devices, func(d RevokedDevice) bool { return d.Device == id })
}

// Signed is a verified matrix or revocations document and who signed it (ByOwner or ByHub).
type Signed[T any] struct {
	Body   T
	Raw    []byte
	Signer string
	Doc    Doc
}

// VerifyRevocations checks a revocations document: one signature, by the owner or the hub,
// and the body for this hub.
func VerifyRevocations(d Doc, t Trust) (Signed[RevocationBody], error) {
	raw, err := Verify(d, TypeRevocations, t.ring())
	if err != nil {
		return Signed[RevocationBody]{}, err
	}
	var b RevocationBody
	if err := decodeBody(raw, &b); err != nil {
		return Signed[RevocationBody]{}, err
	}
	if b.Version < 1 || !validTime(b.IssuedAt) {
		return Signed[RevocationBody]{}, fmt.Errorf("%w: revocations version or issued_at", ErrBody)
	}
	for _, e := range b.Devices {
		if e.Device == "" || !validTime(e.At) || slices.ContainsFunc(e.Keys, func(k string) bool { return !ValidFP(k) }) {
			return Signed[RevocationBody]{}, fmt.Errorf("%w: revocation entry %q", ErrBody, e.Device)
		}
	}
	if b.Hub != t.HubFP() {
		return Signed[RevocationBody]{}, fmt.Errorf("%w: revocations of hub %s", ErrWrongHub, GroupFP(b.Hub))
	}
	return Signed[RevocationBody]{Body: b, Raw: raw, Signer: d.SignedBy()[0], Doc: d}, nil
}

// NextRevocations decides whether a verifier holding cur (nil: none) takes next: a version
// not above the current one is ignored (newer false, no error: no replay of an older list),
// and a newer list must keep every entry of cur (ErrDroppedEntry otherwise).
func NextRevocations(cur *RevocationBody, next RevocationBody) (newer bool, err error) {
	if cur == nil {
		return true, nil
	}
	if next.Version <= cur.Version {
		return false, nil
	}
	for _, e := range cur.Devices {
		if !slices.ContainsFunc(next.Devices, func(n RevokedDevice) bool {
			return n.Device == e.Device && slices.Equal(n.Keys, e.Keys)
		}) {
			return false, fmt.Errorf("%w (%s)", ErrDroppedEntry, e.Device)
		}
	}
	return true, nil
}

// ---- peer_envelope ----

// VerifyPeerEnvelope checks a peer_envelope's one device signature with the sign key of the
// sender's verified certificate (peers.md 8.2) and returns the body bytes. The caller checks
// the certificate first (VerifyCert with requireOwner) and the body's fields after.
func VerifyPeerEnvelope(d Doc, sender Cert) ([]byte, error) {
	if sender.Body.Sign.Alg != AlgEd25519 {
		return nil, fmt.Errorf("%w: the sender's certificate has no ed25519 key", ErrSignature)
	}
	return Verify(d, TypePeerEnvelope, KeyRing{ByDevice: sender.Body.SignPub()})
}
