package link

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/push/webpush"
	"github.com/mraj602/switchyard/internal/hub/push/webpushtest"
	"github.com/mraj602/switchyard/internal/keys"
)

var update = flag.Bool("update", false, "rewrite testdata/*.json (the vectors shared with the Kotlin tests)")

// Fixed inputs (link.md 10): the hub seed 0x01…01, the machine's Ed25519 seed 0x02…02 and
// X25519 key 0x03…03, the phone's P-256 key, auth secret, sender key and salt of RFC 8291
// appendix A.
var (
	hubSeed     = bytes.Repeat([]byte{0x01}, 32)
	machineSeed = bytes.Repeat([]byte{0x02}, 32)
	machineBox  = bytes.Repeat([]byte{0x03}, 32)
)

const (
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"

	machineDev = "dev_01JA4VECT0RMACH1NE000000"
	phoneDev   = "dev_01JA4VECT0RPH0NE00000000"
	otherDev   = "dev_01JA4VECT0R0THER00000000"
	nonce      = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA" // bytes 1..32
	nonce2     = "ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8" // bytes 32..63
	chalExp    = 1760518860
	serverTime = "2025-10-15T09:00:00.000Z" // chalExp − 60 s
	alertExp   = 1760540400
)

// hubSigner is the hub key of the vectors.
type hubSigner struct{ k ed25519.PrivateKey }

func (h hubSigner) SignLink(in []byte) ([]byte, error) { return ed25519.Sign(h.k, in), nil }

func vecHub() (hubSigner, ed25519.PublicKey) {
	k := ed25519.NewKeyFromSeed(hubSeed)
	return hubSigner{k}, k.Public().(ed25519.PublicKey)
}

func ub(t testing.TB, s string) []byte {
	t.Helper()
	b, err := keys.UnB64(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// vector is one testdata file: {"name", "keys", "fields", "signed_hex", "frame"}.
type vector struct {
	Name      string            `json:"name"`
	Keys      map[string]string `json:"keys"`
	Fields    map[string]any    `json:"fields"`
	SignedHex string            `json:"signed_hex,omitempty"`
	Frame     json.RawMessage   `json:"frame"`
}

func mustJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func frameFields(f Frame, device, ctx string, payload []byte) map[string]any {
	return map[string]any{"device": device, "t": f.T, "seq": f.Seq, "exp": f.Exp, "tag": f.Tag, "ctx": ctx,
		"payload_hex": hex.EncodeToString(payload)}
}

// alertPlain is the alert-phone plaintext: push.md 6.5's payload plus origin (13.2).
const alertPlain = `{"v":1,"type":"alert","alert":"al_01JA3B0000000000000000000","alerts":["al_01JA3B0000000000000000000"],` +
	`"trigger":"asked","title":"work · api · asked a question","body":"Should I also update the migration?",` +
	`"tag":"sy-s-c41e0000","url":"/#/sessions/c41e0000?alert=al_01JA3B0000000000000000000","tap":"auto",` +
	`"session":"c41e0000","needs_you":1,"private":false,"ts":"2026-10-15T09:00:00.000Z","origin":"https://hub.example.ts.net"}`

// build makes every vector from the fixed inputs. Randomised parts (the phone's ECDSA hello,
// the machine's HPKE body) are only made with -update; otherwise the files are checked.
func build(t *testing.T) []vector {
	hs, hubPub := vecHub()
	hubFP := keys.Fingerprint(hubPub)
	machine := ed25519.NewKeyFromSeed(machineSeed)
	box, err := ecdh.X25519().NewPrivateKey(machineBox)
	if err != nil {
		t.Fatal(err)
	}
	hubKeys := map[string]string{"hub": keys.B64(hubPub), "hub_fp": hubFP}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range hubKeys {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	var out []vector

	// challenge
	c, _ := NewChallenge(hs, hubFP, nonce, chalExp)
	out = append(out, vector{Name: "challenge", Keys: with(nil),
		Fields:    map[string]any{"hub": hubFP, "nonce": nonce, "exp": chalExp},
		SignedHex: hex.EncodeToString(ChallengeInput(hubFP, nonce, chalExp)), Frame: mustJSON(t, c)})
	bc := c
	bc.Nonce = nonce2
	out = append(out, vector{Name: "bad-challenge-nonce", Keys: with(nil), Fields: map[string]any{"hub": hubFP, "nonce": nonce2, "exp": chalExp},
		Frame: mustJSON(t, bc)})
	bc = c
	bc.Exp++
	out = append(out, vector{Name: "bad-challenge-exp", Keys: with(nil), Fields: map[string]any{"hub": hubFP, "nonce": nonce, "exp": chalExp + 1},
		Frame: mustJSON(t, bc)})

	// hello-machine (Ed25519 is deterministic)
	h := SignHelloEd25519(machine, c, machineDev, 41, Client{Kind: "agent", Sy: "0.9.0", OS: "linux", PingS: 180}, &Known{Matrix: 3, Revocations: 2})
	out = append(out, vector{Name: "hello-machine",
		Keys:      with(map[string]string{"device": machineDev, "sign_ed25519": keys.B64(machine.Public().(ed25519.PublicKey))}),
		Fields:    map[string]any{"hub": hubFP, "nonce": nonce, "exp": chalExp, "device": machineDev, "last": 41},
		SignedHex: hex.EncodeToString(HelloInput(hubFP, nonce, chalExp, machineDev, 41)), Frame: mustJSON(t, h)})

	// hello-phone (ECDSA: a signature to verify, made once)
	hp := Hello{T: TypeHello, V: Version, Device: phoneDev, Nonce: nonce, Exp: chalExp, Last: 7,
		Client: Client{Kind: "android", App: "0.9.0", Build: "14", SDK: 35, PingS: 180}}
	out = append(out, vector{Name: "hello-phone",
		Keys:      with(map[string]string{"device": phoneDev, "sign_p256": rfcUAPublic}),
		Fields:    map[string]any{"hub": hubFP, "nonce": nonce, "exp": chalExp, "device": phoneDev, "last": 7},
		SignedHex: hex.EncodeToString(HelloInput(hubFP, nonce, chalExp, phoneDev, 7)), Frame: mustJSON(t, hp)})

	// welcome (ephemeral, the nonce as context)
	wb := mustJSON(t, Welcome{V: 1, Device: phoneDev, Next: 57, PingS: 180, URLs: []string{"wss://hub.example.ts.net/v1/link"},
		ServerTime: serverTime})
	w, _ := SignFrame(hs, phoneDev, TypeWelcome, 0, chalExp+60, "", nonce, wb)
	out = append(out, vector{Name: "welcome", Keys: with(map[string]string{"device": phoneDev}),
		Fields: frameFields(w, phoneDev, nonce, wb), SignedHex: hex.EncodeToString(FrameInput(phoneDev, w.T, 0, w.Exp, "", nonce, wb)),
		Frame: mustJSON(t, w)})
	bw := w
	out = append(out, vector{Name: "bad-welcome-nonce", Keys: with(map[string]string{"device": phoneDev}),
		Fields: frameFields(bw, phoneDev, nonce2, wb), Frame: mustJSON(t, bw)})

	// action-result (ephemeral)
	ab := mustJSON(t, ActionResult{V: 1, ID: "act_0123456789abcdef", Status: 200, Applied: true,
		Alert: map[string]any{"id": "al_01JA3B0000000000000000000", "status": "seen"}})
	a, _ := SignFrame(hs, phoneDev, TypeActionResult, 0, chalExp+60, "", nonce, ab)
	out = append(out, vector{Name: "action-result", Keys: with(map[string]string{"device": phoneDev}),
		Fields: frameFields(a, phoneDev, nonce, ab), SignedHex: hex.EncodeToString(FrameInput(phoneDev, a.T, 0, a.Exp, "", nonce, ab)),
		Frame: mustJSON(t, a)})

	// alert-phone: RFC 8291 with the appendix's sender key and salt, so byte-exact
	rnd := bytes.NewReader(append(ub(t, rfcASPrivate), ub(t, rfcSalt)...))
	sealed, err := webpush.Encrypt(rnd, []byte(alertPlain), ub(t, rfcUAPublic), ub(t, rfcAuth), webpush.PadMultiple)
	if err != nil {
		t.Fatal(err)
	}
	phoneKeys := with(map[string]string{"device": phoneDev, "ua_private": rfcUAPrivate, "ua_public": rfcUAPublic, "auth": rfcAuth,
		"as_private": rfcASPrivate, "salt": rfcSalt})
	al, _ := SignFrame(hs, phoneDev, TypeAlert, 57, alertExp, "sy-s-c41e0000", CtxQueue, sealed)
	af := frameFields(al, phoneDev, CtxQueue, sealed)
	af["plaintext"] = alertPlain
	out = append(out, vector{Name: "alert-phone", Keys: phoneKeys, Fields: af,
		SignedHex: hex.EncodeToString(FrameInput(phoneDev, al.T, al.Seq, al.Exp, al.Tag, CtxQueue, sealed)), Frame: mustJSON(t, al)})

	// bad-*: one changed byte in each signed field, a wrong device, seq 0 on a queued type
	bad := func(name string, f Frame, fields map[string]any) {
		out = append(out, vector{Name: name, Keys: phoneKeys, Fields: fields, Frame: mustJSON(t, f)})
	}
	f := al
	f.T = "alerv"
	bad("bad-alert-type", f, af)
	f = al
	f.Seq = 58
	bad("bad-alert-seq", f, af)
	f = al
	f.Exp++
	bad("bad-alert-exp", f, af)
	f = al
	f.Tag = "sy-s-c41e0001"
	bad("bad-alert-tag", f, af)
	f = al
	body := append([]byte(nil), sealed...)
	body[len(body)-20] ^= 1
	f.Body = keys.B64(body)
	bad("bad-alert-body", f, af)
	f = al
	sig := ub(t, al.Sig)
	sig[5] ^= 1
	f.Sig = keys.B64(sig)
	bad("bad-alert-sig", f, af)
	other := frameFields(al, otherDev, CtxQueue, sealed)
	bad("bad-device", al, other)
	z, _ := SignFrame(hs, phoneDev, TypeAlert, 0, alertExp, "sy-s-c41e0000", CtxQueue, sealed)
	bad("bad-seq0", z, frameFields(z, phoneDev, CtxQueue, sealed))
	big1, _ := SignFrame(hs, phoneDev, TypeAlert, 1_000_000_000, alertExp, "", CtxQueue, sealed)
	sig = ub(t, big1.Sig)
	sig[0] ^= 1
	big1.Sig = keys.B64(sig)
	bad("bad-inserted-seq", big1, frameFields(big1, phoneDev, CtxQueue, sealed))

	// matrix-machine: HPKE is randomised, so the file holds a body to open
	mdoc, err := os.ReadFile(filepath.Join("..", "keys", "testdata", "doc-matrix-hub.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc keys.Doc
	if err := json.Unmarshal(mdoc, &doc); err != nil {
		t.Fatal(err)
	}
	plain := mustJSON(t, doc)
	mbody, err := SealMachine(box.PublicKey().Bytes(), machineDev, TypeMatrix, plain)
	if err != nil {
		t.Fatal(err)
	}
	mf, _ := SignFrame(hs, machineDev, TypeMatrix, 3, chalExp+30*86400, "matrix", CtxQueue, mbody)
	mff := frameFields(mf, machineDev, CtxQueue, mbody)
	mff["plaintext"] = string(plain)
	out = append(out, vector{Name: "matrix-machine",
		Keys: with(map[string]string{"device": machineDev, "x25519_public": keys.B64(box.PublicKey().Bytes()),
			"x25519_private": keys.B64(machineBox), "sign_ed25519": keys.B64(machine.Public().(ed25519.PublicKey))}),
		Fields: mff, SignedHex: hex.EncodeToString(FrameInput(machineDev, mf.T, mf.Seq, mf.Exp, mf.Tag, CtxQueue, mbody)),
		Frame: mustJSON(t, mf)})
	return out
}

func vecPath(name string) string { return filepath.Join("testdata", name+".json") }

// signP256 makes the phone's ECDSA hello signature with the RFC 8291 ua_private scalar.
func signP256(t *testing.T, in []byte) []byte {
	d := new(big.Int).SetBytes(ub(t, rfcUAPrivate))
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), ub(t, rfcUAPublic))
	if err != nil {
		t.Fatal(err)
	}
	k := &ecdsa.PrivateKey{PublicKey: *pub, D: d}
	h := sha256.Sum256(in)
	sig, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestVectors(t *testing.T) {
	vs := build(t)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			switch v.Name {
			case "hello-phone":
				var h Hello
				_ = json.Unmarshal(v.Frame, &h)
				h.Sig = keys.B64(signP256(t, HelloInput(v.Fields["hub"].(string), h.Nonce, h.Exp, h.Device, h.Last)))
				v.Frame = mustJSON(t, h)
			}
			b, _ := json.MarshalIndent(v, "", "  ")
			if err := os.WriteFile(vecPath(v.Name), append(b, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, hubPub := vecHub()
	files, _ := filepath.Glob(filepath.Join("testdata", "*.json"))
	if len(files) != len(vs) {
		t.Fatalf("testdata has %d files, the builder makes %d (run go test ./internal/link -update)", len(files), len(vs))
	}
	for _, want := range vs {
		t.Run(want.Name, func(t *testing.T) {
			b, err := os.ReadFile(vecPath(want.Name))
			if err != nil {
				t.Fatal(err)
			}
			var got vector
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			var cf bytes.Buffer
			if err := json.Compact(&cf, got.Frame); err != nil {
				t.Fatal(err)
			}
			got.Frame = cf.Bytes()
			if want.Name != "matrix-machine" && got.SignedHex != want.SignedHex {
				t.Fatalf("signed_hex differs from the builder")
			}
			checkVector(t, got, hubPub, want)
		})
	}
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func num(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// checkVector checks one file: its signed input rebuilt from fields, then the frame verifies
// (or, for bad-*, is rejected).
func checkVector(t *testing.T, v vector, hubPub ed25519.PublicKey, built vector) {
	f := v.Fields
	if v.SignedHex != "" {
		var in []byte
		switch {
		case v.Name == "challenge":
			in = ChallengeInput(str(f, "hub"), str(f, "nonce"), num(f, "exp"))
		case strings.HasPrefix(v.Name, "hello"):
			in = HelloInput(str(f, "hub"), str(f, "nonce"), num(f, "exp"), str(f, "device"), num(f, "last"))
		default:
			p, _ := hex.DecodeString(str(f, "payload_hex"))
			in = FrameInput(str(f, "device"), str(f, "t"), num(f, "seq"), num(f, "exp"), str(f, "tag"), str(f, "ctx"), p)
		}
		if hex.EncodeToString(in) != v.SignedHex {
			t.Fatalf("signed input not rebuilt byte for byte")
		}
	}
	bad := strings.HasPrefix(v.Name, "bad-")
	switch {
	case strings.Contains(v.Name, "challenge"):
		var c Challenge
		_ = json.Unmarshal(v.Frame, &c)
		if err := CheckChallenge(c, hubPub); (err != nil) != bad {
			t.Fatalf("challenge check: %v", err)
		}
	case strings.HasPrefix(v.Name, "hello"):
		var h Hello
		_ = json.Unmarshal(v.Frame, &h)
		alg, pub := keys.AlgEd25519, ub(t, v.Keys["sign_ed25519"])
		if v.Name == "hello-phone" {
			alg, pub = keys.AlgP256, ub(t, v.Keys["sign_p256"])
		} else if string(v.Frame) != string(built.Frame) {
			t.Fatalf("hello-machine frame differs from the builder")
		}
		if err := VerifyHello(h, str(f, "hub"), alg, pub); err != nil {
			t.Fatalf("hello: %v", err)
		}
		h.Last++
		if VerifyHello(h, str(f, "hub"), alg, pub) == nil {
			t.Fatalf("hello verified with a changed last")
		}
	default:
		var fr Frame
		_ = json.Unmarshal(v.Frame, &fr)
		body, err := Check(fr, hubPub, str(f, "device"), str(f, "ctx"))
		if bad {
			if err == nil {
				t.Fatalf("bad frame accepted")
			}
			return
		}
		if err != nil {
			t.Fatalf("frame: %v", err)
		}
		switch v.Name {
		case "alert-phone":
			if string(v.Frame) != string(built.Frame) {
				t.Fatalf("alert-phone is not byte-exact")
			}
			priv, _ := ecdh.P256().NewPrivateKey(ub(t, v.Keys["ua_private"]))
			plain, _, err := webpushtest.Decode(body, priv, ub(t, v.Keys["auth"]))
			if err != nil || string(plain) != str(f, "plaintext") {
				t.Fatalf("alert body does not open: %v", err)
			}
		case "matrix-machine":
			priv, _ := ecdh.X25519().NewPrivateKey(ub(t, v.Keys["x25519_private"]))
			plain, err := OpenMachine(priv, str(f, "device"), fr.T, body)
			if err != nil || string(plain) != str(f, "plaintext") {
				t.Fatalf("matrix body does not open: %v", err)
			}
			if _, err := OpenMachine(priv, otherDev, fr.T, body); !errors.Is(err, ErrOpen) {
				t.Fatalf("opened for another device")
			}
			if _, err := OpenMachine(priv, str(f, "device"), TypeRevoke, body); !errors.Is(err, ErrOpen) {
				t.Fatalf("opened as another type")
			}
		default:
			if string(v.Frame) != string(built.Frame) {
				t.Fatalf("%s differs from the builder", v.Name)
			}
		}
	}
}

func TestVectorNamesCoverSpec(t *testing.T) {
	var names []string
	for _, v := range build(t) {
		names = append(names, v.Name)
	}
	sort.Strings(names)
	for _, want := range []string{"challenge", "hello-machine", "hello-phone", "welcome", "alert-phone", "matrix-machine", "action-result", "bad-device", "bad-seq0"} {
		if i := sort.SearchStrings(names, want); i == len(names) || names[i] != want {
			t.Errorf("missing vector %s", want)
		}
	}
}
