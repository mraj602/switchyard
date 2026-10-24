package pair

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/keys"
)

func keyedFixture(t *testing.T) *fixture {
	f := newFixture(t)
	hk, _ := hubkeys.FromSeed(bytes.Repeat([]byte{1}, 32))
	f.svc.Keys = hubkeys.New(f.st, hk)
	return f
}

func phoneKeys(t *testing.T) keys.DeviceKeysNew {
	t.Helper()
	sk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sb, _ := sk.PublicKey.Bytes()
	bk, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	return keys.DeviceKeysNew{Sign: keys.KeySpec{Alg: keys.AlgP256, Pub: keys.B64(sb)},
		Box: keys.KeySpec{Alg: keys.AlgP256, Pub: keys.B64(bk.PublicKey().Bytes())}, Auth: keys.B64(auth)}
}

func machineKeys(t *testing.T) keys.DeviceKeysNew {
	t.Helper()
	dk, err := keys.NewDeviceKeys(t0, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := dk.Public()
	return p
}

// qrParams reads h= and k= from a QR URL's fragment.
func qrParams(t *testing.T, u string) url.Values {
	t.Helper()
	_, frag, _ := strings.Cut(u, "#/pair?")
	v, err := url.ParseQuery(frag)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func seal(t *testing.T, k []byte, secret string, dk keys.DeviceKeysNew) string {
	t.Helper()
	b, _ := json.Marshal(dk)
	s, err := keys.SealKeys(k, secret, b, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func claimStatus(err error) int {
	var ce *ClaimError
	if errors.As(err, &ce) {
		return ce.Status
	}
	if errors.Is(err, ErrInvalid) {
		return 401
	}
	return 0
}

func (f *fixture) live(id string) bool {
	f.t.Helper()
	st, err := f.svc.Status(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return st.State == "live"
}

func TestQRCarriesHubFingerprintAndSealKey(t *testing.T) {
	f := keyedFixture(t)
	c := f.code("phone")
	v := qrParams(t, c.URL)
	if v.Get("h") != f.svc.Keys.Key.FP {
		t.Fatalf("h=%q", v.Get("h"))
	}
	k, err := keys.UnB64(v.Get("k"))
	if err != nil || len(k) != 16 || len(v.Get("k")) != 22 {
		t.Fatalf("k=%q", v.Get("k"))
	}
	want, _ := f.svc.SealKey(c.Secret)
	if !bytes.Equal(k, want) {
		t.Fatal("k is not HMAC(pepper, seal:secret)[0:16]")
	}
	// Without a hub key the QR is as before.
	g := newFixture(t)
	if u := g.code("phone").URL; strings.Contains(u, "&h=") || strings.Contains(u, "&k=") {
		t.Fatalf("keyless hub: %s", u)
	}
}

func TestClaimWithSealedKeys(t *testing.T) {
	f := keyedFixture(t)
	ctx := context.Background()

	// Good: the QR's k seals the phone's keys; the reply has a qr certificate for them.
	c := f.code("phone")
	k, _ := keys.UnB64(qrParams(t, c.URL).Get("k"))
	pk := phoneKeys(t)
	r := claimOK(t, f, "10.0.0.1", ClaimRequest{Secret: c.Secret, KeysSealed: seal(t, k, c.Secret, pk)})
	if r.Cert == nil || r.Reg != keys.RegQR {
		t.Fatalf("reply %+v", r)
	}
	cert, err := keys.VerifyCert(*r.Cert, keys.Trust{Hub: f.svc.Keys.Key.Pub}, false)
	if err != nil || cert.Body.Device != r.Device.ID || cert.Body.Sign.Pub != pk.Sign.Pub || cert.Body.Box.Pub != pk.Box.Pub || cert.Body.Reg != keys.RegQR {
		t.Fatalf("cert %+v %v", cert.Body, err)
	}
	if _, err := f.svc.Keys.Cert(ctx, r.Device.ID); err != nil {
		t.Fatalf("stored cert: %v", err)
	}
	// Replacing QR-registered keys is refused (keys.md 4.3).
	if _, err := f.svc.Keys.RegisterKeys(ctx, r.Device.ID, phoneKeys(t)); err == nil {
		t.Fatal("PUT keys over qr keys accepted")
	}

	// Bad seals: wrong k, another code's secret as AAD, garbage, tampered byte. Each is 400
	// and the code stays live, so the real phone can still claim it.
	c2 := f.code("phone")
	k2, _ := keys.UnB64(qrParams(t, c2.URL).Get("k"))
	good := seal(t, k2, c2.Secret, pk)
	raw, _ := keys.UnB64(good)
	raw[len(raw)-1] ^= 1
	for name, sealed := range map[string]string{
		"wrong k":      seal(t, k, c2.Secret, pk),
		"other secret": seal(t, k2, c.Secret, pk),
		"garbage":      "not-base64!",
		"tampered":     keys.B64(raw),
		"unknown fields": func() string {
			s, _ := keys.SealKeys(k2, c2.Secret, []byte(`{"sign":{},"box":{},"x":1}`), rand.Reader)
			return s
		}(),
		"machine keys": seal(t, k2, c2.Secret, machineKeys(t)),
	} {
		_, err := f.svc.Claim(ctx, "10.0.0.2", ClaimRequest{Secret: c2.Secret, KeysSealed: sealed})
		if claimStatus(err) != 400 || !strings.Contains(err.Error(), "key registration invalid") {
			t.Errorf("%s: %v", name, err)
		}
		if !f.live(c2.ID) {
			t.Fatalf("%s: the code was consumed", name)
		}
	}
	// Downgrade: the QR path with keys in the clear is refused the same way.
	if _, err := f.svc.Claim(ctx, "10.0.0.2", ClaimRequest{Secret: c2.Secret, Keys: &pk}); claimStatus(err) != 400 {
		t.Fatalf("clear keys on a secret claim: %v", err)
	}
	if !f.live(c2.ID) {
		t.Fatal("downgrade consumed the code")
	}
	// Stripped: a proxy that drops the keys gets a device without a certificate, which the
	// app refuses (keys.md 7.3); the hub side is a plain claim.
	r = claimOK(t, f, "10.0.0.2", ClaimRequest{Secret: c2.Secret})
	if r.Cert != nil || r.Reg != "" {
		t.Fatalf("stripped claim has a cert: %+v", r)
	}
	// A sealed claim with an unknown secret is 401, as before.
	if _, err := f.svc.Claim(ctx, "10.0.0.3", ClaimRequest{Secret: strings.Repeat("A", 22), KeysSealed: good}); claimStatus(err) != 401 {
		t.Fatalf("unknown secret: %v", err)
	}
}

func TestClaimWithClearKeys(t *testing.T) {
	f := keyedFixture(t)
	ctx := context.Background()
	c := f.code("machine", func(r *CreateRequest) { r.Machine = testMachine })
	mk := machineKeys(t)
	// Typed codes cannot carry sealed keys.
	if _, err := f.svc.Claim(ctx, "10.0.0.1", ClaimRequest{Code: c.Code, KeysSealed: "x"}); claimStatus(err) != 400 {
		t.Fatalf("sealed keys with a typed code: %v", err)
	}
	// Kind mismatch: phone keys on a machine code.
	pk := phoneKeys(t)
	if _, err := f.svc.Claim(ctx, "10.0.0.1", ClaimRequest{Code: c.Code, Keys: &pk}); claimStatus(err) != 400 {
		t.Fatalf("phone keys on a machine code: %v", err)
	}
	if !f.live(c.ID) {
		t.Fatal("kind mismatch consumed the code")
	}
	r := claimOK(t, f, "10.0.0.1", ClaimRequest{Code: c.Code, Keys: &mk})
	if r.Cert == nil || r.Reg != keys.RegTOFU {
		t.Fatalf("typed claim %+v", r)
	}
	// TOFU keys may be replaced once, before an owner certifies them.
	if _, err := f.svc.Keys.RegisterKeys(ctx, r.Device.ID, machineKeys(t)); err != nil {
		t.Fatalf("PUT keys over tofu keys: %v", err)
	}
	// A hub without a key refuses keys rather than dropping them.
	g := newFixture(t)
	gc := g.code("machine", func(r *CreateRequest) { r.Machine = testMachine })
	if _, err := g.svc.Claim(ctx, "10.0.0.1", ClaimRequest{Code: gc.Code, Keys: &mk}); claimStatus(err) != 400 {
		t.Fatalf("keyless hub: %v", err)
	}
}
