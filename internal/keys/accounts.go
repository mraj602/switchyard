package keys

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// MaxBoundAccounts is the most accounts one machine_accounts document lists.
const MaxBoundAccounts = 64

// AccountsBody is the body of a machine_accounts document (keys.md 4.3): the accounts whose
// sessions one machine's device may speak for in peer envelopes. The owner signs it with
// `sy keys certify`; it names the certificate's device, machine and sign key, so new keys
// need a new document.
type AccountsBody struct {
	V        int      `json:"v"`
	Hub      string   `json:"hub"`
	Device   string   `json:"device"`
	Machine  string   `json:"machine"`
	Sign     string   `json:"sign"` // fingerprint of the certificate's sign key
	Accounts []string `json:"accounts"`
	IssuedAt string   `json:"issued_at"`
}

// NewAccountsBody builds the body for certificate c and the accounts (sorted, without
// duplicates or empty ids).
func NewAccountsBody(c CertBody, accounts []string, now time.Time) AccountsBody {
	var list []string
	for _, a := range accounts {
		if a != "" {
			list = append(list, a)
		}
	}
	slices.Sort(list)
	list = slices.Compact(list)
	if list == nil {
		list = []string{}
	}
	return AccountsBody{V: 1, Hub: c.Hub, Device: c.Device, Machine: c.MachineID(), Sign: c.SignFP(), Accounts: list,
		IssuedAt: FormatTime(now)}
}

// Has reports whether account is bound.
func (b AccountsBody) Has(account string) bool {
	return account != "" && slices.Contains(b.Accounts, account)
}

func (b AccountsBody) check() error {
	bad := func(f string) error { return fmt.Errorf("%w: machine_accounts %s", ErrBody, f) }
	if b.Device == "" || b.Machine == "" {
		return bad("device or machine")
	}
	if !ValidFP(b.Hub) || !ValidFP(b.Sign) {
		return bad("hub or sign")
	}
	if !validTime(b.IssuedAt) {
		return bad("issued_at")
	}
	if len(b.Accounts) > MaxBoundAccounts {
		return bad("accounts (too many)")
	}
	for i, a := range b.Accounts {
		if a == "" || len(a) > 128 || (i > 0 && a <= b.Accounts[i-1]) {
			return bad("accounts (empty, too long, unsorted or repeated)")
		}
	}
	return nil
}

// SignAccounts makes the owner-signed machine_accounts document of body.
func SignAccounts(body AccountsBody, owner ed25519.PrivateKey) (Doc, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Doc{}, err
	}
	d := NewDoc(TypeMachineAccounts, raw)
	return d, d.Sign(ByOwner, owner)
}

// VerifyAccounts checks a machine_accounts document: one owner signature with t.Owner, the
// body for this hub and for certificate c (its device, machine and sign key). A document
// made for a device's earlier keys does not verify.
func VerifyAccounts(d Doc, t Trust, c Cert) (AccountsBody, error) {
	if t.Owner == nil {
		return AccountsBody{}, fmt.Errorf("%w: no owner key to check machine_accounts with", ErrSignature)
	}
	raw, err := Verify(d, TypeMachineAccounts, KeyRing{ByOwner: t.Owner})
	if err != nil {
		return AccountsBody{}, err
	}
	var b AccountsBody
	if err := decodeBody(raw, &b); err != nil {
		return AccountsBody{}, err
	}
	if err := b.check(); err != nil {
		return AccountsBody{}, err
	}
	if b.Hub != t.HubFP() {
		return AccountsBody{}, fmt.Errorf("%w: machine_accounts of hub %s", ErrWrongHub, GroupFP(b.Hub))
	}
	if b.Device != c.Body.Device || b.Machine != c.Body.MachineID() || b.Sign != c.Body.SignFP() {
		return AccountsBody{}, fmt.Errorf("%w: machine_accounts is for %s/%s key %s, the certificate is %s/%s key %s", ErrBody,
			b.Device, b.Machine, GroupFP(b.Sign), c.Body.Device, c.Body.MachineID(), GroupFP(c.Body.SignFP()))
	}
	return b, nil
}
