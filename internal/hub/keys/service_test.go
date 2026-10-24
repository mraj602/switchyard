package hubkeys

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
)

var t0 = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fix struct {
	t     *testing.T
	ctx   context.Context
	clk   *clock
	st    *store.Store
	svc   *Service
	owner ed25519.PrivateKey
	logs  *bytes.Buffer
}

func newFix(t *testing.T) *fix {
	t.Helper()
	clk := &clock{t: t0}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	k, _ := FromSeed(bytes.Repeat([]byte{1}, 32))
	svc := New(st, k)
	var logs bytes.Buffer
	svc.Log = log.New(&logs, "", 0)
	return &fix{t: t, ctx: context.Background(), clk: clk, st: st, svc: svc,
		owner: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32)), logs: &logs}
}

func (f *fix) exec(q string, args ...any) {
	f.t.Helper()
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error { _, err := tx.ExecContext(f.ctx, q, args...); return err }); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fix) activeOwner() {
	f.t.Helper()
	d, err := keys.NewOwnerKeyDoc(f.owner, f.svc.Key.FP, t0)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.ProposeAndConfirm(f.ctx, d); err != nil {
		f.t.Fatal(err)
	}
}

func machineKeysNew(seed byte) keys.DeviceKeysNew {
	dk, _ := keys.NewDeviceKeys(t0, bytes.NewReader(bytes.Repeat([]byte{seed}, 64)))
	p, _ := dk.Public()
	return p
}

// device makes a machine device and registers keys with reg.
func (f *fix) device(name string, reg string, seed byte) string {
	f.t.Helper()
	c, err := auth.Create(f.ctx, f.st, auth.CreateRequest{Kind: "machine", Name: name, Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4" + name[:1]})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error {
		_, err := f.svc.RegisterTx(tx, c.Device, machineKeysNew(seed), reg)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return c.Device.ID
}

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Concurrent first runs (the CLI and the server) end with one key, and an existing key file is
// never replaced by the fallback for file systems without hard links.
func TestHubKeyFileConcurrentCreatorsAgree(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", KeyFile)
	fps := make([]string, 8)
	var wg sync.WaitGroup
	for i := range fps {
		wg.Go(func() {
			k, err := LoadOrCreate(p)
			if err != nil {
				t.Error(err)
				return
			}
			fps[i] = k.FP
		})
	}
	wg.Wait()
	for _, fp := range fps {
		if fp != fps[0] {
			t.Fatalf("creators disagree: %v", fps)
		}
	}
	if err := writeExclusive(p, make([]byte, 32)); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("writeExclusive over an existing key: %v", err)
	}
	if k, err := Load(p); err != nil || k.FP != fps[0] {
		t.Fatalf("the key changed: %v", err)
	}
}

func TestHubKeyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", KeyFile)
	a, err := LoadOrCreate(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(p)
	if err != nil || a.FP != b.FP {
		t.Fatalf("second load: %v", err)
	}
	if fi, _ := os.Stat(p); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if runtime.GOOS != "windows" {
		os.Chmod(p, 0o644)
		if _, err := Load(p); err == nil {
			t.Fatal("world-readable key accepted")
		}
		os.Chmod(p, 0o600)
	}
	os.WriteFile(p, []byte("short"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("damaged key accepted")
	}
	if _, err := a.SignLink(keys.DocInput(keys.TypeMatrix, []byte("{}"))); err == nil {
		t.Fatal("SignLink signed a document input")
	}
	if _, err := a.SignLink([]byte("wy-link-v1\nx")); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerProposeConfirm(t *testing.T) {
	f := newFix(t)
	d, _ := keys.NewOwnerKeyDoc(f.owner, f.svc.Key.FP, t0)
	p, err := f.svc.Propose(f.ctx, d, "dev_laptop")
	if err != nil || p.State != "pending" {
		t.Fatalf("propose: %v", err)
	}
	if o, _ := f.svc.ActiveOwner(f.ctx); o != nil {
		t.Fatal("pending key is active")
	}
	if _, err := f.svc.Propose(f.ctx, d, "dev_laptop"); status(err) != 409 {
		t.Fatalf("same proposal again: %v", err)
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, 32))
	d2, _ := keys.NewOwnerKeyDoc(other, f.svc.Key.FP, t0)
	if _, err := f.svc.Propose(f.ctx, d2, "dev_x"); status(err) != 409 {
		t.Fatalf("second pending: %v", err)
	}
	// Not for this hub, or already hub-signed: refused.
	d3, _ := keys.NewOwnerKeyDoc(other, keys.Fingerprint([]byte("other hub")), t0)
	if _, err := f.svc.Propose(f.ctx, d3, "dev_x"); status(err) != 400 {
		t.Fatalf("other hub: %v", err)
	}
	if _, err := f.svc.Confirm(f.ctx, keys.Fingerprint(pubOf(other)), "dev_o"); status(err) != 404 {
		t.Fatalf("confirm unknown: %v", err)
	}
	a, err := f.svc.Confirm(f.ctx, p.FP, "dev_owner_session")
	if err != nil || a.State != "active" {
		t.Fatalf("confirm: %v", err)
	}
	// The confirmed document carries both signatures, and a device can pin from it.
	if ok, err := keys.VerifyOwnerKey(a.Doc, f.svc.Key.Pub); err != nil || !ok.Confirmed {
		t.Fatalf("confirmed doc: %v", err)
	}
	if _, err := f.svc.Confirm(f.ctx, p.FP, "dev_o"); status(err) != 409 {
		t.Fatalf("confirm twice: %v", err)
	}
	// Replacement: the new key retires the old one.
	if _, err := f.svc.Propose(f.ctx, d2, "dev_x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Confirm(f.ctx, keys.Fingerprint(pubOf(other)), "cli"); err != nil {
		t.Fatal(err)
	}
	o, _ := f.svc.ActiveOwner(f.ctx)
	if o == nil || o.FP != keys.Fingerprint(pubOf(other)) {
		t.Fatal("replacement not active")
	}
	rows, _ := f.st.OwnerKeys(f.ctx, "retired")
	if len(rows) != 1 || rows[0].FP != p.FP {
		t.Fatalf("retired %v", rows)
	}
}

func TestOwnerPendingExpires(t *testing.T) {
	f := newFix(t)
	d, _ := keys.NewOwnerKeyDoc(f.owner, f.svc.Key.FP, t0)
	p, _ := f.svc.Propose(f.ctx, d, "dev_x")
	f.clk.Add(PendingTTL + time.Minute)
	if l, _ := f.svc.PendingOwners(f.ctx); len(l) != 0 {
		t.Fatal("expired proposal listed")
	}
	if _, err := f.svc.Confirm(f.ctx, p.FP, "cli"); status(err) != 404 {
		t.Fatalf("confirm after 24 h: %v", err)
	}
}

// A database-only attacker (no hub_sign.key) cannot install an owner key.
func TestDBWriterCannotInstallOwner(t *testing.T) {
	f := newFix(t)
	evil := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{6}, 32))
	d, _ := keys.NewOwnerKeyDoc(evil, f.svc.Key.FP, t0)
	body, _ := d.BodyBytes()
	self, _ := keys.UnB64(d.Sigs[0].Sig)
	fp := keys.Fingerprint(pubOf(evil))
	// A row written as active with a made-up hub signature.
	f.exec(`INSERT INTO owner_key (fp, pub, body, self_sig, hub_sig, state, proposed_by, created_at) VALUES (?, ?, ?, ?, ?, 'active', 'x', 0)`,
		fp, []byte(pubOf(evil)), body, self, bytes.Repeat([]byte{7}, 64))
	if o, err := f.svc.ActiveOwner(f.ctx); err != nil || o != nil {
		t.Fatalf("forged active row used: %v %v", o, err)
	}
	tr, _ := f.svc.Trust(f.ctx)
	if tr.Owner != nil {
		t.Fatal("forged owner in trust")
	}
	// The hub's real confirmation of another document, copied onto this row, does not help.
	f.activeOwner()
	real, _ := f.svc.ActiveOwner(f.ctx)
	hubSig, _ := keys.UnB64(real.Doc.Sigs[1].Sig)
	f.exec(`UPDATE owner_key SET state = 'retired' WHERE fp = ?`, real.FP)
	f.exec(`UPDATE owner_key SET hub_sig = ? WHERE fp = ?`, hubSig, fp)
	if o, _ := f.svc.ActiveOwner(f.ctx); o != nil {
		t.Fatal("copied hub signature accepted")
	}
	if n, _ := f.svc.VerifyStored(f.ctx); n == 0 {
		t.Fatal("VerifyStored found nothing")
	}
	if !bytes.Contains(f.logs.Bytes(), []byte("owner_key row")) {
		t.Fatal("ignored row not logged")
	}
}

func TestDeviceCert(t *testing.T) {
	f := newFix(t)
	id := f.device("laptop", keys.RegTOFU, 2)
	c, err := f.svc.Cert(f.ctx, id)
	if err != nil || c.Body.Device != id || c.OwnerSigned {
		t.Fatalf("cert: %v %+v", err, c)
	}
	want := c.Body.SignFP()
	// Editing the key columns changes nothing: the certificate's keys are used.
	f.exec(`UPDATE device_key SET sign_pub = ?, box_pub = ? WHERE device_id = ?`, bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{9}, 32), id)
	if c, err := f.svc.Cert(f.ctx, id); err != nil || c.Body.SignFP() != want {
		t.Fatalf("column edit: %v", err)
	}
	// Editing the certificate body fails the hub signature.
	var raw []byte
	_ = f.st.R().QueryRowContext(f.ctx, `SELECT cert FROM device_key WHERE device_id = ?`, id).Scan(&raw)
	edited := bytes.Replace(raw, []byte(c.Body.Sign.Pub), []byte(keys.B64(bytes.Repeat([]byte{8}, 32))), 1)
	f.exec(`UPDATE device_key SET cert = ? WHERE device_id = ?`, edited, id)
	if _, err := f.svc.Cert(f.ctx, id); !errors.Is(err, ErrBadCert) {
		t.Fatalf("edited cert: %v", err)
	}
	// Another device's valid certificate copied over: the body names another device.
	other := f.device("server", keys.RegTOFU, 3)
	var oraw, osig []byte
	_ = f.st.R().QueryRowContext(f.ctx, `SELECT cert, hub_sig FROM device_key WHERE device_id = ?`, other).Scan(&oraw, &osig)
	f.exec(`UPDATE device_key SET cert = ?, hub_sig = ? WHERE device_id = ?`, oraw, osig, id)
	if _, err := f.svc.Cert(f.ctx, id); !errors.Is(err, ErrBadCert) {
		t.Fatalf("swapped cert: %v", err)
	}
	// A row inserted for a device without the hub key is refused.
	c3, _ := auth.Create(f.ctx, f.st, auth.CreateRequest{Kind: "machine", Name: "intruder", Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4I"})
	f.exec(`INSERT INTO device_key (device_id, sign_alg, sign_pub, box_alg, box_pub, reg, cert, hub_sig, created_at)
		VALUES (?, 'ed25519', ?, 'x25519', ?, 'tofu', ?, ?, 0)`, c3.Device.ID, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{1}, 32),
		[]byte(`{"v":1}`), bytes.Repeat([]byte{1}, 64))
	if _, err := f.svc.Cert(f.ctx, c3.Device.ID); !errors.Is(err, ErrBadCert) {
		t.Fatalf("inserted row: %v", err)
	}
	if _, err := f.svc.Cert(f.ctx, "dev_none"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no keys: %v", err)
	}
}

func TestCertifyAndReplaceRules(t *testing.T) {
	f := newFix(t)
	tofu := f.device("laptop", keys.RegTOFU, 2)
	qr := f.device("qrphone", keys.RegQR, 3)
	ssh := f.device("ssh", keys.RegSSH, 4)
	// Certify needs an active owner key.
	if err := f.svc.Certify(f.ctx, tofu, "x", "y"); status(err) != 409 {
		t.Fatalf("no owner: %v", err)
	}
	f.activeOwner()
	ofp := keys.Fingerprint(pubOf(f.owner))
	var raw []byte
	_ = f.st.R().QueryRowContext(f.ctx, `SELECT cert FROM device_key WHERE device_id = ?`, tofu).Scan(&raw)
	good := keys.SignDoc(keys.TypeDeviceCert, raw, keys.ByOwner, f.owner).Sig
	if err := f.svc.Certify(f.ctx, tofu, keys.Fingerprint([]byte("x")), good); status(err) != 400 {
		t.Fatalf("wrong fp: %v", err)
	}
	if err := f.svc.Certify(f.ctx, tofu, ofp, keys.SignDoc(keys.TypeMatrix, raw, keys.ByOwner, f.owner).Sig); status(err) != 400 {
		t.Fatalf("signature of another type: %v", err)
	}
	if err := f.svc.Certify(f.ctx, tofu, ofp, good); err != nil {
		t.Fatal(err)
	}
	c, _ := f.svc.Cert(f.ctx, tofu)
	if !c.OwnerSigned {
		t.Fatal("not owner-signed")
	}
	// The full document verifies on a machine that pins both keys, with requireOwner.
	if _, err := keys.VerifyCert(c.Doc, keys.Trust{Hub: f.svc.Key.Pub, Owner: pubOf(f.owner)}, true); err != nil {
		t.Fatal(err)
	}
	// A database writer cannot certify: a made-up owner signature is dropped.
	f.exec(`UPDATE device_key SET owner_fp = ?, owner_sig = ?, certified_at = 1 WHERE device_id = ?`, ofp, bytes.Repeat([]byte{3}, 64), ssh)
	if c, _ := f.svc.Cert(f.ctx, ssh); c.OwnerSigned {
		t.Fatal("forged owner signature counted")
	}
	// The 409 rule of PUT /v1/devices/{id}/keys.
	for _, id := range []string{tofu, qr, ssh} {
		if _, err := f.svc.RegisterKeys(f.ctx, id, machineKeysNew(7)); status(err) != 409 {
			t.Errorf("%s: replace verified keys: %v", id, err)
		}
	}
	plain := f.device("plain", keys.RegTOFU, 5)
	old, _ := f.svc.Cert(f.ctx, plain)
	if _, err := f.svc.RegisterKeys(f.ctx, plain, machineKeysNew(8)); err != nil {
		t.Fatal(err)
	}
	r, _ := f.svc.Revocations(f.ctx)
	if r == nil || !r.Body.RevokedKey(old.Body.SignFP()) {
		t.Fatal("replaced keys not in the revocation list")
	}
	if _, err := f.svc.RegisterKeys(f.ctx, plain, keys.DeviceKeysNew{Sign: keys.KeySpec{Alg: "p256", Pub: "x"}}); status(err) != 400 {
		t.Fatalf("bad keys: %v", err)
	}
	// A database writer flipping the reg column does not make qr keys replaceable: the
	// rule reads the signed certificate.
	f.exec(`UPDATE device_key SET reg = 'tofu' WHERE device_id = ?`, qr)
	if _, err := f.svc.RegisterKeys(f.ctx, qr, machineKeysNew(9)); status(err) != 409 {
		t.Fatalf("reg column flipped to tofu: %v", err)
	}
}

func TestRevocations(t *testing.T) {
	f := newFix(t)
	a := f.device("alpha", keys.RegTOFU, 2)
	b := f.device("beta", keys.RegTOFU, 3)
	if err := f.svc.RevokeDevice(f.ctx, a, "cli", "lost"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RevokeDevice(f.ctx, b, "dev_o", "lost"); err != nil {
		t.Fatal(err)
	}
	r, _ := f.svc.Revocations(f.ctx)
	if r == nil || r.Body.Version != 2 || !r.Body.RevokedDevice(a) || !r.Body.RevokedDevice(b) || r.Signer != keys.ByHub {
		t.Fatalf("list %+v", r)
	}
	if err := f.svc.RevokeDevice(f.ctx, a, "cli", "lost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	// A forged row with a higher version is ignored; the next real list goes above it.
	f.exec(`INSERT INTO revocation_doc (version, body, signer, key_fp, sig, made_by, created_at) VALUES (99, ?, 'hub', ?, ?, 'x', 0)`,
		[]byte(`{"v":1,"version":99,"devices":[]}`), f.svc.Key.FP, bytes.Repeat([]byte{1}, 64))
	if r, _ := f.svc.Revocations(f.ctx); r.Body.Version != 2 {
		t.Fatalf("forged list used: %d", r.Body.Version)
	}
	c := f.device("gamma", keys.RegTOFU, 4)
	_ = f.svc.RevokeDevice(f.ctx, c, "cli", "lost")
	r, _ = f.svc.Revocations(f.ctx)
	if r.Body.Version != 100 || len(r.Body.Devices) != 3 {
		t.Fatalf("after forged row: v%d %d entries", r.Body.Version, len(r.Body.Devices))
	}

	// Owner-signed uploads.
	f.activeOwner()
	tr, _ := f.svc.Trust(f.ctx)
	d := f.device("delta", keys.RegTOFU, 5)
	dc, _ := f.svc.Cert(f.ctx, d)
	sign := func(body keys.RevocationBody, k ed25519.PrivateKey, by string) keys.Doc {
		raw, _ := json.Marshal(body)
		doc := keys.NewDoc(keys.TypeRevocations, raw)
		_ = doc.Sign(by, k)
		return doc
	}
	next := r.Body
	next.Version = 101
	next.Devices = append(append([]keys.RevokedDevice(nil), r.Body.Devices...), keys.RevokedDevice{Device: d,
		Keys: []string{dc.Body.SignFP(), dc.Body.BoxFP()}, At: keys.FormatTime(t0), Reason: "stolen"})
	drop := next
	drop.Devices = drop.Devices[1:]
	if _, err := f.svc.AcceptOwnerRevocations(f.ctx, sign(drop, f.owner, keys.ByOwner), "dev_laptop"); status(err) != 409 {
		t.Fatalf("dropped entry: %v", err)
	}
	stale := next
	stale.Version = 50
	if _, err := f.svc.AcceptOwnerRevocations(f.ctx, sign(stale, f.owner, keys.ByOwner), "dev_laptop"); status(err) != 409 {
		t.Fatalf("old version: %v", err)
	}
	stranger := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	if _, err := f.svc.AcceptOwnerRevocations(f.ctx, sign(next, stranger, keys.ByOwner), "dev_laptop"); status(err) != 400 {
		t.Fatalf("stranger: %v", err)
	}
	revoked, err := f.svc.AcceptOwnerRevocations(f.ctx, sign(next, f.owner, keys.ByOwner), "dev_laptop")
	if err != nil || len(revoked) != 1 || revoked[0] != d {
		t.Fatalf("upload: %v %v", revoked, err)
	}
	dd, _ := f.st.DeviceDetailByID(f.ctx, d)
	if dd.RevokedAt == 0 {
		t.Fatal("device not revoked")
	}
	if r, _ := f.svc.Revocations(f.ctx); r.Signer != keys.ByOwner || r.Body.Version != 101 {
		t.Fatal("owner list not newest")
	}
	if _, err := keys.VerifyRevocations(sign(next, f.owner, keys.ByOwner), tr); err != nil {
		t.Fatal(err)
	}
}

// A database-only attacker cannot widen the matrix: rows it writes do not verify, and a
// real signature cannot be moved onto another body.
func TestMatrixRowsReverified(t *testing.T) {
	f := newFix(t)
	base := keys.BaselineMatrix(f.svc.Key.FP, t0)
	d, err := f.svc.SignMatrix(base)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := d.BodyBytes()
	sig, _ := keys.UnB64(d.Sigs[0].Sig)
	f.exec(`INSERT INTO matrix_doc (version, body, signer, key_fp, sig, widens, made_by, created_at) VALUES (1, ?, 'hub', ?, ?, 0, 'hub', 0)`,
		raw, f.svc.Key.FP, sig)
	wide := base
	wide.Version = 2
	wide.Allow = [][]string{{"a", "b"}}
	wraw, _ := json.Marshal(wide)
	f.exec(`INSERT INTO matrix_doc (version, body, signer, key_fp, sig, widens, made_by, created_at) VALUES (2, ?, 'hub', ?, ?, 1, 'x', 0)`,
		wraw, f.svc.Key.FP, sig)
	m, err := f.svc.Matrix(f.ctx)
	if err != nil || m == nil || m.Body.Version != 1 {
		t.Fatalf("matrix %+v %v", m, err)
	}
	if _, err := f.svc.SignMatrix(keys.BaselineMatrix("aaaaaaaaaaaaaaaaaaaaaaaa", t0)); !errors.Is(err, keys.ErrWrongHub) {
		t.Fatalf("other hub's body: %v", err)
	}
}

func pubOf(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

// An owner-signed revocation revokes a device whose replaced tofu keys are already on the
// list: the earlier entry (reason "replaced") does not make the live device count as revoked.
func TestOwnerRevocationAfterReplacedKeys(t *testing.T) {
	f := newFix(t)
	f.activeOwner()
	d := f.device("echo", keys.RegTOFU, 2)
	if _, err := f.svc.RegisterKeys(f.ctx, d, machineKeysNew(3)); err != nil {
		t.Fatal(err)
	}
	cur, _ := f.svc.Revocations(f.ctx)
	if cur == nil || !cur.Body.RevokedDevice(d) {
		t.Fatal("replaced keys not listed")
	}
	c, _ := f.svc.Cert(f.ctx, d)
	next := cur.Body
	next.Version++
	next.Devices = append(append([]keys.RevokedDevice(nil), cur.Body.Devices...), keys.RevokedDevice{Device: d,
		Keys: []string{c.Body.SignFP(), c.Body.BoxFP()}, At: keys.FormatTime(t0), Reason: "stolen"})
	raw, _ := json.Marshal(next)
	doc := keys.NewDoc(keys.TypeRevocations, raw)
	_ = doc.Sign(keys.ByOwner, f.owner)
	revoked, err := f.svc.AcceptOwnerRevocations(f.ctx, doc, "dev_laptop")
	if err != nil || len(revoked) != 1 || revoked[0] != d {
		t.Fatalf("revoked %v, %v", revoked, err)
	}
	if dd, _ := f.st.DeviceDetailByID(f.ctx, d); dd.RevokedAt == 0 {
		t.Fatal("device still live")
	}
}
