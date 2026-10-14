package webpush

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/push/webpushtest"
)

// RFC 8291 section 5 and appendix A.
const (
	rfcPlain     = "When I grow up, I want to be a watermelon"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcBody      = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEncryptRFC8291ExampleByteExact(t *testing.T) {
	// The random source gives the sender's private key, then the salt.
	rnd := bytes.NewReader(append(b64(t, rfcASPrivate), b64(t, rfcSalt)...))
	got, err := Encrypt(rnd, []byte(rfcPlain), b64(t, rfcUAPublic), b64(t, rfcAuth), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := b64(t, rfcBody)
	if !bytes.Equal(got, want) {
		t.Fatalf("body differs from RFC 8291 section 5\n got %s\nwant %s",
			base64.RawURLEncoding.EncodeToString(got), rfcBody)
	}
	if len(got) != 144 {
		t.Fatalf("len %d, want 144", len(got))
	}
}

// The independent decoder reads the RFC's own message.
func TestIndependentDecoderReadsRFCExample(t *testing.T) {
	priv, err := ecdh.P256().NewPrivateKey(b64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	plain, pad, err := webpushtest.Decode(b64(t, rfcBody), priv, b64(t, rfcAuth))
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != rfcPlain || pad != 0 {
		t.Fatalf("got %q pad %d", plain, pad)
	}
}

func TestEncryptPaddingAndRoundTrip(t *testing.T) {
	priv, _ := ecdh.P256().GenerateKey(strings.NewReader(strings.Repeat("k", 64)))
	auth := bytes.Repeat([]byte{7}, 16)
	for _, n := range []int{0, 1, 100, 127, 128, 500, MaxPlaintext} {
		plain := bytes.Repeat([]byte("x"), n)
		rnd := bytes.NewReader(bytes.Repeat([]byte{9, 4, 1}, 30))
		body, err := Encrypt(rnd, plain, priv.PublicKey().Bytes(), auth, PadMultiple)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		got, pad, err := webpushtest.Decode(body, priv, auth)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("n=%d: round trip failed: %v", n, err)
		}
		rec := len(body) - headerLen - tagLen
		if rec > MaxPlaintext+1 {
			t.Fatalf("n=%d: record %d exceeds one record", n, rec)
		}
		if rec%PadMultiple != 0 && rec != MaxPlaintext+1 {
			t.Fatalf("n=%d: record %d is not a multiple of %d (pad %d)", n, rec, PadMultiple, pad)
		}
	}
	if _, err := Encrypt(bytes.NewReader(nil), make([]byte, MaxPlaintext+1), priv.PublicKey().Bytes(), auth, 0); err != ErrTooLarge {
		t.Fatalf("oversize: %v", err)
	}
}

func TestEncryptRejectsBadKeys(t *testing.T) {
	rnd := bytes.NewReader(bytes.Repeat([]byte{1}, 64))
	if _, err := Encrypt(rnd, []byte("x"), []byte("short"), make([]byte, 16), 0); err == nil {
		t.Fatal("bad p256dh accepted")
	}
	if _, err := Encrypt(rnd, []byte("x"), b64(t, rfcUAPublic), make([]byte, 15), 0); err == nil {
		t.Fatal("bad auth accepted")
	}
}

func TestValidateKeys(t *testing.T) {
	if _, _, err := ValidateKeys(rfcUAPublic, rfcAuth); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidateKeys(rfcUAPublic+"=", rfcAuth+"=="); err != nil {
		t.Fatalf("padding should be tolerated: %v", err)
	}
	// A point not on the curve: flip the last byte of Y.
	bad := b64(t, rfcUAPublic)
	bad[64] ^= 1
	for name, p := range map[string][2]string{
		"off-curve": {base64.RawURLEncoding.EncodeToString(bad), rfcAuth},
		"short":     {"AAAA", rfcAuth},
		"auth len":  {rfcUAPublic, "AAAA"},
		"not b64":   {"!!!", rfcAuth},
	} {
		if _, _, err := ValidateKeys(p[0], p[1]); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
