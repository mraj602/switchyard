package webpush

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
)

func verifyJWT(t *testing.T, v *VAPID, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", token)
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(hdr) != `{"typ":"JWT","alg":"ES256"}` {
		t.Fatalf("header %s", hdr)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 raw (R||S)", len(sig))
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), v.PublicBytes())
	if err != nil {
		t.Fatal(err)
	}
	der, _ := asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])})
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.VerifyASN1(pub, d[:], der) {
		t.Fatal("signature does not verify with the public key")
	}
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func newKey(t *testing.T) *VAPID {
	t.Helper()
	v, _, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestJWTVerifiesAndHasClaims(t *testing.T) {
	v := newKey(t)
	now := time.Unix(1_800_000_000, 0)
	tok, err := v.JWT("https://fcm.googleapis.com", "https://hub.example", now)
	if err != nil {
		t.Fatal(err)
	}
	c := verifyJWT(t, v, tok)
	if c["aud"] != "https://fcm.googleapis.com" || c["sub"] != "https://hub.example" ||
		int64(c["exp"].(float64)) != now.Add(12*time.Hour).Unix() {
		t.Fatalf("claims %v", c)
	}
	if len(v.PublicKey()) != 87 || v.PublicBytes()[0] != 4 {
		t.Fatal("public key is not a 65-byte uncompressed point")
	}
}

func TestAudience(t *testing.T) {
	for in, want := range map[string]string{
		"https://fcm.googleapis.com/fcm/send/abc": "https://fcm.googleapis.com",
		"https://example.com:8443/p?x=1":          "https://example.com:8443",
		"https://web.push.apple.com/Qx":           "https://web.push.apple.com",
	} {
		got, err := Audience(in)
		if err != nil || got != want {
			t.Errorf("Audience(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Audience("nonsense"); err == nil {
		t.Error("bad endpoint accepted")
	}
}

func TestJWTCachePerAudienceAndRenewal(t *testing.T) {
	v := newKey(t)
	t0 := time.Unix(1_800_000_000, 0)
	a1, _ := v.JWT("https://a.example", "s", t0)
	a2, _ := v.JWT("https://a.example", "s", t0.Add(10*time.Hour))
	if a1 != a2 {
		t.Fatal("token not reused within 11 h")
	}
	b, _ := v.JWT("https://b.example", "s", t0)
	if b == a1 {
		t.Fatal("audiences share a token")
	}
	other, _ := v.JWT("https://a.example", "other-sub", t0)
	if other == a1 {
		t.Fatal("subjects share a token")
	}
	later := t0.Add(11*time.Hour + time.Minute)
	a3, _ := v.JWT("https://a.example", "s", later)
	if a3 == a1 {
		t.Fatal("token not renewed in its last hour")
	}
	c := verifyJWT(t, v, a3)
	if int64(c["exp"].(float64)) != later.Add(12*time.Hour).Unix() {
		t.Fatalf("renewed exp %v", c["exp"])
	}
	// A clock that moved back does not serve a token issued in the future.
	a4, _ := v.JWT("https://a.example", "s", t0.Add(-time.Hour))
	if a4 == a3 {
		t.Fatal("token reused for an earlier time")
	}
}

func TestAuthorizationHeader(t *testing.T) {
	v := newKey(t)
	h, err := v.Authorization("https://x.example", "", time.Now())
	if err != nil || !strings.HasPrefix(h, "vapid t=") || !strings.HasSuffix(h, ", k="+v.PublicKey()) {
		t.Fatalf("%q %v", h, err)
	}
	tok := strings.TrimSuffix(strings.TrimPrefix(h, "vapid t="), ", k="+v.PublicKey())
	c := verifyJWT(t, v, tok)
	if _, has := c["sub"]; has {
		t.Fatal("empty subject should omit sub")
	}
}

func TestLoadVAPIDPersistsAndSharesAcrossCalls(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v1, err := LoadVAPID(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := LoadVAPID(ctx, st)
	if err != nil || v1.PublicKey() != v2.PublicKey() {
		t.Fatalf("second load made another key: %v", err)
	}
	set, err := st.PushSetting(ctx, SettingVapidPublic)
	if err != nil || string(set) != `"`+v1.PublicKey()+`"` {
		t.Fatalf("setting %s %v", set, err)
	}
	sec, err := st.HubSecret(ctx, SecretVapidPrivate)
	if err != nil || len(sec) != 32 {
		t.Fatalf("hub_secret %d bytes %v", len(sec), err)
	}
	// Losing the setting does not lose the key; it is rewritten.
	if err := st.PutPushSetting(ctx, SettingVapidPublic, json.RawMessage(`"stale"`)); err != nil {
		t.Fatal(err)
	}
	v3, _ := LoadVAPID(ctx, st)
	set, _ = st.PushSetting(ctx, SettingVapidPublic)
	if v3.PublicKey() != v1.PublicKey() || string(set) != `"`+v1.PublicKey()+`"` {
		t.Fatal("setting not repaired")
	}
}
