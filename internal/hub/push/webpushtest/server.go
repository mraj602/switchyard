// Package webpushtest is a fake Web Push service for tests (push.md 11.2). It is a TLS
// httptest server that holds the private keys of the subscriptions it hands out, decrypts
// every request with an independent RFC 8291 decoder (decode.go), verifies the VAPID
// header, records what it saw and answers scripted statuses.
//
// A real sender cannot reach it with the default guarded client (loopback, port, host
// allow-list), so tests build the sender with the fake's Client and AllowAnyEndpoint.
package webpushtest

import (
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
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Subscription is what a browser's PushManager.subscribe returns, plus the private half.
type Subscription struct {
	Endpoint string
	P256dh   string // base64url, 65-byte uncompressed point
	Auth     string // base64url, 16 bytes

	priv       *ecdh.PrivateKey
	authSecret []byte
	path       string
}

// Reply is one scripted answer.
type Reply struct {
	Status     int           // 0 means 201
	RetryAfter string        // Retry-After header value
	Body       string        // reply body (a push service's explanation)
	Delay      time.Duration // sleep before answering (for timeout tests)
}

// Received is one request the server saw.
type Received struct {
	Sub       *Subscription
	Header    http.Header
	Plain     []byte // decrypted payload; nil when DecodeErr is set
	Padding   int    // zero bytes after the delimiter
	DecodeErr error
	VapidErr  error // nil when the Authorization header verified
	Claims    map[string]any
	BodyLen   int
}

// Server is the fake push service.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	subs     map[string]*Subscription
	script   []Reply
	got      []Received
	vapidKey string // expected k=, base64url; empty accepts any (still verifies the JWT)
	signal   chan struct{}
}

// New starts the server. Close it when done.
func New() *Server {
	s := &Server{subs: map[string]*Subscription{}, signal: make(chan struct{}, 1024)}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// URL is the server's base URL (https://127.0.0.1:port).
func (s *Server) URL() string { return s.srv.URL }

// Client is an HTTP client that trusts the server's certificate.
func (s *Server) Client() *http.Client {
	c := s.srv.Client()
	c.Timeout = 10 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// ExpectVapidKey makes the server reject requests whose k= is not this base64url key.
func (s *Server) ExpectVapidKey(b64 string) {
	s.mu.Lock()
	s.vapidKey = b64
	s.mu.Unlock()
}

// NewSubscription makes a subscription with fresh keys at a fresh endpoint.
func (s *Server) NewSubscription() *Subscription {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	id := make([]byte, 18)
	_, _ = rand.Read(id)
	path := "/push/" + base64.RawURLEncoding.EncodeToString(id)
	sub := &Subscription{
		Endpoint:   s.srv.URL + path,
		P256dh:     base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:       base64.RawURLEncoding.EncodeToString(auth),
		priv:       priv,
		authSecret: auth,
		path:       path,
	}
	s.mu.Lock()
	s.subs[path] = sub
	s.mu.Unlock()
	return sub
}

// Script queues answers, consumed one per request in order; when the queue is empty the
// server answers 201 Created.
func (s *Server) Script(r ...Reply) {
	s.mu.Lock()
	s.script = append(s.script, r...)
	s.mu.Unlock()
}

// Requests returns a copy of everything received so far.
func (s *Server) Requests() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.got...)
}

// Wait blocks until n requests have been received in total, or the timeout passes.
func (s *Server) Wait(n int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		have := len(s.got)
		s.mu.Unlock()
		if have >= n {
			return true
		}
		select {
		case <-s.signal:
		case <-deadline:
			return false
		}
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sub := s.subs[r.URL.Path]
	var reply Reply
	if len(s.script) > 0 {
		reply, s.script = s.script[0], s.script[1:]
	}
	expectKey := s.vapidKey
	s.mu.Unlock()

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	rec := Received{Sub: sub, Header: r.Header.Clone(), BodyLen: len(body)}
	if r.Method != http.MethodPost {
		rec.VapidErr = errors.New("method is not POST")
	} else {
		rec.Claims, rec.VapidErr = verifyVapid(r.Header.Get("Authorization"), "https://"+r.Host, expectKey)
	}
	if sub == nil {
		rec.DecodeErr = errors.New("unknown subscription path")
	} else if ce := r.Header.Get("Content-Encoding"); ce != "aes128gcm" {
		rec.DecodeErr = fmt.Errorf("Content-Encoding %q", ce)
	} else {
		rec.Plain, rec.Padding, rec.DecodeErr = Decode(body, sub.priv, sub.authSecret)
	}
	s.mu.Lock()
	s.got = append(s.got, rec)
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}

	if reply.Delay > 0 {
		select {
		case <-time.After(reply.Delay):
		case <-r.Context().Done():
			return
		}
	}
	if reply.RetryAfter != "" {
		w.Header().Set("Retry-After", reply.RetryAfter)
	}
	status := reply.Status
	if status == 0 {
		status = http.StatusCreated
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, reply.Body)
}

// verifyVapid checks "vapid t=<jwt>, k=<key>" the way a push service does (RFC 8292): the
// token is a three-part ES256 JWT with a raw 64-byte signature that verifies under k, whose
// aud is the request's origin and whose exp is in the future and at most 24 h away.
func verifyVapid(h, origin, expectKey string) (map[string]any, error) {
	rest, ok := strings.CutPrefix(h, "vapid ")
	if !ok {
		return nil, errors.New("Authorization is not a vapid scheme")
	}
	var token, key string
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "t="); ok {
			token = v
		} else if v, ok := strings.CutPrefix(part, "k="); ok {
			key = v
		}
	}
	if token == "" || key == "" {
		return nil, errors.New("vapid header needs t= and k=")
	}
	if expectKey != "" && key != expectKey {
		return nil, errors.New("k= is not the expected VAPID key")
	}
	kb, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return nil, errors.New("k= is not base64url")
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), kb)
	if err != nil {
		return nil, fmt.Errorf("k= is not a P-256 point: %w", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("JWT does not have three parts")
	}
	hdr, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("JWT header is not base64url")
	}
	var header map[string]string
	if err := json.Unmarshal(hdr, &header); err != nil || header["alg"] != "ES256" || header["typ"] != "JWT" {
		return nil, errors.New(`JWT header must be {"typ":"JWT","alg":"ES256"}`)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("JWT signature must be 64 raw bytes (R||S)")
	}
	der, err := asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.VerifyASN1(pub, digest[:], der) {
		return nil, errors.New("JWT signature does not verify")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("JWT claims are not base64url")
	}
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, errors.New("JWT claims are not JSON")
	}
	if aud, _ := claims["aud"].(string); aud != origin {
		return claims, fmt.Errorf("aud %q is not the origin %q", aud, origin)
	}
	exp, _ := claims["exp"].(float64)
	if left := time.Until(time.Unix(int64(exp), 0)); left <= 0 || left > 24*time.Hour {
		return claims, fmt.Errorf("exp is %v away (must be within 24h)", left)
	}
	return claims, nil
}
