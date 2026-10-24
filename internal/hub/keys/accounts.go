package hubkeys

import (
	"context"
	"errors"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
)

// Machine accounts (keys.md 4.3): the owner-signed list of accounts whose sessions a
// machine's device may speak for in peer envelopes. The peer relay and every receiving agent
// refuse an envelope whose sender account is not on it.

func accountsDoc(r store.DeviceAccountsRow) keys.Doc {
	return keys.Doc{Type: keys.TypeMachineAccounts, Body: keys.B64(r.Body),
		Sigs: []keys.Sig{{By: keys.ByOwner, FP: r.OwnerFP, Sig: keys.B64(r.OwnerSig)}}}
}

// SetAccounts stores an owner-signed machine_accounts document for a device (POST
// /v1/devices/{id}/accounts). It must be signed by the active owner key and name the
// device's current certificate (its device, machine and sign key).
func (s *Service) SetAccounts(ctx context.Context, deviceID string, doc keys.Doc) (keys.AccountsBody, error) {
	var out keys.AccountsBody
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		t, owner, err := s.trustTx(tx)
		if err != nil {
			return err
		}
		if owner == nil {
			return conflict("this hub has no active owner key (wy keys init, then confirm it)")
		}
		r, err := tx.DeviceKey(deviceID)
		if errors.Is(err, store.ErrNotFound) {
			return notFound("that device has no keys")
		}
		if err != nil {
			return err
		}
		c, err := s.certFromRow(r, nil)
		if err != nil {
			return conflict("that device's certificate does not verify; re-pair it")
		}
		if c.Body.Kind != "machine" && c.Body.Kind != "desktop" {
			return badRequest("only a machine or desktop speaks for accounts")
		}
		b, err := keys.VerifyAccounts(doc, t, c)
		if err != nil {
			return badRequest("machine_accounts: %v", err)
		}
		raw, _ := doc.BodyBytes()
		sig, _ := doc.SigOf(keys.ByOwner)
		sb, _ := keys.UnB64(sig.Sig)
		out = b
		return tx.PutDeviceAccounts(store.DeviceAccountsRow{DeviceID: deviceID, Body: raw, OwnerFP: owner.FP, OwnerSig: sb,
			CreatedAt: tx.NowMS()})
	})
	return out, err
}

// AccountsTx is the verified machine_accounts document of a device for its certificate c,
// checked with the active owner key; ok is false when there is none, or it does not verify
// (another owner key, earlier keys of the device, an edited row: logged).
func (s *Service) AccountsTx(tx *store.Tx, deviceID string, c keys.Cert) (keys.Doc, keys.AccountsBody, bool, error) {
	r, err := tx.DeviceAccounts(deviceID)
	if errors.Is(err, store.ErrNotFound) {
		return keys.Doc{}, keys.AccountsBody{}, false, nil
	}
	if err != nil {
		return keys.Doc{}, keys.AccountsBody{}, false, err
	}
	owner, err := s.activeOwnerTx(tx)
	if err != nil {
		return keys.Doc{}, keys.AccountsBody{}, false, err
	}
	return s.checkAccounts(r, owner, c)
}

// Accounts is AccountsTx outside a transaction.
func (s *Service) Accounts(ctx context.Context, deviceID string, c keys.Cert) (keys.Doc, keys.AccountsBody, bool, error) {
	r, err := s.Store.DeviceAccounts(ctx, deviceID)
	if errors.Is(err, store.ErrNotFound) {
		return keys.Doc{}, keys.AccountsBody{}, false, nil
	}
	if err != nil {
		return keys.Doc{}, keys.AccountsBody{}, false, err
	}
	owner, err := s.ActiveOwner(ctx)
	if err != nil {
		return keys.Doc{}, keys.AccountsBody{}, false, err
	}
	return s.checkAccounts(r, owner, c)
}

func (s *Service) checkAccounts(r store.DeviceAccountsRow, owner *OwnerKeyInfo, c keys.Cert) (keys.Doc, keys.AccountsBody, bool, error) {
	if owner == nil || r.OwnerFP != owner.FP {
		return keys.Doc{}, keys.AccountsBody{}, false, nil // signed by a key that is no longer the owner's
	}
	d := accountsDoc(r)
	b, err := keys.VerifyAccounts(d, keys.Trust{Hub: s.Key.Pub, Owner: owner.Pub}, c)
	if err != nil {
		s.logf("keys: machine_accounts of %s ignored: %v", r.DeviceID, err)
		return keys.Doc{}, keys.AccountsBody{}, false, nil
	}
	return d, b, true, nil
}
