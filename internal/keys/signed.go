package keys

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
)

// Document types (keys.md 4.1).
const (
	TypeOwnerKey     = "owner_key"
	TypeDeviceCert   = "device_cert"
	TypeMatrix       = "matrix"
	TypeRevocations  = "revocations"
	TypePeerEnvelope = "peer_envelope"
)

// Signers (the `by` of a signature).
const (
	ByHub    = "hub"
	ByOwner  = "owner"
	ByDevice = "device"
)

// signerSets lists, per type, every set of signers a verifier accepts, each in the order a
// document carries them. Anything else (a missing, an extra or a repeated signer) is a
// reject (keys.md 4.1).
var signerSets = map[string][][]string{
	TypeOwnerKey:     {{ByOwner}, {ByOwner, ByHub}},
	TypeDeviceCert:   {{ByHub}, {ByHub, ByOwner}},
	TypeMatrix:       {{ByOwner}, {ByHub}},
	TypeRevocations:  {{ByOwner}, {ByHub}},
	TypePeerEnvelope: {{ByDevice}},
}

// Errors of document checks. Each wraps one of these, so callers can map them to a reason
// word with errors.Is.
var (
	ErrDocType      = errors.New("unexpected document type")
	ErrSigners      = errors.New("wrong set of signatures")
	ErrSignature    = errors.New("bad signature")
	ErrBody         = errors.New("malformed document body")
	ErrWrongHub     = errors.New("document is for another hub")
	ErrUncertified  = errors.New("certificate has no owner signature")
	ErrHubWidening  = errors.New("a hub-signed document may not widen the matrix here")
	ErrDroppedEntry = errors.New("revocation list drops an entry of the previous version")
	ErrVersion      = errors.New("unexpected document version")
)

// docPrefix keeps documents apart from link frames, which start "sy-link-v1" (link.md 4.3).
const docPrefix = "sy-doc-v1\n"

// Sig is one signature of a document.
type Sig struct {
	By  string `json:"by"`
	FP  string `json:"fp"`
	Sig string `json:"sig"`
}

// Doc is the wire form of every signed object (keys.md 4.1): the body bytes as signed, in
// base64url, and its signatures.
type Doc struct {
	Type string `json:"type"`
	Body string `json:"body"`
	Sigs []Sig  `json:"sigs"`
}

// DocInput is what a document signature covers: "sy-doc-v1\n" ‖ type ‖ "\n" ‖ body.
func DocInput(typ string, body []byte) []byte {
	b := make([]byte, 0, len(docPrefix)+len(typ)+1+len(body))
	b = append(b, docPrefix...)
	b = append(b, typ...)
	b = append(b, '\n')
	return append(b, body...)
}

// NewDoc wraps body bytes in an unsigned document of type typ.
func NewDoc(typ string, body []byte) Doc {
	return Doc{Type: typ, Body: B64(body), Sigs: []Sig{}}
}

// SignDoc returns the signature priv makes over body as type typ, by signer by.
func SignDoc(typ string, body []byte, by string, priv ed25519.PrivateKey) Sig {
	pub := priv.Public().(ed25519.PublicKey)
	return Sig{By: by, FP: Fingerprint(pub), Sig: B64(ed25519.Sign(priv, DocInput(typ, body)))}
}

// Sign adds a signature by `by` made with priv. A signer that already signed is refused.
func (d *Doc) Sign(by string, priv ed25519.PrivateKey) error {
	if _, ok := signerSets[d.Type]; !ok {
		return fmt.Errorf("%w %q", ErrDocType, d.Type)
	}
	if d.Has(by) {
		return fmt.Errorf("%w: %s already signed", ErrSigners, by)
	}
	body, err := d.BodyBytes()
	if err != nil {
		return err
	}
	d.Sigs = append(d.Sigs, SignDoc(d.Type, body, by, priv))
	return nil
}

// BodyBytes decodes the body.
func (d Doc) BodyBytes() ([]byte, error) {
	b, err := UnB64(d.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: body is not base64url", ErrBody)
	}
	return b, nil
}

// Has reports whether the document carries a signature by `by` (unchecked).
func (d Doc) Has(by string) bool {
	_, ok := d.SigOf(by)
	return ok
}

// SigOf returns the signature by `by` (unchecked).
func (d Doc) SigOf(by string) (Sig, bool) {
	for _, s := range d.Sigs {
		if s.By == by {
			return s, true
		}
	}
	return Sig{}, false
}

// KeyRing maps each signer to the key its signature must verify with: the verifier's pins
// for hub and owner, the sender's certificate key for device.
type KeyRing map[string]ed25519.PublicKey

// Verify checks a document of the expected type typ: its signers are exactly one of the
// sets keys.md 4.1 names for the type (no missing, extra or repeated signer), and every
// signature verifies with ring[by]. It returns the body bytes, which the caller decodes only
// now. A signer whose key is not in ring is a reject: nothing is ignored.
func Verify(d Doc, typ string, ring KeyRing) ([]byte, error) {
	sets, ok := signerSets[typ]
	if !ok || d.Type != typ {
		return nil, fmt.Errorf("%w: %q, want %q", ErrDocType, d.Type, typ)
	}
	var by []string
	for _, s := range d.Sigs {
		by = append(by, s.By)
	}
	if !slices.ContainsFunc(sets, func(set []string) bool { return sameSet(set, by) }) {
		return nil, fmt.Errorf("%w for %s: %v", ErrSigners, typ, by)
	}
	body, err := d.BodyBytes()
	if err != nil {
		return nil, err
	}
	in := DocInput(typ, body)
	for _, s := range d.Sigs {
		pub := ring[s.By]
		if len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: no %s key to check the %s signature with", ErrSignature, s.By, typ)
		}
		sig, err := UnB64(s.Sig)
		if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, in, sig) {
			return nil, fmt.Errorf("%w: %s signature on %s (signed by key %s, checked with %s)", ErrSignature, s.By, typ,
				GroupFP(s.FP), GroupFP(Fingerprint(pub)))
		}
	}
	return body, nil
}

// sameSet reports whether got holds exactly the signers of want, each once.
func sameSet(want, got []string) bool {
	if len(want) != len(got) {
		return false
	}
	for _, w := range want {
		if slices.Index(got, w) < 0 {
			return false
		}
	}
	for i, g := range got {
		if slices.Index(got[i+1:], g) >= 0 {
			return false
		}
	}
	return true
}

// SignedBy returns the signers of a verified document, in its order.
func (d Doc) SignedBy() []string {
	var out []string
	for _, s := range d.Sigs {
		out = append(out, s.By)
	}
	return out
}
