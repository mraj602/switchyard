package keys

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/*.json (the shared key and document vectors)")

// Fixed inputs of the shared vectors (keys.md 12, link.md 10): the hub seed 0x01…01, a
// machine's Ed25519 seed 0x02…02 and X25519 key 0x03…03, the phone's P-256 key and auth
// secret of RFC 8291 appendix A, and the owner key made from R = 0x00, 0x01, …, 0x1f.
var (
	goldenTime  = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	hubSeed     = bytes.Repeat([]byte{0x01}, 32)
	machineSeed = bytes.Repeat([]byte{0x02}, 32)
	machineBox  = bytes.Repeat([]byte{0x03}, 32)
	// RFC 8291 appendix A: ua_public and auth_secret.
	phonePub  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	phoneAuth = "BTBZMqHH6r4Tts7J_aSIgg"
)

func goldenR() [32]byte {
	var r [32]byte
	for i := range r {
		r[i] = byte(i)
	}
	return r
}

func hubKey() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(hubSeed) }

func ownerKey(t testing.TB) ed25519.PrivateKey {
	k, err := OwnerFromRecovery(goldenR())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pubOf(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

func machineKeys(t testing.TB) DeviceKeysNew {
	var k DeviceKeys
	k.V = 1
	k.Sign.Alg, k.Sign.Seed = AlgEd25519, B64(machineSeed)
	k.Box.Alg, k.Box.Priv = AlgX25519, B64(machineBox)
	p, err := k.Public()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func phoneKeys() DeviceKeysNew {
	return DeviceKeysNew{Sign: KeySpec{AlgP256, phonePub}, Box: KeySpec{AlgP256, phonePub}, Auth: phoneAuth}
}

func mustJSON(t testing.TB, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// docVector is one file of testdata: the keys, the fields, the exact signed input of each
// signature, the document, and variants that must be rejected.
type docVector struct {
	Name      string            `json:"name"`
	Keys      map[string]string `json:"keys"`
	Fields    map[string]any    `json:"fields"`
	SignedHex string            `json:"signed_hex"`
	Doc       Doc               `json:"doc"`
	Bad       []badVector       `json:"bad"`
}

type badVector struct {
	Why string `json:"why"`
	Doc Doc    `json:"doc"`
}

func goldenTrust(t testing.TB) Trust { return Trust{Hub: pubOf(hubKey()), Owner: pubOf(ownerKey(t))} }

// goldenDocs builds every document vector from the fixed keys.
func goldenDocs(t testing.TB) []docVector {
	hub, owner := hubKey(), ownerKey(t)
	hubFP := Fingerprint(pubOf(hub))
	keys := map[string]string{"hub": B64(pubOf(hub)), "owner": B64(pubOf(owner))}
	var out []docVector
	add := func(name, typ string, body []byte, signers ...string) {
		d := NewDoc(typ, body)
		for _, by := range signers {
			k := hub
			if by == ByOwner {
				k = owner
			}
			if err := d.Sign(by, k); err != nil {
				t.Fatal(err)
			}
		}
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		v := docVector{Name: name, Keys: keys, Fields: map[string]any{"type": typ, "body": fields, "body_text": string(body)},
			SignedHex: hex.EncodeToString(DocInput(typ, body)), Doc: d}
		// Rejected variants: one changed body byte, a swapped signer, a missing and an extra
		// signature, the same signatures under another type.
		flip := append([]byte(nil), body...)
		flip[len(flip)/2] ^= 0x01
		v.Bad = append(v.Bad, badVector{"body byte changed", Doc{Type: typ, Body: B64(flip), Sigs: d.Sigs}})
		sw := d
		sw.Sigs = append([]Sig(nil), d.Sigs...)
		if sw.Sigs[0].By == ByHub {
			sw.Sigs[0].By = ByOwner
		} else {
			sw.Sigs[0].By = ByHub
		}
		v.Bad = append(v.Bad, badVector{"by swapped", sw})
		v.Bad = append(v.Bad, badVector{"no signature", Doc{Type: typ, Body: d.Body, Sigs: []Sig{}}})
		extra := Doc{Type: typ, Body: d.Body, Sigs: append(append([]Sig(nil), d.Sigs...), SignDoc(typ, body, ByDevice, hub))}
		v.Bad = append(v.Bad, badVector{"extra signature", extra})
		other := TypeMatrix
		if typ == TypeMatrix {
			other = TypeRevocations
		}
		v.Bad = append(v.Bad, badVector{"another type", Doc{Type: other, Body: d.Body, Sigs: d.Sigs}})
		out = append(out, v)
	}

	okBody := mustJSON(t, OwnerKeyBody{V: 1, Pub: B64(pubOf(owner)), Hub: hubFP, CreatedAt: FormatTime(goldenTime)})
	add("owner_key-pending", TypeOwnerKey, okBody, ByOwner)
	add("owner_key-confirmed", TypeOwnerKey, okBody, ByOwner, ByHub)

	m := machineKeys(t)
	cert := NewCertBody("dev_01JA4MACHINE00000000000000", "machine", "laptop", "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", m, RegSSH, hubFP, goldenTime)
	add("device_cert-machine-hub", TypeDeviceCert, mustJSON(t, cert), ByHub)
	add("device_cert-machine-owner", TypeDeviceCert, mustJSON(t, cert), ByHub, ByOwner)
	pc := NewCertBody("dev_01JA4PHONE0000000000000000", "phone", "Pixel 9 · app", "", phoneKeys(), RegQR, hubFP, goldenTime)
	add("device_cert-phone", TypeDeviceCert, mustJSON(t, pc), ByHub)

	mx := BaselineMatrix(hubFP, goldenTime)
	mx.Version = 4
	mx.Accounts = map[string]string{"3f1c0000-0000-4000-8000-000000000001": "work", "7a200000-0000-4000-8000-000000000002": "personal"}
	mx.Allow = SortAllow([][]string{{"7a200000-0000-4000-8000-000000000002", "3f1c0000-0000-4000-8000-000000000001"},
		{"3f1c0000-0000-4000-8000-000000000001", "7a200000-0000-4000-8000-000000000002"}})
	add("matrix-owner", TypeMatrix, mustJSON(t, mx), ByOwner)
	add("matrix-hub", TypeMatrix, mustJSON(t, mx), ByHub)

	rv := RevocationBody{V: 1, Version: 4, Hub: hubFP, IssuedAt: FormatTime(goldenTime.Add(10 * time.Minute)),
		Devices: []RevokedDevice{{Device: "dev_01JA4MACHINE00000000000000", Keys: []string{cert.SignFP(), cert.BoxFP()},
			At: FormatTime(goldenTime.Add(10 * time.Minute)), Reason: "lost"}}}
	add("revocations-owner", TypeRevocations, mustJSON(t, rv), ByOwner)
	add("revocations-hub", TypeRevocations, mustJSON(t, rv), ByHub)
	return out
}

// verifyAny checks a document of any type with the golden trust.
func verifyAny(t testing.TB, d Doc) error {
	tr := goldenTrust(t)
	var err error
	switch d.Type {
	case TypeOwnerKey:
		_, err = VerifyOwnerKey(d, tr.Hub)
	case TypeDeviceCert:
		_, err = VerifyCert(d, tr, false)
	case TypeMatrix:
		_, err = VerifyMatrix(d, tr)
	case TypeRevocations:
		_, err = VerifyRevocations(d, tr)
	default:
		err = ErrDocType
	}
	return err
}

func TestDocGoldens(t *testing.T) {
	dir := "testdata"
	for _, v := range goldenDocs(t) {
		path := filepath.Join(dir, "doc-"+v.Name+".json")
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		b = append(b, '\n')
		if *update {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/keys -update)", path, err)
		}
		if !bytes.Equal(want, b) {
			t.Errorf("%s differs from what the code makes (Ed25519 is deterministic); run -update if the change is intended", path)
		}
		// The file, read back, verifies; its signed input is rebuilt from fields; every bad
		// variant is rejected.
		var got docVector
		if err := json.Unmarshal(want, &got); err != nil {
			t.Fatal(err)
		}
		if err := verifyAny(t, got.Doc); err != nil {
			t.Errorf("%s: %v", v.Name, err)
		}
		body, _ := got.Doc.BodyBytes()
		if hex.EncodeToString(DocInput(got.Fields["type"].(string), []byte(got.Fields["body_text"].(string)))) != got.SignedHex ||
			!bytes.Equal(body, []byte(got.Fields["body_text"].(string))) {
			t.Errorf("%s: signed_hex not rebuilt from fields", v.Name)
		}
		for _, bad := range got.Bad {
			if err := verifyAny(t, bad.Doc); err == nil {
				t.Errorf("%s / %s: accepted", v.Name, bad.Why)
			}
		}
	}
}

// keyVectors is testdata/keys.json: fingerprints, the recovery code and the QR seal.
type keyVectors struct {
	Fingerprints []struct {
		Name    string `json:"name"`
		Pub     string `json:"pub"`
		FP      string `json:"fp"`
		Grouped string `json:"grouped"`
	} `json:"fingerprints"`
	Recovery struct {
		RHex    string `json:"r_hex"`
		Code    string `json:"code"`
		SeedHex string `json:"seed_hex"`
		Pub     string `json:"pub"`
		FP      string `json:"fp"`
	} `json:"recovery"`
	QRSeal struct {
		K      string `json:"k"`
		Secret string `json:"secret"`
		Nonce  string `json:"nonce"`
		Keys   string `json:"keys"`
		Sealed string `json:"sealed"`
	} `json:"qr_seal"`
}

func goldenKeyVectors(t testing.TB) keyVectors {
	var v keyVectors
	m := machineKeys(t)
	for _, k := range []struct{ name, pub string }{
		{"hub", B64(pubOf(hubKey()))}, {"owner", B64(pubOf(ownerKey(t)))},
		{"machine_sign", m.Sign.Pub}, {"machine_box", m.Box.Pub}, {"phone_p256", phonePub},
	} {
		pub, _ := UnB64(k.pub)
		fp := Fingerprint(pub)
		v.Fingerprints = append(v.Fingerprints, struct {
			Name    string `json:"name"`
			Pub     string `json:"pub"`
			FP      string `json:"fp"`
			Grouped string `json:"grouped"`
		}{k.name, k.pub, fp, GroupFP(fp)})
	}
	r := goldenR()
	seed, _ := SeedFromRecovery(r)
	ok := ownerKey(t)
	v.Recovery.RHex, v.Recovery.Code, v.Recovery.SeedHex = hex.EncodeToString(r[:]), RecoveryCode(r), hex.EncodeToString(seed)
	v.Recovery.Pub, v.Recovery.FP = B64(pubOf(ok)), Fingerprint(pubOf(ok))
	k := bytes.Repeat([]byte{0x05}, 16)
	nonce := bytes.Repeat([]byte{0x06}, 12)
	secret := "Q2xhdWRlIGlzIG5vdCBoZXJl"
	kj := mustJSON(t, phoneKeys())
	sealed, err := SealKeys(k, secret, kj, bytes.NewReader(nonce))
	if err != nil {
		t.Fatal(err)
	}
	v.QRSeal.K, v.QRSeal.Secret, v.QRSeal.Nonce, v.QRSeal.Keys, v.QRSeal.Sealed = B64(k), secret, B64(nonce), string(kj), sealed
	return v
}

func TestKeyGoldens(t *testing.T) {
	path := filepath.Join("testdata", "keys.json")
	b, err := json.MarshalIndent(goldenKeyVectors(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if *update {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/keys -update)", err)
	}
	if !bytes.Equal(want, b) {
		t.Fatalf("%s differs; run -update if intended", path)
	}
	var v keyVectors
	if err := json.Unmarshal(want, &v); err != nil {
		t.Fatal(err)
	}
	for _, f := range v.Fingerprints {
		if len(f.FP) != FPLen || !ValidFP(f.FP) {
			t.Errorf("%s: fingerprint shape %q", f.Name, f.FP)
		}
	}
	r, err := ParseRecoveryCode(v.Recovery.Code)
	if err != nil || hex.EncodeToString(r[:]) != v.Recovery.RHex {
		t.Fatalf("recovery code does not parse back: %v", err)
	}
	k, _ := UnB64(v.QRSeal.K)
	got, err := OpenKeys(k, v.QRSeal.Secret, v.QRSeal.Sealed)
	if err != nil || string(got) != v.QRSeal.Keys {
		t.Fatalf("qr seal vector does not open: %v", err)
	}
	if !errors.Is(verifyAny(t, Doc{Type: "nope"}), ErrDocType) {
		t.Fatal("unknown type accepted")
	}
}
