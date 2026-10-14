package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mraj602/switchyard/internal/keys"
)

// ErrTampered means a claim reply did not hold up (keys.md 7.3): the device must forget the
// new token and pair again.
var ErrTampered = errors.New("pairing was tampered with; try again on your home network")

// ClaimedKeys is what a verified claim reply gave a device: the hub key and, when one is
// active with both signatures, the owner key.
type ClaimedKeys struct {
	Hub   ed25519.PublicKey
	HubFP string
	Owner *keys.OwnerKey
	Cert  keys.Cert
}

// VerifyClaim checks a claim reply against the keys the device sent (keys.md 7.3): the
// certificate's hub signature with hub_key (and hub_key.fp against qrFP when the QR had
// one), that the certificate is for this device and holds its own public keys, and wantReg.
// Any mismatch is ErrTampered (wrapped with the reason).
func VerifyClaim(rep *ClaimReply, sent keys.DeviceKeysNew, wantReg, qrFP string) (ClaimedKeys, error) {
	bad := func(why string, a ...any) (ClaimedKeys, error) {
		return ClaimedKeys{}, fmt.Errorf("%w (%s)", ErrTampered, fmt.Sprintf(why, a...))
	}
	if rep.HubKey == nil || rep.Cert == nil {
		return bad("the reply has no hub key or certificate")
	}
	if rep.HubKey.Alg != keys.AlgEd25519 {
		return bad("hub key algorithm %q", rep.HubKey.Alg)
	}
	pub, err := keys.UnB64(rep.HubKey.Pub)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return bad("hub key is not an Ed25519 key")
	}
	hub := ed25519.PublicKey(pub)
	fp := keys.Fingerprint(hub)
	if fp != rep.HubKey.FP {
		return bad("hub key fingerprint does not match the key")
	}
	if qrFP != "" && qrFP != fp {
		return bad("this hub's key does not match the QR code")
	}
	c, err := keys.VerifyCert(*rep.Cert, keys.Trust{Hub: hub}, false)
	if err != nil {
		return bad("certificate: %v", err)
	}
	if c.Body.Device != rep.Device || c.Body.Sign.Pub != sent.Sign.Pub || c.Body.Box.Pub != sent.Box.Pub ||
		c.Body.Sign.Alg != sent.Sign.Alg || c.Body.Box.Alg != sent.Box.Alg {
		return bad("the certificate does not hold this device's keys")
	}
	if wantReg != "" && (rep.Reg != wantReg || c.Body.Reg != wantReg) {
		return bad("registration %q, want %q", rep.Reg, wantReg)
	}
	out := ClaimedKeys{Hub: hub, HubFP: fp, Cert: c}
	if rep.OwnerKey != nil {
		// Only a key with the owner's self-signature and the hub's confirmation; anything
		// else is ignored (not pinned), not a tampering sign.
		if o, err := keys.VerifyOwnerKey(*rep.OwnerKey, hub); err == nil && o.Confirmed {
			out.Owner = &o
		}
	}
	return out, nil
}

// PinClaimed pins the hub key (how, e.g. keys.PinTOFU for a typed code) and, when the reply
// carried a confirmed owner key, the owner key (keys.PinTOFU), in stateDir/keys (keys.md
// 3.5). A different hub key already pinned is keys.ErrPinned and nothing changes. A
// different owner pin is kept; ownerKept then reports it.
func PinClaimed(stateDir string, ck ClaimedKeys, how string, now time.Time) (ownerKept bool, err error) {
	if err := keys.SetPin(keys.Path(stateDir, keys.HubPinFile), keys.NewPin(ck.Hub, how, now)); err != nil {
		return false, err
	}
	if ck.Owner == nil {
		return false, nil
	}
	err = keys.SetPin(keys.Path(stateDir, keys.OwnerPinFile), keys.NewPin(ck.Owner.Pub, keys.PinTOFU, now))
	if errors.Is(err, keys.ErrPinned) {
		return true, nil
	}
	return false, err
}

// DeleteSelf is DELETE /v1/devices/self: a device gives up its own token (after a claim
// reply that did not verify, keys.md 7.3).
func (c *Client) DeleteSelf(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint("/v1/devices/self"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return statusError(resp, b)
	}
	return nil
}

// ---- the key endpoints (api.yaml tag keys) ----

// KeysInfo is GET /v1/keys.
type KeysInfo struct {
	HubKey             PublicKey      `json:"hub_key"`
	OwnerKey           *OwnerKeyInfo  `json:"owner_key"`
	Pending            []OwnerKeyInfo `json:"pending"`
	RevocationsVersion int            `json:"revocations_version"`
	Devices            []KeyDevice    `json:"devices"`
}

// OwnerKeyInfo is api.yaml OwnerKey.
type OwnerKeyInfo struct {
	FP         string   `json:"fp"`
	Pub        string   `json:"pub"`
	State      string   `json:"state"`
	ProposedBy string   `json:"proposed_by"`
	CreatedAt  string   `json:"created_at"`
	Doc        keys.Doc `json:"doc"`
}

// KeyDevice is one entry of KeysInfo.Devices.
type KeyDevice struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Keys struct {
		SignFP      string `json:"sign_fp"`
		BoxFP       string `json:"box_fp"`
		Reg         string `json:"reg"`
		OwnerSigned bool   `json:"owner_signed"`
	} `json:"keys"`
}

// doJSON sends in (nil: no body) with the bearer token and decodes a 2xx reply into out
// (nil: ignored). Anything else is *StatusError.
func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return statusError(resp, b)
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// Keys is GET /v1/keys.
func (c *Client) Keys(ctx context.Context) (KeysInfo, error) {
	var k KeysInfo
	return k, c.doJSON(ctx, http.MethodGet, "/v1/keys", nil, &k)
}

// ProposeOwner is POST /v1/keys/owner.
func (c *Client) ProposeOwner(ctx context.Context, doc keys.Doc) (OwnerKeyInfo, error) {
	var o OwnerKeyInfo
	return o, c.doJSON(ctx, http.MethodPost, "/v1/keys/owner", map[string]any{"doc": doc}, &o)
}

// DeviceCert is GET /v1/devices/{id}/cert.
func (c *Client) DeviceCert(ctx context.Context, id string) (keys.Doc, bool, error) {
	var r struct {
		Cert        keys.Doc `json:"cert"`
		OwnerSigned bool     `json:"owner_signed"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/v1/devices/"+url.PathEscape(id)+"/cert", nil, &r)
	return r.Cert, r.OwnerSigned, err
}

// Certify is POST /v1/devices/{id}/cert.
func (c *Client) Certify(ctx context.Context, id, fp, sig string) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/devices/"+url.PathEscape(id)+"/cert", map[string]string{"fp": fp, "sig": sig}, nil)
}

// Revocations is GET /v1/keys/revocations; nil when nothing was revoked.
func (c *Client) Revocations(ctx context.Context) (*keys.Doc, error) {
	var r struct {
		Doc *keys.Doc `json:"doc"`
	}
	return r.Doc, c.doJSON(ctx, http.MethodGet, "/v1/keys/revocations", nil, &r)
}

// UploadRevocations is POST /v1/keys/revocations; it returns the devices the hub revoked.
func (c *Client) UploadRevocations(ctx context.Context, doc keys.Doc) ([]string, error) {
	var r struct {
		Revoked []string `json:"revoked"`
	}
	return r.Revoked, c.doJSON(ctx, http.MethodPost, "/v1/keys/revocations", map[string]any{"doc": doc}, &r)
}

// PutKeys is PUT /v1/devices/self/keys; it returns the hub-signed certificate.
func (c *Client) PutKeys(ctx context.Context, k keys.DeviceKeysNew) (keys.Doc, error) {
	var r struct {
		Cert keys.Doc `json:"cert"`
	}
	return r.Cert, c.doJSON(ctx, http.MethodPut, "/v1/devices/self/keys", k, &r)
}
