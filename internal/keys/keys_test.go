package keys

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFingerprint(t *testing.T) {
	fp := Fingerprint(pubOf(hubKey()))
	if len(fp) != 24 || !ValidFP(fp) || strings.ToLower(fp) != fp {
		t.Fatalf("fp %q", fp)
	}
	g := GroupFP(fp)
	if len(g) != 29 || strings.Count(g, "-") != 5 {
		t.Fatalf("grouped %q", g)
	}
	if n, ok := NormalizeFP(strings.ToUpper(g)); !ok || n != fp {
		t.Fatalf("normalize %q -> %q", g, n)
	}
	for _, bad := range []string{"", fp[:23], fp + "a", strings.Replace(fp, fp[:1], "1", 1), strings.Replace(fp, fp[:1], "A", 1)} {
		if ValidFP(bad) {
			t.Errorf("ValidFP(%q)", bad)
		}
	}
}

func TestRecoveryCode(t *testing.T) {
	r := goldenR()
	code := RecoveryCode(r)
	if !strings.HasPrefix(code, "WYR1-") || len(code) != 5+52+12+1+4 {
		t.Fatalf("code shape %q (%d)", code, len(code))
	}
	// Round trip: code -> R -> seed -> the same public key.
	got, err := ParseRecoveryCode(code)
	if err != nil || got != r {
		t.Fatalf("parse: %v", err)
	}
	a, _ := OwnerFromRecovery(r)
	b, _ := OwnerFromRecovery(got)
	if !bytes.Equal(a, b) {
		t.Fatal("recovered key differs")
	}
	// Typing: lower case, spaces for dashes, 0/1/8 for O/I/B.
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	typed = strings.NewReplacer("o", "0", "i", "1", "b", "8").Replace(typed)
	if got, err := ParseRecoveryCode(typed); err != nil || got != r {
		t.Fatalf("typed form %q: %v", typed, err)
	}
	// A typo in a data group is caught by the check group.
	body := []byte(code)
	pos := 5
	if body[pos] == 'A' {
		body[pos] = 'C'
	} else {
		body[pos] = 'A'
	}
	if _, err := ParseRecoveryCode(string(body)); !errors.Is(err, ErrRecoveryCheck) {
		t.Fatalf("typo: %v", err)
	}
	for _, bad := range []string{"", "SYR2-AAAA", code[:len(code)-1], "XX" + code, code + "Q"} {
		if _, err := ParseRecoveryCode(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
	other := r
	other[0] ^= 1
	// Legacy compat: a code printed before the rename starts SYR1-; same R, same check group.
	if got, err := ParseRecoveryCode("SYR1-" + strings.TrimPrefix(code, "WYR1-")); err != nil || got != r {
		t.Fatalf("legacy SYR1 code: %v", err)
	}
	if RecoveryCode(other) == code {
		t.Fatal("different R, same code")
	}
}

func TestSealedOwner(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, 32)
	pass := "correct horse battery staple"
	s, err := SealOwner(seed, pass, 1000, goldenTime, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.KDF.Iter != 1000 || s.FP != Fingerprint(pubOf(ed25519.NewKeyFromSeed(seed))) {
		t.Fatalf("file %+v", s)
	}
	priv, err := s.Open(pass)
	if err != nil || !bytes.Equal(priv.Seed(), seed) {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.Open(pass + "x"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, err := SealOwner(seed, "short pass", 1000, goldenTime, nil); !errors.Is(err, ErrShortPassphrase) {
		t.Fatalf("short passphrase: %v", err)
	}
	// 11 characters refused, 12 taken (counted in characters, not bytes).
	if CheckPassphrase("ééééééééééé") == nil || CheckPassphrase("éééééééééééé") != nil {
		t.Fatal("passphrase length counts characters")
	}
	// The AAD binds fp; the pub check binds pub; iter is what the file says.
	other := pubOf(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32)))
	for name, mut := range map[string]func(*SealedOwner){
		"fp":   func(f *SealedOwner) { f.FP = Fingerprint(other) },
		"pub":  func(f *SealedOwner) { f.Pub = B64(other) },
		"iter": func(f *SealedOwner) { f.KDF.Iter = 1001 },
		"salt": func(f *SealedOwner) { f.KDF.Salt = B64(make([]byte, 16)) },
		"ct": func(f *SealedOwner) {
			b, _ := UnB64(f.CT)
			b[0] ^= 1
			f.CT = B64(b)
		},
	} {
		f := s
		mut(&f)
		if _, err := f.Open(pass); err == nil {
			t.Errorf("%s changed: still opens", name)
		}
	}
	// File round trip, mode 0600.
	dir := t.TempDir()
	p := filepath.Join(dir, "keys", OwnerSealedFile)
	if err := WriteSealedOwner(p, s); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	back, err := ReadSealedOwner(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := back.Open(pass); err != nil {
		t.Fatal(err)
	}
}

// docFixture signs a fresh document of each type with the golden keys.
func docFixture(t *testing.T, typ string, body []byte, signers ...string) Doc {
	d := NewDoc(typ, body)
	for _, by := range signers {
		k := hubKey()
		switch by {
		case ByOwner:
			k = ownerKey(t)
		case ByDevice:
			k = ed25519.NewKeyFromSeed(machineSeed)
		}
		if err := d.Sign(by, k); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// rebody replaces one top-level body field and keeps the old signatures.
func rebody(t *testing.T, d Doc, field string, value any) Doc {
	b, _ := d.BodyBytes()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m[field] = value
	nb, _ := json.Marshal(m)
	return Doc{Type: d.Type, Body: B64(nb), Sigs: d.Sigs}
}

func TestVerifyRules(t *testing.T) {
	tr := goldenTrust(t)
	hubFP := tr.HubFP()
	m := machineKeys(t)
	cert := mustJSON(t, NewCertBody("dev_A", "machine", "laptop", "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", m, RegTOFU, hubFP, goldenTime))
	mx := BaselineMatrix(hubFP, goldenTime)
	mx.Version = 2
	matrix := mustJSON(t, mx)
	rev := mustJSON(t, RevocationBody{V: 1, Version: 1, Hub: hubFP, IssuedAt: FormatTime(goldenTime), Devices: []RevokedDevice{}})
	okb := mustJSON(t, OwnerKeyBody{V: 1, Pub: B64(tr.Owner), Hub: hubFP, CreatedAt: FormatTime(goldenTime)})

	cases := []struct {
		name string
		doc  Doc
		ok   bool
		want error
	}{
		{"owner_key self", docFixture(t, TypeOwnerKey, okb, ByOwner), true, nil},
		{"owner_key confirmed", docFixture(t, TypeOwnerKey, okb, ByOwner, ByHub), true, nil},
		{"owner_key hub only", docFixture(t, TypeOwnerKey, okb, ByHub), false, ErrSigners},
		{"owner_key hub first", docFixture(t, TypeOwnerKey, okb, ByHub, ByOwner), true, nil},
		{"cert hub", docFixture(t, TypeDeviceCert, cert, ByHub), true, nil},
		{"cert hub+owner", docFixture(t, TypeDeviceCert, cert, ByHub, ByOwner), true, nil},
		{"cert owner only", docFixture(t, TypeDeviceCert, cert, ByOwner), false, ErrSigners},
		{"cert device", docFixture(t, TypeDeviceCert, cert, ByHub, ByDevice), false, ErrSigners},
		{"matrix owner", docFixture(t, TypeMatrix, matrix, ByOwner), true, nil},
		{"matrix hub", docFixture(t, TypeMatrix, matrix, ByHub), true, nil},
		{"matrix both", docFixture(t, TypeMatrix, matrix, ByOwner, ByHub), false, ErrSigners},
		{"matrix none", docFixture(t, TypeMatrix, matrix), false, ErrSigners},
		{"revocations owner", docFixture(t, TypeRevocations, rev, ByOwner), true, nil},
		{"revocations hub", docFixture(t, TypeRevocations, rev, ByHub), true, nil},
		{"revocations device", docFixture(t, TypeRevocations, rev, ByDevice), false, ErrSigners},
	}
	for _, c := range cases {
		err := verifyAny(t, c.doc)
		if c.ok != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: err %v, want ok=%v %v", c.name, err, c.ok, c.want)
		}
	}

	// Each body field changed under the old signatures → reject.
	for _, d := range []Doc{
		docFixture(t, TypeOwnerKey, okb, ByOwner, ByHub),
		docFixture(t, TypeDeviceCert, cert, ByHub, ByOwner),
		docFixture(t, TypeMatrix, matrix, ByOwner),
		docFixture(t, TypeRevocations, rev, ByHub),
	} {
		b, _ := d.BodyBytes()
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		for f := range m {
			if err := verifyAny(t, rebody(t, d, f, "changed")); err == nil {
				t.Errorf("%s: field %s changed: accepted", d.Type, f)
			}
		}
	}

	// A signer listed twice, or a signature with a bad length, is a reject.
	d := docFixture(t, TypeDeviceCert, cert, ByHub)
	dup := Doc{Type: d.Type, Body: d.Body, Sigs: []Sig{d.Sigs[0], d.Sigs[0]}}
	if err := verifyAny(t, dup); !errors.Is(err, ErrSigners) {
		t.Errorf("duplicate signer: %v", err)
	}
	short := Doc{Type: d.Type, Body: d.Body, Sigs: []Sig{{By: ByHub, FP: d.Sigs[0].FP, Sig: B64(make([]byte, 10))}}}
	if err := verifyAny(t, short); !errors.Is(err, ErrSignature) {
		t.Errorf("short signature: %v", err)
	}
	// An owner-signed certificate checked without an owner key: no key, reject.
	if _, err := VerifyCert(docFixture(t, TypeDeviceCert, cert, ByHub, ByOwner), Trust{Hub: tr.Hub}, false); !errors.Is(err, ErrSignature) {
		t.Errorf("owner signature without an owner pin: %v", err)
	}
	// requireOwner: a hub-only certificate is uncertified.
	if _, err := VerifyCert(docFixture(t, TypeDeviceCert, cert, ByHub), tr, true); !errors.Is(err, ErrUncertified) {
		t.Errorf("uncertified: %v", err)
	}
	// Signed by another owner key → reject.
	stranger := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	sd := NewDoc(TypeMatrix, matrix)
	_ = sd.Sign(ByOwner, stranger)
	if _, err := VerifyMatrix(sd, tr); !errors.Is(err, ErrSignature) {
		t.Errorf("stranger owner: %v", err)
	}
	// The type a verifier expects must be the document's.
	if _, err := Verify(docFixture(t, TypeMatrix, matrix, ByHub), TypeRevocations, tr.ring()); !errors.Is(err, ErrDocType) {
		t.Errorf("type confusion: %v", err)
	}
	// Body "v" other than 1, and unknown fields ignored.
	v2 := mx
	v2.V = 2
	if _, err := VerifyMatrix(docFixture(t, TypeMatrix, mustJSON(t, v2), ByHub), tr); !errors.Is(err, ErrBody) {
		t.Errorf("v 2: %v", err)
	}
	extra := strings.Replace(string(matrix), `{"v":1`, `{"v":1,"future":"grants nothing"`, 1)
	if _, err := VerifyMatrix(docFixture(t, TypeMatrix, []byte(extra), ByHub), tr); err != nil {
		t.Errorf("unknown field: %v", err)
	}
}

func TestWrongHub(t *testing.T) {
	tr := goldenTrust(t)
	other := Fingerprint(pubOf(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x0a}, 32))))
	mx := BaselineMatrix(other, goldenTime)
	if _, err := VerifyMatrix(docFixture(t, TypeMatrix, mustJSON(t, mx), ByOwner), tr); !errors.Is(err, ErrWrongHub) {
		t.Errorf("matrix: %v", err)
	}
	rv := RevocationBody{V: 1, Version: 1, Hub: other, IssuedAt: FormatTime(goldenTime), Devices: []RevokedDevice{}}
	if _, err := VerifyRevocations(docFixture(t, TypeRevocations, mustJSON(t, rv), ByOwner), tr); !errors.Is(err, ErrWrongHub) {
		t.Errorf("revocations: %v", err)
	}
	c := NewCertBody("dev_A", "machine", "x", "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", machineKeys(t), RegTOFU, other, goldenTime)
	if _, err := VerifyCert(docFixture(t, TypeDeviceCert, mustJSON(t, c), ByHub), tr, false); !errors.Is(err, ErrWrongHub) {
		t.Errorf("cert: %v", err)
	}
	ok := OwnerKeyBody{V: 1, Pub: B64(tr.Owner), Hub: other, CreatedAt: FormatTime(goldenTime)}
	if _, err := VerifyOwnerKey(docFixture(t, TypeOwnerKey, mustJSON(t, ok), ByOwner), tr.Hub); !errors.Is(err, ErrWrongHub) {
		t.Errorf("owner_key: %v", err)
	}
}

// linkFrameInput is link.md 4.3's signed input, built here by hand: the link package is not
// this one, and the point is that the two domains never overlap.
func linkFrameInput(device, typ string, seq, exp int64, tag, ctx string, payload []byte) []byte {
	s := "sy-link-v1\n" + device + "\n" + typ + "\n" + strconv.FormatInt(seq, 10) + "\n" + strconv.FormatInt(exp, 10) + "\n" +
		tag + "\n" + ctx + "\n"
	return append([]byte(s), payload...)
}

func TestDomainSeparation(t *testing.T) {
	hub := hubKey()
	tr := goldenTrust(t)
	mx := mustJSON(t, BaselineMatrix(tr.HubFP(), goldenTime))
	// A link frame signature over the same bytes is not a document signature...
	frameSig := ed25519.Sign(hub, linkFrameInput("dev_A", "matrix", 1, 2, "matrix", "queue", mx))
	d := Doc{Type: TypeMatrix, Body: B64(mx), Sigs: []Sig{{By: ByHub, FP: tr.HubFP(), Sig: B64(frameSig)}}}
	if _, err := VerifyMatrix(d, tr); !errors.Is(err, ErrSignature) {
		t.Errorf("frame signature taken as a document signature: %v", err)
	}
	// ...and a document signature is not a frame signature.
	docSig := ed25519.Sign(hub, DocInput(TypeMatrix, mx))
	if ed25519.Verify(pubOf(hub), linkFrameInput("dev_A", "matrix", 1, 2, "matrix", "queue", mx), docSig) {
		t.Error("document signature verifies as a frame")
	}
	if bytes.HasPrefix(DocInput(TypeMatrix, mx), []byte("sy-link-v1")) {
		t.Error("prefixes overlap")
	}
}

func TestWidens(t *testing.T) {
	base := BaselineMatrix("x", goldenTime)
	pair := []string{"a", "b"}
	with := func(f func(*MatrixBody)) MatrixBody {
		m := base
		m.Allow = append([][]string(nil), base.Allow...)
		f(&m)
		return m
	}
	allowed := with(func(m *MatrixBody) { m.Allow = [][]string{pair} })
	cases := []struct {
		name      string
		old, next MatrixBody
		widens    bool
	}{
		{"same", base, base, false},
		{"pair added", base, allowed, true},
		{"pair removed", allowed, base, false},
		{"pair swapped", allowed, with(func(m *MatrixBody) { m.Allow = [][]string{{"b", "a"}} }), true},
		{"same_account on", with(func(m *MatrixBody) { m.SameAccount = false }), base, true},
		{"same_account off", base, with(func(m *MatrixBody) { m.SameAccount = false }), false},
		{"resume", with(func(m *MatrixBody) { m.Paused = true }), base, true},
		{"pause", base, with(func(m *MatrixBody) { m.Paused = true }), false},
		{"turns up", with(func(m *MatrixBody) { m.MaxTurns = 30 }), with(func(m *MatrixBody) { m.MaxTurns = 31 }), true},
		{"turns down", with(func(m *MatrixBody) { m.MaxTurns = 30 }), with(func(m *MatrixBody) { m.MaxTurns = 29 }), false},
		{"turns to no limit", with(func(m *MatrixBody) { m.MaxTurns = 500 }), base, true},
		{"turns from no limit", base, with(func(m *MatrixBody) { m.MaxTurns = 500 }), false},
		{"no limit kept", base, base, false},
		{"labels only", base, with(func(m *MatrixBody) { m.Accounts = map[string]string{"a": "work"} }), false},
		{"version only", base, with(func(m *MatrixBody) { m.Version = 9 }), false},
	}
	for _, c := range cases {
		if got := Widens(c.old, c.next); got != c.widens {
			t.Errorf("%s: Widens = %v", c.name, got)
		}
	}
}

func TestAcceptMatrix(t *testing.T) {
	tr := goldenTrust(t)
	base := BaselineMatrix(tr.HubFP(), goldenTime)
	signed := func(m MatrixBody, by string) Signed[MatrixBody] {
		s, err := VerifyMatrix(docFixture(t, TypeMatrix, mustJSON(t, m), by), tr)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	wide := base
	wide.Version = 2
	wide.Allow = [][]string{{"a", "b"}}
	narrow := base
	narrow.Version = 2
	narrow.Paused = true

	// No document yet: compared with the baseline.
	if ok, err := AcceptMatrix(nil, signed(wide, ByHub), false); ok || !errors.Is(err, ErrHubWidening) {
		t.Errorf("hub widening, hub_may_widen=false: %v %v", ok, err)
	}
	if ok, err := AcceptMatrix(nil, signed(wide, ByHub), true); !ok || err != nil {
		t.Errorf("hub widening, default: %v %v", ok, err)
	}
	if ok, err := AcceptMatrix(nil, signed(wide, ByOwner), false); !ok || err != nil {
		t.Errorf("owner widening: %v %v", ok, err)
	}
	if ok, err := AcceptMatrix(nil, signed(narrow, ByHub), false); !ok || err != nil {
		t.Errorf("hub narrowing: %v %v", ok, err)
	}
	// Lower or equal versions are ignored, higher ones may skip.
	cur := wide
	if ok, err := AcceptMatrix(&cur, signed(narrow, ByOwner), true); ok || err != nil {
		t.Errorf("same version: %v %v", ok, err)
	}
	skip := narrow
	skip.Version = 7
	if ok, err := AcceptMatrix(&cur, signed(skip, ByHub), false); !ok || err != nil {
		t.Errorf("narrowing skip: %v %v", ok, err)
	}

	// The hub's own rules.
	if err := HubMaySignMatrix(base, wide, false); !errors.Is(err, ErrHubWidening) {
		t.Errorf("device widening: %v", err)
	}
	if err := HubMaySignMatrix(base, wide, true); err != nil {
		t.Errorf("owner session widening: %v", err)
	}
	if err := HubMaySignMatrix(base, narrow, false); err != nil {
		t.Errorf("device narrowing: %v", err)
	}
	if err := HubMaySignMatrix(base, skip, true); !errors.Is(err, ErrVersion) {
		t.Errorf("version skip at the hub: %v", err)
	}
	if err := HubTakesOwnerMatrix(&base, signed(wide, ByOwner)); err != nil {
		t.Errorf("owner upload: %v", err)
	}
	if err := HubTakesOwnerMatrix(&base, signed(wide, ByHub)); err == nil {
		t.Error("hub-signed upload taken as owner")
	}
	if err := HubTakesOwnerMatrix(&base, signed(skip, ByOwner)); !errors.Is(err, ErrVersion) {
		t.Errorf("owner upload skipping: %v", err)
	}
	if !HubMayRevoke("dev_a", "dev_a", false) || HubMayRevoke("dev_a", "dev_b", false) || !HubMayRevoke("dev_o", "dev_b", true) {
		t.Error("HubMayRevoke")
	}
	// Matrix field checks.
	for name, m := range map[string]func(*MatrixBody){
		"turns 1":   func(m *MatrixBody) { m.MaxTurns = 1 },
		"turns 501": func(m *MatrixBody) { m.MaxTurns = 501 },
		"turns -1":  func(m *MatrixBody) { m.MaxTurns = -1 },
		"unsorted":  func(m *MatrixBody) { m.Allow = [][]string{{"b", "a"}, {"a", "b"}} },
		"dup":       func(m *MatrixBody) { m.Allow = [][]string{{"a", "b"}, {"a", "b"}} },
		"triple":    func(m *MatrixBody) { m.Allow = [][]string{{"a", "b", "c"}} },
		"version 0": func(m *MatrixBody) { m.Version = 0 },
	} {
		b := base
		m(&b)
		if b.Check() == nil {
			t.Errorf("%s: passes Check", name)
		}
	}
	for _, n := range []int{0, 2, 30, 500} {
		b := base
		b.MaxTurns = n
		if err := b.Check(); err != nil {
			t.Errorf("max_turns %d: %v", n, err)
		}
	}
	if base.MaxTurns != NoTurnLimit {
		t.Errorf("baseline max_turns %d, want no limit", base.MaxTurns)
	}
}

func TestRevocationsNext(t *testing.T) {
	e := func(id string) RevokedDevice {
		return RevokedDevice{Device: id, Keys: []string{Fingerprint([]byte(id))}, At: FormatTime(goldenTime), Reason: "lost"}
	}
	v1 := RevocationBody{V: 1, Version: 1, Devices: []RevokedDevice{e("a")}}
	v2 := RevocationBody{V: 1, Version: 2, Devices: []RevokedDevice{e("a"), e("b")}}
	drop := RevocationBody{V: 1, Version: 3, Devices: []RevokedDevice{e("b")}}
	if ok, err := NextRevocations(nil, v1); !ok || err != nil {
		t.Error("first list")
	}
	if ok, err := NextRevocations(&v1, v2); !ok || err != nil {
		t.Error("grown list")
	}
	if ok, err := NextRevocations(&v2, v1); ok || err != nil {
		t.Error("older list must be ignored")
	}
	if _, err := NextRevocations(&v2, drop); !errors.Is(err, ErrDroppedEntry) {
		t.Errorf("dropped entry: %v", err)
	}
	if !v2.RevokedKey(Fingerprint([]byte("b"))) || v2.RevokedKey("x") || !v2.RevokedDevice("a") {
		t.Error("lookups")
	}
}

func TestCheckKeys(t *testing.T) {
	m := machineKeys(t)
	p := phoneKeys()
	offCurve := append([]byte{4}, make([]byte, 64)...)
	offCurve[64] = 1
	cases := []struct {
		name string
		kind string
		k    DeviceKeysNew
		ok   bool
	}{
		{"machine", "machine", m, true},
		{"desktop", "desktop", m, true},
		{"phone", "phone", p, true},
		{"machine keys for a phone", "phone", m, false},
		{"phone keys for a machine", "machine", p, false},
		{"browser", "browser", m, false},
		{"short ed25519", "machine", DeviceKeysNew{Sign: KeySpec{AlgEd25519, B64(make([]byte, 31))}, Box: m.Box}, false},
		{"auth on machine", "machine", DeviceKeysNew{Sign: m.Sign, Box: m.Box, Auth: phoneAuth}, false},
		{"phone off curve", "phone", DeviceKeysNew{Sign: p.Sign, Box: KeySpec{AlgP256, B64(offCurve)}, Auth: p.Auth}, false},
		{"phone sign off curve", "phone", DeviceKeysNew{Sign: KeySpec{AlgP256, B64(offCurve)}, Box: p.Box, Auth: p.Auth}, false},
		{"phone no auth", "phone", DeviceKeysNew{Sign: p.Sign, Box: p.Box}, false},
		{"phone short auth", "phone", DeviceKeysNew{Sign: p.Sign, Box: p.Box, Auth: B64(make([]byte, 15))}, false},
		{"padded base64", "machine", DeviceKeysNew{Sign: KeySpec{AlgEd25519, m.Sign.Pub + "="}, Box: m.Box}, false},
	}
	for _, c := range cases {
		if err := CheckKeys(c.kind, c.k, true); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestQRSeal(t *testing.T) {
	k := bytes.Repeat([]byte{5}, 16)
	plain := []byte(`{"sign":{}}`)
	s, err := SealKeys(k, "secret-1", plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := OpenKeys(k, "secret-1", s); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open: %v", err)
	}
	wrong := bytes.Repeat([]byte{6}, 16)
	b, _ := UnB64(s)
	b[len(b)-1] ^= 1
	for name, f := range map[string]func() ([]byte, error){
		"wrong k":      func() ([]byte, error) { return OpenKeys(wrong, "secret-1", s) },
		"wrong secret": func() ([]byte, error) { return OpenKeys(k, "secret-2", s) },
		"tampered":     func() ([]byte, error) { return OpenKeys(k, "secret-1", B64(b)) },
		"short":        func() ([]byte, error) { return OpenKeys(k, "secret-1", "AAAA") },
		"not b64":      func() ([]byte, error) { return OpenKeys(k, "secret-1", "!!") },
	} {
		if _, err := f(); !errors.Is(err, ErrSeal) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPinsAndDeviceKeys(t *testing.T) {
	state := t.TempDir()
	dk, made, err := LoadOrCreateDeviceKeys(state, goldenTime)
	if err != nil || !made {
		t.Fatal(err)
	}
	again, made, err := LoadOrCreateDeviceKeys(state, goldenTime)
	if err != nil || made || again.Sign.Seed != dk.Sign.Seed {
		t.Fatal("device keys not kept")
	}
	if fi, _ := os.Stat(Path(state, DeviceFile)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("device.json mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(state, Dir)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("keys/ mode %v", fi.Mode())
	}
	pub, _ := dk.Public()
	if err := CheckKeys("machine", pub, true); err != nil {
		t.Fatal(err)
	}

	hp := Path(state, HubPinFile)
	a := NewPin(pubOf(hubKey()), PinTOFU, goldenTime)
	b := NewPin(pubOf(ownerKey(t)), PinTOFU, goldenTime)
	if err := SetPin(hp, a); err != nil {
		t.Fatal(err)
	}
	if err := SetPin(hp, a); err != nil {
		t.Fatal("same key again must be a no-op")
	}
	if err := SetPin(hp, b); !errors.Is(err, ErrPinned) {
		t.Fatalf("network replace: %v", err)
	}
	if err := ReplacePin(hp, b); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadPin(hp)
	if got.FP != b.FP {
		t.Fatal("replace did not stick")
	}
	tr, err := PinnedTrust(state)
	if err != nil || tr.Owner != nil || !bytes.Equal(tr.Hub, pubOf(ownerKey(t))) {
		t.Fatalf("trust %v", err)
	}
	// A pin edited to another key than its fp is refused.
	bad := a
	bad.FP = b.FP
	_ = ReplacePin(hp, bad)
	if _, err := ReadPin(hp); err == nil {
		t.Fatal("inconsistent pin accepted")
	}
}

func TestSignRefusesDoubleSigner(t *testing.T) {
	d := NewDoc(TypeMatrix, []byte(`{"v":1}`))
	if err := d.Sign(ByHub, hubKey()); err != nil {
		t.Fatal(err)
	}
	if err := d.Sign(ByHub, hubKey()); !errors.Is(err, ErrSigners) {
		t.Fatalf("second hub signature: %v", err)
	}
	u := NewDoc("bogus", nil)
	if err := u.Sign(ByHub, hubKey()); !errors.Is(err, ErrDocType) {
		t.Fatal("unknown type signed")
	}
}

func TestPeerEnvelope(t *testing.T) {
	tr := goldenTrust(t)
	c := NewCertBody("dev_A", "machine", "laptop", "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", machineKeys(t), RegSSH, tr.HubFP(), goldenTime)
	cert, err := VerifyCert(docFixture(t, TypeDeviceCert, mustJSON(t, c), ByHub, ByOwner), tr, true)
	if err != nil {
		t.Fatal(err)
	}
	env := docFixture(t, TypePeerEnvelope, []byte(`{"v":1,"msg":"m1"}`), ByDevice)
	if _, err := VerifyPeerEnvelope(env, cert); err != nil {
		t.Fatal(err)
	}
	forged := NewDoc(TypePeerEnvelope, []byte(`{"v":1,"msg":"m1"}`))
	_ = forged.Sign(ByDevice, hubKey())
	if _, err := VerifyPeerEnvelope(forged, cert); !errors.Is(err, ErrSignature) {
		t.Fatalf("envelope signed by another key: %v", err)
	}
}
