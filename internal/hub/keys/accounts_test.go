package hubkeys

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/mraj602/watchyard/internal/keys"
)

// keys.md 4.3: the hub stores a machine_accounts document only when the active owner key
// signed it for the device's current certificate, and reads it back only while that holds.
func TestMachineAccounts(t *testing.T) {
	f := newFix(t)
	id := f.device("laptop", keys.RegTOFU, 7)
	other := f.device("worker", keys.RegTOFU, 8)
	c, err := f.svc.Cert(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	oc, _ := f.svc.Cert(f.ctx, other)
	sign := func(body keys.AccountsBody, k ed25519.PrivateKey) keys.Doc {
		d, err := keys.SignAccounts(body, k)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	body := keys.NewAccountsBody(c.Body, []string{"acc-b", "acc-a", "acc-a", ""}, t0)
	if !slices.Equal(body.Accounts, []string{"acc-a", "acc-b"}) {
		t.Fatalf("accounts %v", body.Accounts)
	}

	// No owner key yet.
	if _, err := f.svc.SetAccounts(f.ctx, id, sign(body, f.owner)); status(err) != 409 {
		t.Fatalf("without an owner key: %v", err)
	}
	f.activeOwner()
	stranger := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	for name, d := range map[string]keys.Doc{
		"another key":    sign(body, stranger),
		"another device": sign(keys.NewAccountsBody(oc.Body, []string{"acc-a"}, t0), f.owner),
		"hub signature": func() keys.Doc {
			d := keys.NewDoc(keys.TypeMachineAccounts, mustJSON(t, body))
			_ = f.svc.Key.Sign(&d)
			return d
		}(),
		"unsorted accounts":   sign(keys.AccountsBody{V: 1, Hub: body.Hub, Device: body.Device, Machine: body.Machine, Sign: body.Sign, Accounts: []string{"b", "a"}, IssuedAt: body.IssuedAt}, f.owner),
		"another sign key":    sign(keys.AccountsBody{V: 1, Hub: body.Hub, Device: body.Device, Machine: body.Machine, Sign: oc.Body.SignFP(), Accounts: body.Accounts, IssuedAt: body.IssuedAt}, f.owner),
		"another hub":         sign(keys.AccountsBody{V: 1, Hub: oc.Body.SignFP(), Device: body.Device, Machine: body.Machine, Sign: body.Sign, Accounts: body.Accounts, IssuedAt: body.IssuedAt}, f.owner),
		"another type's body": func() keys.Doc { d := sign(body, f.owner); d.Type = keys.TypeDeviceCert; return d }(),
	} {
		if _, err := f.svc.SetAccounts(f.ctx, id, d); status(err) != 400 {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, ok, _ := f.svc.Accounts(f.ctx, id, c); ok {
		t.Fatal("a refused document was stored")
	}

	if _, err := f.svc.SetAccounts(f.ctx, id, sign(body, f.owner)); err != nil {
		t.Fatal(err)
	}
	_, b, ok, err := f.svc.Accounts(f.ctx, id, c)
	if err != nil || !ok || !b.Has("acc-a") || b.Has("acc-c") || b.Has("") {
		t.Fatalf("stored: %+v %v %v", b, ok, err)
	}
	// Checked against another certificate (the device's keys replaced) it counts for nothing.
	if _, _, ok, _ := f.svc.Accounts(f.ctx, id, oc); ok {
		t.Fatal("document accepted for another certificate")
	}
	// An edited row is ignored.
	f.exec(`UPDATE device_accounts SET body = ? WHERE device_id = ?`,
		mustJSON(t, keys.AccountsBody{V: 1, Hub: body.Hub, Device: body.Device, Machine: body.Machine, Sign: body.Sign,
			Accounts: []string{"acc-a", "acc-b", "acc-evil"}, IssuedAt: body.IssuedAt}), id)
	if _, _, ok, _ := f.svc.Accounts(f.ctx, id, c); ok {
		t.Fatal("edited row trusted")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyAccountsNeedsOwner(t *testing.T) {
	_, err := keys.VerifyAccounts(keys.Doc{}, keys.Trust{}, keys.Cert{})
	if !errors.Is(err, keys.ErrSignature) {
		t.Fatalf("err %v", err)
	}
}
