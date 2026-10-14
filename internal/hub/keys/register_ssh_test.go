package hubkeys

import (
	"testing"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/keys"
)

// RegisterSSH (sy machine add through the admin channel, keys.md 4.2 reg ssh) and the
// idempotence of both registration paths.
func TestRegisterSSH(t *testing.T) {
	f := newFix(t)
	const mid = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"
	c, err := auth.Create(f.ctx, f.st, auth.CreateRequest{Kind: "machine", Name: "gpu-box", Machine: mid})
	if err != nil {
		t.Fatal(err)
	}
	dev := c.Device.ID
	k := machineKeysNew(2)

	// The machine id must match the device's, and the device must be a live machine.
	if _, err := f.svc.RegisterSSH(f.ctx, dev, "m_01JA2W5N0G8H3Q7S2V6X9Y1ZZZ", k); status(err) != 409 {
		t.Fatalf("other machine: %v", err)
	}
	if _, err := f.svc.RegisterSSH(f.ctx, dev, "", k); status(err) != 400 {
		t.Fatalf("no machine: %v", err)
	}
	if _, err := f.svc.RegisterSSH(f.ctx, "dev_nope", mid, k); status(err) != 404 {
		t.Fatalf("no device: %v", err)
	}

	doc, err := f.svc.RegisterSSH(f.ctx, dev, mid, k)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := keys.VerifyCert(doc, f.svc.Key.Trust(), false)
	if err != nil || cert.Body.Reg != keys.RegSSH || cert.Body.Device != dev || *cert.Body.Machine != mid || cert.Body.Sign != k.Sign {
		t.Fatalf("cert %+v %v", cert.Body, err)
	}
	// Again: the same certificate, nothing revoked.
	again, err := f.svc.RegisterSSH(f.ctx, dev, mid, k)
	if err != nil || again.Body != doc.Body {
		t.Fatalf("rerun reissued: %v", err)
	}
	// The machine's own bearer (PUT .../keys) with the same keys: no-op; other keys: 409.
	if d, err := f.svc.RegisterKeys(f.ctx, dev, k); err != nil || d.Body != doc.Body {
		t.Fatalf("PUT same keys on an ssh device: %v", err)
	}
	if _, err := f.svc.RegisterKeys(f.ctx, dev, machineKeysNew(3)); status(err) != 409 {
		t.Fatalf("PUT other keys on an ssh device: %v", err)
	}
	if r, _ := f.svc.Revocations(f.ctx); r != nil {
		t.Fatalf("revocations after no-ops: %+v", r.Body)
	}

	// Owner-certified, then the same keys over SSH again: the signature stays.
	f.activeOwner()
	if err := f.svc.Certify(f.ctx, dev, keys.Fingerprint(pubOf(f.owner)), keys.SignDoc(keys.TypeDeviceCert, cert.Raw, keys.ByOwner, f.owner).Sig); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RegisterSSH(f.ctx, dev, mid, k); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.svc.Cert(f.ctx, dev); !c.OwnerSigned {
		t.Fatal("rerun dropped the owner signature")
	}

	// New keys over SSH replace the old ones, which are revoked; those cannot come back.
	k2 := machineKeysNew(4)
	if _, err := f.svc.RegisterSSH(f.ctx, dev, mid, k2); err != nil {
		t.Fatal(err)
	}
	r, _ := f.svc.Revocations(f.ctx)
	if r == nil || !r.Body.RevokedKey(cert.Body.SignFP()) {
		t.Fatal("replaced keys not revoked")
	}
	if c, _ := f.svc.Cert(f.ctx, dev); c.OwnerSigned || c.Body.Sign != k2.Sign {
		t.Fatalf("new certificate %+v owner %v", c.Body, c.OwnerSigned)
	}
	if _, err := f.svc.RegisterSSH(f.ctx, dev, mid, k); status(err) != 409 {
		t.Fatalf("revoked keys registered again: %v", err)
	}

	// A tofu device: the same keys by bearer are a no-op (not revoked), and the same keys
	// over SSH reissue the certificate as ssh.
	tofu := f.device("tofu", keys.RegTOFU, 6)
	tc, _ := f.svc.Cert(f.ctx, tofu)
	if _, err := f.svc.RegisterKeys(f.ctx, tofu, machineKeysNew(6)); err != nil {
		t.Fatalf("PUT same keys on tofu: %v", err)
	}
	if r2, _ := f.svc.Revocations(f.ctx); r2.Body.RevokedKey(tc.Body.SignFP()) {
		t.Fatal("re-registering the same tofu keys revoked them")
	}
	if _, err := f.svc.RegisterSSH(f.ctx, tofu, *tc.Body.Machine, machineKeysNew(6)); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.svc.Cert(f.ctx, tofu); c.Body.Reg != keys.RegSSH {
		t.Fatalf("tofu not reissued as ssh: %s", c.Body.Reg)
	}
}
