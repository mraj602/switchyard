package webpush

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// The RFC 8291 sealed-body vectors shared with the phone (push.md 14.10): the Kotlin
// SharedVectorsTest.webpushGoldensOpenIfPresent opens every testdata/*.json that has a
// ua_private and compares the plaintext. Inputs are fixed, so the files are reproducible.
// Run `go test ./internal/hub/push/webpush -run TestSharedVectors -update` after a change to
// Encrypt; without the flag the files are checked byte for byte.
var update = flag.Bool("update", false, "rewrite testdata/*.json (the vectors shared with the Kotlin tests)")

type sharedVector struct {
	Name      string `json:"name"`
	Note      string `json:"note"`
	UAPrivate string `json:"ua_private"`
	UAPublic  string `json:"ua_public"`
	Auth      string `json:"auth"`
	ASPrivate string `json:"as_private"`
	Salt      string `json:"salt"`
	Pad       int    `json:"pad_multiple"`
	Plaintext string `json:"plaintext"`
	Body      string `json:"body"`
}

func buildSharedVectors(t *testing.T) []sharedVector {
	t.Helper()
	enc := base64.RawURLEncoding
	mk := func(name, note string, uaPriv, asPriv, salt, auth []byte, plain string, pad int) sharedVector {
		ua, err := ecdh.P256().NewPrivateKey(uaPriv)
		if err != nil {
			t.Fatal(err)
		}
		body, err := Encrypt(bytes.NewReader(append(append([]byte{}, asPriv...), salt...)), []byte(plain), ua.PublicKey().Bytes(), auth, pad)
		if err != nil {
			t.Fatal(err)
		}
		return sharedVector{Name: name, Note: note, UAPrivate: enc.EncodeToString(uaPriv), UAPublic: enc.EncodeToString(ua.PublicKey().Bytes()),
			Auth: enc.EncodeToString(auth), ASPrivate: enc.EncodeToString(asPriv), Salt: enc.EncodeToString(salt), Pad: pad,
			Plaintext: plain, Body: enc.EncodeToString(body)}
	}
	dec := func(s string) []byte { b, _ := enc.DecodeString(s); return b }
	alert := `{"v":1,"alert":"al_1","title":"work · api · asked a question","body":"Should I also update the migration?"}`
	return []sharedVector{
		mk("rfc8291-appendix-a", "RFC 8291 section 5 / appendix A, no padding",
			dec(rfcUAPrivate), dec(rfcASPrivate), dec(rfcSalt), dec(rfcAuth), rfcPlain, 0),
		mk("alert-padded", "an alert-sized JSON payload padded to the hub's 128-byte multiple (push.md 6.3), other fixed keys",
			bytes.Repeat([]byte{0x04}, 32), bytes.Repeat([]byte{0x05}, 32), bytes.Repeat([]byte{0x06}, 16), bytes.Repeat([]byte{0x07}, 16), alert, PadMultiple),
	}
}

func TestSharedVectors(t *testing.T) {
	vs := buildSharedVectors(t)
	for _, v := range vs {
		path := filepath.Join("testdata", v.Name+".json")
		want, _ := json.MarshalIndent(v, "", "  ")
		want = append(want, '\n')
		if *update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run go test ./internal/hub/push/webpush -run TestSharedVectors -update)", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s is stale; run go test ./internal/hub/push/webpush -run TestSharedVectors -update", path)
		}
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "*.json"))
	if len(files) != len(vs) {
		t.Fatalf("testdata has %d files, the builder makes %d", len(files), len(vs))
	}
}
