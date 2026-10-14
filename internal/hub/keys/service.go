package hubkeys

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
)

// PendingTTL is how long an owner-key proposal waits for confirmation (keys.md 3.3 step 4).
const PendingTTL = 24 * time.Hour

// Service is the hub's key authority over its store. Every stored document is checked with
// the hub key (and the active owner key) each time it is loaded: a row written or edited in
// the database without the hub key is ignored and logged (keys.md 4.1).
type Service struct {
	Store *store.Store
	Key   *HubKey
	// Log receives one line per ignored row; never a key or a signature. Nil discards.
	Log *log.Logger
}

// New returns a service over st signing with key.
func New(st *store.Store, key *HubKey) *Service { return &Service{Store: st, Key: key} }

// Error is a refusal with its HTTP status and api.yaml code.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func badRequest(format string, a ...any) error {
	return &Error{http.StatusBadRequest, "bad_request", fmt.Sprintf(format, a...)}
}
func conflict(format string, a ...any) error {
	return &Error{http.StatusConflict, "conflict", fmt.Sprintf(format, a...)}
}
func notFound(format string, a ...any) error {
	return &Error{http.StatusNotFound, "not_found", fmt.Sprintf(format, a...)}
}

// ErrBadCert means a device_key row does not verify with the hub key (edited in the
// database); the hub treats the device as having no keys.
var ErrBadCert = errors.New("device_key row does not match its certificate")

func (s *Service) logf(format string, a ...any) {
	if s.Log != nil {
		s.Log.Printf(format, a...)
	}
}

func (s *Service) now() time.Time { return s.Store.Now() }

// ---- owner key (keys.md 3.3) ----

// OwnerKeyInfo is a verified owner_key row (api.yaml OwnerKey).
type OwnerKeyInfo struct {
	FP, State, ProposedBy string
	Pub                   ed25519.PublicKey
	CreatedAt             int64
	ConfirmedAt           int64
	Doc                   keys.Doc
}

func (s *Service) ownerDoc(r store.OwnerKeyRow) keys.Doc {
	d := keys.Doc{Type: keys.TypeOwnerKey, Body: keys.B64(r.Body), Sigs: []keys.Sig{{By: keys.ByOwner, FP: r.FP, Sig: keys.B64(r.SelfSig)}}}
	if r.HubSig != nil {
		d.Sigs = append(d.Sigs, keys.Sig{By: keys.ByHub, FP: s.Key.FP, Sig: keys.B64(r.HubSig)})
	}
	return d
}

// checkOwnerRow re-verifies a row: its self-signature, the hub's confirmation for active and
// retired keys, and that the columns match the signed body.
func (s *Service) checkOwnerRow(r store.OwnerKeyRow) (OwnerKeyInfo, error) {
	ok, err := keys.VerifyOwnerKey(s.ownerDoc(r), s.Key.Pub)
	if err != nil {
		return OwnerKeyInfo{}, err
	}
	if ok.FP != r.FP || !bytes.Equal(ok.Pub, r.Pub) || ok.Confirmed != (r.State != "pending") {
		return OwnerKeyInfo{}, errors.New("columns do not match the signed document")
	}
	return OwnerKeyInfo{FP: r.FP, State: r.State, ProposedBy: r.ProposedBy, Pub: ok.Pub, CreatedAt: r.CreatedAt,
		ConfirmedAt: r.ConfirmedAt, Doc: ok.Doc}, nil
}

func (s *Service) verifiedOwners(rows []store.OwnerKeyRow) []OwnerKeyInfo {
	var out []OwnerKeyInfo
	for _, r := range rows {
		info, err := s.checkOwnerRow(r)
		if err != nil {
			s.logf("keys: owner_key row %s (%s) ignored: %v", r.FP, r.State, err)
			continue
		}
		out = append(out, info)
	}
	return out
}

// ActiveOwner is the active owner key, verified; nil when there is none (or the row does not
// verify, which is the same to everyone).
func (s *Service) ActiveOwner(ctx context.Context) (*OwnerKeyInfo, error) {
	rows, err := s.Store.OwnerKeys(ctx, "active")
	if err != nil {
		return nil, err
	}
	if v := s.verifiedOwners(rows); len(v) > 0 {
		return &v[0], nil
	}
	return nil, nil
}

func (s *Service) activeOwnerTx(tx *store.Tx) (*OwnerKeyInfo, error) {
	rows, err := tx.OwnerKeys("active")
	if err != nil {
		return nil, err
	}
	if v := s.verifiedOwners(rows); len(v) > 0 {
		return &v[0], nil
	}
	return nil, nil
}

// PendingOwners lists live proposals, verified, newest first.
func (s *Service) PendingOwners(ctx context.Context) ([]OwnerKeyInfo, error) {
	rows, err := s.Store.OwnerKeys(ctx, "pending")
	if err != nil {
		return nil, err
	}
	cut := s.now().Add(-PendingTTL).UnixMilli()
	rows = slices.DeleteFunc(rows, func(r store.OwnerKeyRow) bool { return r.CreatedAt < cut })
	return s.verifiedOwners(rows), nil
}

// Trust is the hub key plus the active owner key, if any.
func (s *Service) Trust(ctx context.Context) (keys.Trust, error) {
	t := s.Key.Trust()
	o, err := s.ActiveOwner(ctx)
	if err != nil {
		return t, err
	}
	if o != nil {
		t.Owner = o.Pub
	}
	return t, nil
}

func (s *Service) trustTx(tx *store.Tx) (keys.Trust, *OwnerKeyInfo, error) {
	t := s.Key.Trust()
	o, err := s.activeOwnerTx(tx)
	if err != nil {
		return t, nil, err
	}
	if o != nil {
		t.Owner = o.Pub
	}
	return t, o, nil
}

// Propose stores a self-signed owner_key document as a pending proposal (POST
// /v1/keys/owner). by is the device that sent it, or "cli". Another live proposal, or a key
// already known, is a conflict.
func (s *Service) Propose(ctx context.Context, doc keys.Doc, by string) (OwnerKeyInfo, error) {
	ok, err := keys.VerifyOwnerKey(doc, s.Key.Pub)
	if err != nil {
		return OwnerKeyInfo{}, badRequest("owner key document: %v", err)
	}
	if ok.Confirmed || len(doc.Sigs) != 1 {
		return OwnerKeyInfo{}, badRequest("a proposal carries only its own signature")
	}
	var out OwnerKeyInfo
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.NowMS()
		if _, err := tx.DeletePendingOwnerKeys(now - PendingTTL.Milliseconds()); err != nil {
			return err
		}
		if r, err := tx.OwnerKeyByFP(ok.FP); err == nil {
			return conflict("owner key %s is already %s", keys.GroupFP(ok.FP), r.State)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if p, err := tx.OwnerKeys("pending"); err != nil {
			return err
		} else if len(p) > 0 {
			return conflict("another owner key proposal (%s) is waiting for confirmation", keys.GroupFP(p[0].FP))
		}
		body, _ := doc.BodyBytes()
		self, _ := keys.UnB64(doc.Sigs[0].Sig)
		row := store.OwnerKeyRow{FP: ok.FP, Pub: ok.Pub, Body: body, SelfSig: self, State: "pending", ProposedBy: by, CreatedAt: now}
		if err := tx.InsertOwnerKey(row); err != nil {
			return err
		}
		out = OwnerKeyInfo{FP: ok.FP, State: "pending", ProposedBy: by, Pub: ok.Pub, CreatedAt: now, Doc: doc}
		return nil
	})
	return out, err
}

// Confirm activates a pending proposal (an owner session, or `sy hub keys confirm-owner`):
// the hub re-checks the self-signature, adds its own over the same bytes, and retires the
// key that was active (keys.md 3.3 steps 2, 3 and 5).
func (s *Service) Confirm(ctx context.Context, fp, by string) (OwnerKeyInfo, error) {
	var out OwnerKeyInfo
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.NowMS()
		if _, err := tx.DeletePendingOwnerKeys(now - PendingTTL.Milliseconds()); err != nil {
			return err
		}
		r, err := tx.OwnerKeyByFP(fp)
		if errors.Is(err, store.ErrNotFound) {
			return notFound("no owner key proposal %s (proposals expire after 24 hours)", keys.GroupFP(fp))
		}
		if err != nil {
			return err
		}
		if r.State != "pending" {
			return conflict("owner key %s is %s, not pending", keys.GroupFP(fp), r.State)
		}
		if _, err := s.checkOwnerRow(r); err != nil {
			s.logf("keys: owner_key proposal %s ignored: %v", fp, err)
			return conflict("the proposal %s does not verify; propose it again", keys.GroupFP(fp))
		}
		sig, _ := keys.UnB64(keys.SignDoc(keys.TypeOwnerKey, r.Body, keys.ByHub, s.Key.priv).Sig)
		if err := tx.ActivateOwnerKey(fp, sig, by, now); err != nil {
			return err
		}
		r.HubSig, r.State, r.ConfirmedAt, r.ConfirmedBy = sig, "active", now, by
		out, err = s.checkOwnerRow(r)
		return err
	})
	return out, err
}

// ProposeAndConfirm is merged mode (keys.md 3.3 step 2, third case): `sy keys init` on the
// hub's own machine and state folder makes the key active at once, confirmed by "cli".
func (s *Service) ProposeAndConfirm(ctx context.Context, doc keys.Doc) (OwnerKeyInfo, error) {
	p, err := s.Propose(ctx, doc, "cli")
	if err != nil {
		return OwnerKeyInfo{}, err
	}
	return s.Confirm(ctx, p.FP, "cli")
}

// ---- device certificates (keys.md 4.2) ----

// RegisterTx makes and stores the hub-signed certificate of a device's keys in tx (the
// pairing claim's transaction, keys.md 7.1 step 3), and for a phone its link push target.
// The keys must already be checked for the device's kind (keys.CheckKeys).
func (s *Service) RegisterTx(tx *store.Tx, d store.Device, k keys.DeviceKeysNew, reg string) (keys.Doc, error) {
	if err := keys.CheckKeys(d.Kind, k, d.Kind == "phone"); err != nil {
		return keys.Doc{}, badRequest("key registration invalid: %v", err)
	}
	now := time.UnixMilli(tx.NowMS())
	body := keys.NewCertBody(d.ID, d.Kind, d.Name, d.MachineID, keys.DeviceKeysNew{Sign: k.Sign, Box: k.Box}, reg, s.Key.FP, now)
	raw, err := json.Marshal(body)
	if err != nil {
		return keys.Doc{}, err
	}
	doc := keys.NewDoc(keys.TypeDeviceCert, raw)
	if err := s.Key.Sign(&doc); err != nil {
		return keys.Doc{}, err
	}
	sig, _ := keys.UnB64(doc.Sigs[0].Sig)
	if err := tx.PutDeviceKey(store.DeviceKeyRow{DeviceID: d.ID, SignAlg: k.Sign.Alg, SignPub: body.SignPub(), BoxAlg: k.Box.Alg,
		BoxPub: body.BoxPub(), Reg: reg, Cert: raw, HubSig: sig, CreatedAt: tx.NowMS()}); err != nil {
		return keys.Doc{}, err
	}
	if d.Kind == "phone" {
		tk, _ := json.Marshal(map[string]string{"p256dh": k.Box.Pub, "auth": k.Auth})
		name := d.Name
		if name == "" {
			name = "phone · app"
		}
		if err := tx.UpsertLinkTarget(store.NewID("pt_", now), d.ID, name, tk, nil, tx.NowMS()); err != nil {
			return keys.Doc{}, err
		}
	}
	return doc, nil
}

// certFromRow re-verifies a device_key row: the hub signature over cert, the body's device,
// and the owner signature when it is by the active owner key (an owner signature that does
// not verify is dropped and logged, so the device counts as uncertified).
func (s *Service) certFromRow(r store.DeviceKeyRow, owner *OwnerKeyInfo) (keys.Cert, error) {
	d := keys.Doc{Type: keys.TypeDeviceCert, Body: keys.B64(r.Cert), Sigs: []keys.Sig{{By: keys.ByHub, FP: s.Key.FP, Sig: keys.B64(r.HubSig)}}}
	c, err := keys.VerifyCert(d, s.Key.Trust(), false)
	if err == nil && c.Body.Device != r.DeviceID {
		err = errors.New("certificate is for another device")
	}
	if err != nil {
		s.logf("keys: device_key row for %s does not match its certificate: %v", r.DeviceID, err)
		return keys.Cert{}, ErrBadCert
	}
	if r.OwnerSig == nil {
		return c, nil
	}
	if owner == nil || r.OwnerFP != owner.FP {
		return c, nil // certified by a key that is no longer the owner's
	}
	full := d
	full.Sigs = append(full.Sigs, keys.Sig{By: keys.ByOwner, FP: r.OwnerFP, Sig: keys.B64(r.OwnerSig)})
	c2, err := keys.VerifyCert(full, keys.Trust{Hub: s.Key.Pub, Owner: owner.Pub}, false)
	if err != nil {
		s.logf("keys: owner signature on %s's certificate ignored: %v", r.DeviceID, err)
		return c, nil
	}
	return c2, nil
}

// Cert is a device's verified certificate (ErrNotFound when it has no keys, ErrBadCert when
// its row was edited). The link login and peer relay use this, never the key columns.
func (s *Service) Cert(ctx context.Context, deviceID string) (keys.Cert, error) {
	r, err := s.Store.DeviceKey(ctx, deviceID)
	if err != nil {
		return keys.Cert{}, err
	}
	owner, err := s.ActiveOwner(ctx)
	if err != nil {
		return keys.Cert{}, err
	}
	return s.certFromRow(r, owner)
}

// DeviceKeyInfo is what the Devices list shows of a device's keys (api.yaml DeviceKeyInfo).
type DeviceKeyInfo struct {
	SignFP, BoxFP, Reg string
	OwnerSigned        bool
	CertifiedAt        int64
}

// Certs returns every device's verified certificate summary, by device id. Rows that do not
// verify are left out.
func (s *Service) Certs(ctx context.Context) (map[string]DeviceKeyInfo, error) {
	rows, err := s.Store.DeviceKeys(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := s.ActiveOwner(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]DeviceKeyInfo{}
	for _, r := range rows {
		c, err := s.certFromRow(r, owner)
		if err != nil {
			continue
		}
		i := DeviceKeyInfo{SignFP: c.Body.SignFP(), BoxFP: c.Body.BoxFP(), Reg: c.Body.Reg, OwnerSigned: c.OwnerSigned}
		if c.OwnerSigned {
			i.CertifiedAt = r.Certified
		}
		out[r.DeviceID] = i
	}
	return out, nil
}

// RegisterKeys is PUT /v1/devices/{id}/keys: keys for a device paired before F1, or new
// keys for a `tofu` device without an owner signature. A device registered under the QR
// seal or SSH, or owner-certified, is refused with 409 ("re-pair this device"): a stolen
// bearer must not downgrade it to keys of its own. Replaced keys go into the next
// revocation list.
func (s *Service) RegisterKeys(ctx context.Context, deviceID string, k keys.DeviceKeysNew) (keys.Doc, error) {
	var doc keys.Doc
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		d, err := tx.DeviceDetailByID(deviceID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && d.RevokedAt != 0) {
			return notFound("no such device")
		}
		if err != nil {
			return err
		}
		if err := keys.CheckKeys(d.Kind, k, d.Kind == "phone"); err != nil {
			return badRequest("key registration invalid: %v", err)
		}
		old, err := tx.DeviceKey(deviceID)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return err
		default:
			if old.Reg != keys.RegTOFU || old.OwnerSig != nil {
				return conflict("this device's keys were verified (%s%s); re-pair this device to change them", old.Reg,
					map[bool]string{true: ", owner-certified", false: ""}[old.OwnerSig != nil])
			}
			_, owner, err := s.trustTx(tx)
			if err != nil {
				return err
			}
			if err := s.appendRevocationTx(tx, []keys.RevokedDevice{s.entryFor(old, owner, "replaced", tx.NowMS())}, deviceID); err != nil {
				return err
			}
		}
		doc, err = s.RegisterTx(tx, d.Device, k, keys.RegTOFU)
		return err
	})
	return doc, err
}

// Certify adds the owner signature to a device's certificate (POST /v1/devices/{id}/cert):
// it must be by the active owner key and verify over the stored body.
func (s *Service) Certify(ctx context.Context, deviceID, fp, sigB64 string) error {
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		_, owner, err := s.trustTx(tx)
		if err != nil {
			return err
		}
		if owner == nil {
			return conflict("this hub has no active owner key (sy keys init, then confirm it)")
		}
		if fp != owner.FP {
			return badRequest("signed by owner key %s, but the active owner key is %s", keys.GroupFP(fp), keys.GroupFP(owner.FP))
		}
		r, err := tx.DeviceKey(deviceID)
		if errors.Is(err, store.ErrNotFound) {
			return notFound("that device has no keys")
		}
		if err != nil {
			return err
		}
		if _, err := s.certFromRow(r, nil); err != nil {
			return conflict("that device's certificate does not verify; re-pair it")
		}
		sig, err := keys.UnB64(sigB64)
		if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(owner.Pub, keys.DocInput(keys.TypeDeviceCert, r.Cert), sig) {
			return badRequest("the owner signature does not verify over this device's certificate")
		}
		return tx.SetDeviceOwnerSig(deviceID, fp, sig, tx.NowMS())
	})
}

// ---- revocations (keys.md 6) ----

func revocationDoc(r store.SignedDocRow) keys.Doc {
	return keys.Doc{Type: keys.TypeRevocations, Body: keys.B64(r.Body), Sigs: []keys.Sig{{By: r.Signer, FP: r.KeyFP, Sig: keys.B64(r.Sig)}}}
}

// newestRevocations is the newest stored list that verifies; rows that do not are logged
// and skipped.
func (s *Service) newestRevocations(rows []store.SignedDocRow, t keys.Trust) *keys.Signed[keys.RevocationBody] {
	for _, r := range rows {
		sd, err := keys.VerifyRevocations(revocationDoc(r), t)
		if err == nil && sd.Body.Version != r.Version {
			err = errors.New("version column differs from the body")
		}
		if err != nil {
			s.logf("keys: revocation_doc version %d ignored: %v", r.Version, err)
			continue
		}
		return &sd
	}
	return nil
}

// Revocations is the newest verified revocation list; nil when there is none.
func (s *Service) Revocations(ctx context.Context) (*keys.Signed[keys.RevocationBody], error) {
	t, err := s.Trust(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.SignedDocs(ctx, false, 0)
	if err != nil {
		return nil, err
	}
	return s.newestRevocations(rows, t), nil
}

// entryFor lists a device's keys for a revocation: from its certificate when the row
// verifies, else from the key columns (listing keys too many only narrows).
func (s *Service) entryFor(r store.DeviceKeyRow, owner *OwnerKeyInfo, reason string, now int64) keys.RevokedDevice {
	fps := []string{keys.Fingerprint(r.SignPub), keys.Fingerprint(r.BoxPub)}
	if c, err := s.certFromRow(r, owner); err == nil {
		fps = []string{c.Body.SignFP(), c.Body.BoxFP()}
	}
	return keys.RevokedDevice{Device: r.DeviceID, Keys: fps, At: keys.FormatTime(time.UnixMilli(now)), Reason: reason}
}

// appendRevocationTx signs and stores the next revocation list: the newest verified one plus
// entries not already in it. No new version when nothing is new.
func (s *Service) appendRevocationTx(tx *store.Tx, entries []keys.RevokedDevice, madeBy string) error {
	t, _, err := s.trustTx(tx)
	if err != nil {
		return err
	}
	rows, err := tx.SignedDocs(false, 0)
	if err != nil {
		return err
	}
	cur := s.newestRevocations(rows, t)
	body := keys.RevocationBody{V: 1, Hub: s.Key.FP, IssuedAt: keys.FormatTime(time.UnixMilli(tx.NowMS())), Devices: []keys.RevokedDevice{}}
	if cur != nil {
		body.Devices = append(body.Devices, cur.Body.Devices...)
	}
	added := false
	for _, e := range entries {
		if !slices.ContainsFunc(body.Devices, func(d keys.RevokedDevice) bool { return d.Device == e.Device && slices.Equal(d.Keys, e.Keys) }) {
			body.Devices = append(body.Devices, e)
			added = true
		}
	}
	if !added {
		return nil
	}
	maxV, err := tx.MaxSignedDocVersion(false)
	if err != nil {
		return err
	}
	body.Version = maxV + 1
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	doc := keys.NewDoc(keys.TypeRevocations, raw)
	if err := s.Key.Sign(&doc); err != nil {
		return err
	}
	sig, _ := keys.UnB64(doc.Sigs[0].Sig)
	return tx.InsertSignedDoc(false, store.SignedDocRow{Version: body.Version, Body: raw, Signer: keys.ByHub, KeyFP: s.Key.FP, Sig: sig,
		MadeBy: madeBy, CreatedAt: tx.NowMS()})
}

// RevokeDevice revokes a device as auth.Revoke does (DELETE /v1/devices/{id}, `sy hub token
// revoke`), drops its link queue and state, and, when it had keys, signs the next revocation
// list with the hub key. madeBy is the device that asked, or "cli". store.ErrNotFound when
// there was nothing to revoke.
func (s *Service) RevokeDevice(ctx context.Context, id, madeBy, reason string) error {
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		r, kerr := tx.DeviceKey(id)
		if kerr != nil && !errors.Is(kerr, store.ErrNotFound) {
			return kerr
		}
		cascade := tx.RevokeDeviceCascade(id)
		if cascade != nil && !errors.Is(cascade, store.ErrNotFound) {
			return cascade
		}
		if err := tx.DropLink(id); err != nil {
			return err
		}
		if kerr == nil {
			_, owner, err := s.trustTx(tx)
			if err != nil {
				return err
			}
			if err := s.appendRevocationTx(tx, []keys.RevokedDevice{s.entryFor(r, owner, reason, tx.NowMS())}, madeBy); err != nil {
				return err
			}
		}
		return cascade
	})
}

// AcceptOwnerRevocations takes an owner-signed revocation list (POST /v1/keys/revocations):
// it must verify with the active owner key, be newer than the hub's and keep every entry.
// The hub then revokes every device the list adds. It returns their ids.
func (s *Service) AcceptOwnerRevocations(ctx context.Context, doc keys.Doc, madeBy string) ([]string, error) {
	var revoked []string
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		t, owner, err := s.trustTx(tx)
		if err != nil {
			return err
		}
		if owner == nil {
			return conflict("this hub has no active owner key")
		}
		next, err := keys.VerifyRevocations(doc, t)
		if err != nil {
			return badRequest("revocation document: %v", err)
		}
		if next.Signer != keys.ByOwner {
			return badRequest("upload an owner-signed revocation document")
		}
		rows, err := tx.SignedDocs(false, 0)
		if err != nil {
			return err
		}
		cur := s.newestRevocations(rows, t)
		var curBody *keys.RevocationBody
		if cur != nil {
			curBody = &cur.Body
		}
		newer, err := keys.NextRevocations(curBody, next.Body)
		if err != nil {
			return conflict("%v", err)
		}
		maxV, err := tx.MaxSignedDocVersion(false)
		if err != nil {
			return err
		}
		if !newer || next.Body.Version <= maxV {
			return conflict("the hub already has revocation list version %d; build on it", maxV)
		}
		sig, _ := keys.UnB64(doc.Sigs[0].Sig)
		if err := tx.InsertSignedDoc(false, store.SignedDocRow{Version: next.Body.Version, Body: next.Raw, Signer: keys.ByOwner,
			KeyFP: owner.FP, Sig: sig, MadeBy: madeBy, CreatedAt: tx.NowMS()}); err != nil {
			return err
		}
		for _, e := range next.Body.Devices {
			if curBody != nil && curBody.RevokedDevice(e.Device) {
				continue
			}
			if err := tx.RevokeDeviceCascade(e.Device); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err := tx.DropLink(e.Device); err != nil {
				return err
			}
			revoked = append(revoked, e.Device)
		}
		return nil
	})
	return revoked, err
}

// ---- matrix (keys.md 5; stored and evaluated by the peers packages) ----

// SignMatrix signs a matrix body with the hub key (a change the hub may sign under
// keys.HubMaySignMatrix). Storing it (matrix_doc) is the caller's.
func (s *Service) SignMatrix(body keys.MatrixBody) (keys.Doc, error) {
	if body.Hub != s.Key.FP {
		return keys.Doc{}, keys.ErrWrongHub
	}
	if err := body.Check(); err != nil {
		return keys.Doc{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return keys.Doc{}, err
	}
	d := keys.NewDoc(keys.TypeMatrix, raw)
	return d, s.Key.Sign(&d)
}

// Matrix is the newest stored matrix document that verifies (owner- or hub-signed); nil
// when there is none. Rows that do not verify are logged and skipped.
func (s *Service) Matrix(ctx context.Context) (*keys.Signed[keys.MatrixBody], error) {
	t, err := s.Trust(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.SignedDocs(ctx, true, 0)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		d := keys.Doc{Type: keys.TypeMatrix, Body: keys.B64(r.Body), Sigs: []keys.Sig{{By: r.Signer, FP: r.KeyFP, Sig: keys.B64(r.Sig)}}}
		sd, err := keys.VerifyMatrix(d, t)
		if err == nil && sd.Body.Version != r.Version {
			err = errors.New("version column differs from the body")
		}
		if err != nil {
			s.logf("keys: matrix_doc version %d ignored: %v", r.Version, err)
			continue
		}
		return &sd, nil
	}
	return nil, nil
}

// VerifyStored re-checks every stored document (at hub start) and returns how many rows
// were ignored; each is logged.
func (s *Service) VerifyStored(ctx context.Context) (int, error) {
	bad := 0
	all, err := s.Store.OwnerKeys(ctx, "")
	if err != nil {
		return 0, err
	}
	bad += len(all) - len(s.verifiedOwners(all))
	owner, err := s.ActiveOwner(ctx)
	if err != nil {
		return bad, err
	}
	dks, err := s.Store.DeviceKeys(ctx)
	if err != nil {
		return bad, err
	}
	for _, r := range dks {
		if _, err := s.certFromRow(r, owner); err != nil {
			bad++
		}
	}
	t, _ := s.Trust(ctx)
	for _, matrix := range []bool{false, true} {
		rows, err := s.Store.SignedDocs(ctx, matrix, 0)
		if err != nil {
			return bad, err
		}
		for _, r := range rows {
			d := keys.Doc{Body: keys.B64(r.Body), Sigs: []keys.Sig{{By: r.Signer, FP: r.KeyFP, Sig: keys.B64(r.Sig)}}}
			var err error
			if matrix {
				d.Type = keys.TypeMatrix
				_, err = keys.VerifyMatrix(d, t)
			} else {
				d.Type = keys.TypeRevocations
				_, err = keys.VerifyRevocations(d, t)
			}
			if err != nil {
				bad++
				s.logf("keys: %s version %d does not verify: %v", map[bool]string{true: "matrix_doc", false: "revocation_doc"}[matrix], r.Version, err)
			}
		}
	}
	return bad, nil
}
