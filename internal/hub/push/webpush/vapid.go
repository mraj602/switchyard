package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
)

// Store keys (push.md 6.2).
const (
	SecretVapidPrivate = "vapid_private" // hub_secret key: the 32-byte scalar
	SettingVapidPublic = "push.vapid_public"
)

const (
	jwtLifetime = 12 * time.Hour
	jwtRenewIn  = time.Hour // a token is reused until this long before it expires
)

// VAPID holds the hub's application server key pair (RFC 8292) and signs the per-push
// JWTs, one per audience, cached until an hour before they expire. Safe for concurrent use.
type VAPID struct {
	priv   *ecdsa.PrivateKey
	public []byte // 65 bytes, 0x04 || X || Y
	pubB64 string

	mu    sync.Mutex
	cache map[string]cachedJWT
}

type cachedJWT struct {
	token string
	exp   time.Time
}

// NewVAPID builds the key pair from the 32-byte private scalar.
func NewVAPID(scalar []byte) (*VAPID, error) {
	if len(scalar) != 32 {
		return nil, errors.New("webpush: VAPID private key must be 32 bytes")
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar)
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID private key: %w", err)
	}
	ek, err := ecdh.P256().NewPrivateKey(scalar)
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID private key: %w", err)
	}
	pub := ek.PublicKey().Bytes()
	return &VAPID{priv: priv, public: pub, pubB64: base64.RawURLEncoding.EncodeToString(pub), cache: map[string]cachedJWT{}}, nil
}

// GenerateVAPID makes a new random key pair and returns it with its private scalar.
func GenerateVAPID() (*VAPID, []byte, error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	scalar := k.Bytes()
	v, err := NewVAPID(scalar)
	return v, scalar, err
}

// LoadVAPID returns the hub's key pair, creating and storing it on first use: the scalar in
// hub_secret `vapid_private`, the public key (base64url) in setting `push.vapid_public`
// (kept in sync even if the setting was lost). Concurrent callers get the same key.
func LoadVAPID(ctx context.Context, st *store.Store) (*VAPID, error) {
	scalar, err := st.EnsureHubSecret(ctx, SecretVapidPrivate, func() ([]byte, error) {
		_, s, err := GenerateVAPID()
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("webpush: vapid key: %w", err)
	}
	v, err := NewVAPID(scalar)
	if err != nil {
		return nil, err
	}
	want, _ := json.Marshal(v.pubB64)
	if cur, err := st.PushSetting(ctx, SettingVapidPublic); err != nil || string(cur) != string(want) {
		if err := st.PutPushSetting(ctx, SettingVapidPublic, want); err != nil {
			return nil, fmt.Errorf("webpush: store vapid public key: %w", err)
		}
	}
	return v, nil
}

// PublicKey is the uncompressed public key, base64url without padding (the browser's
// applicationServerKey and the k= of the Authorization header).
func (v *VAPID) PublicKey() string { return v.pubB64 }

// PublicBytes is the 65-byte uncompressed public key.
func (v *VAPID) PublicBytes() []byte { return append([]byte(nil), v.public...) }

// Audience is the JWT `aud` of an endpoint: scheme://host[:port].
func Audience(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("webpush: bad endpoint URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

// JWT returns an ES256 token for the audience and subject at time now, from the cache when
// a token for the same audience and subject has more than an hour left.
func (v *VAPID) JWT(audience, subject string, now time.Time) (string, error) {
	key := audience + "\x00" + subject
	v.mu.Lock()
	defer v.mu.Unlock()
	if c, ok := v.cache[key]; ok && now.Before(c.exp.Add(-jwtRenewIn)) && !now.Before(c.exp.Add(-jwtLifetime)) {
		return c.token, nil
	}
	exp := now.Add(jwtLifetime)
	claims := struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub,omitempty"`
	}{audience, exp.Unix(), subject}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + enc.EncodeToString(cb)
	digest := sha256.Sum256([]byte(signing))
	der, err := ecdsa.SignASN1(rand.Reader, v.priv, digest[:])
	if err != nil {
		return "", err
	}
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		return "", err
	}
	raw := make([]byte, 64) // R || S, each left-padded to 32 bytes (not DER)
	sig.R.FillBytes(raw[:32])
	sig.S.FillBytes(raw[32:])
	token := signing + "." + enc.EncodeToString(raw)
	if len(v.cache) > 64 { // audiences are a handful of push services; this is a safety net
		v.cache = map[string]cachedJWT{}
	}
	v.cache[key] = cachedJWT{token: token, exp: exp}
	return token, nil
}

// Authorization is the value of the Authorization header (RFC 8292 section 3).
func (v *VAPID) Authorization(audience, subject string, now time.Time) (string, error) {
	t, err := v.JWT(audience, subject, now)
	if err != nil {
		return "", err
	}
	return "vapid t=" + t + ", k=" + v.pubB64, nil
}
