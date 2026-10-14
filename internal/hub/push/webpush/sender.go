package webpush

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// MaxPayloadJSON is the most JSON the sender encrypts (push.md 6.3); text is cut to fit.
const MaxPayloadJSON = 3000

const (
	maxRetryAfter = time.Hour
	defaultTTL    = 6 * time.Hour
	maxErrBytes   = 200
)

// Options configure a Sender. Only Keys is required.
type Options struct {
	// Keys signs the VAPID JWTs.
	Keys *VAPID
	// Subject is the VAPID sub claim, read on every send (push.md 6.2: vapid_subject, else
	// the hub's public URL). Nil or empty omits the claim.
	Subject func() string
	// Hosts is the endpoint allow-list, read on every send (push.md 6.6). Nil uses
	// model.DefaultWebPushHosts.
	Hosts func() []string
	// Client is the HTTP client. Nil uses NewGuardedClient(). Tests that talk to a fake push
	// service on loopback pass the fake's client together with AllowAnyEndpoint.
	Client *http.Client
	// AllowAnyEndpoint skips the endpoint allow-list. For tests only.
	AllowAnyEndpoint bool
	// Rand supplies the ephemeral key and salt of every message; nil is crypto/rand.
	Rand io.Reader
	// PadMultiple pads records to a multiple of this many bytes; 0 means PadMultiple (128),
	// negative means no padding.
	PadMultiple int
	// Now is the clock for the JWT; nil is time.Now.
	Now func() time.Time
}

// Sender delivers pushes to Web Push subscriptions. It implements model.Sender.
type Sender struct {
	o      Options
	client *http.Client
}

var _ model.Sender = (*Sender)(nil)

// New builds a Sender.
func New(o Options) *Sender {
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PadMultiple == 0 {
		o.PadMultiple = PadMultiple
	}
	c := o.Client
	if c == nil {
		c = NewGuardedClient()
	}
	return &Sender{o: o, client: c}
}

// Kind implements model.Sender.
func (s *Sender) Kind() string { return model.KindWebPush }

// PublicKey is the VAPID public key the browsers subscribe with (applicationServerKey).
func (s *Sender) PublicKey() string { return s.o.Keys.PublicKey() }

// Send implements model.Sender: one POST, no retry. The result follows push.md 5.6.
func (s *Sender) Send(ctx context.Context, t model.Target, m model.Message) model.Result {
	if t.Keys == nil {
		return failed(0, "webpush target has no keys")
	}
	pub, auth, err := ValidateKeys(t.Keys.P256dh, t.Keys.Auth)
	if err != nil {
		return failed(0, "bad subscription keys: "+err.Error())
	}
	if !s.o.AllowAnyEndpoint {
		var hosts []string
		if s.o.Hosts != nil {
			hosts = s.o.Hosts()
		}
		if err := CheckEndpoint(t.Endpoint, hosts); err != nil {
			return failed(0, err.Error())
		}
	}
	aud, err := Audience(t.Endpoint)
	if err != nil {
		return failed(0, err.Error())
	}
	var subject string
	if s.o.Subject != nil {
		subject = s.o.Subject()
	}
	now := s.o.Now()
	authz, err := s.o.Keys.Authorization(aud, subject, now)
	if err != nil {
		return failed(0, "vapid: "+err.Error())
	}
	plain, err := payloadJSON(m.Payload)
	if err != nil {
		return failed(0, "payload: "+err.Error())
	}
	pad := s.o.PadMultiple
	if pad < 0 {
		pad = 0
	}
	body, err := Encrypt(s.o.Rand, plain, pub, auth, pad)
	if err != nil {
		return failed(0, "encrypt: "+err.Error())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(body))
	if err != nil {
		return failed(0, "bad endpoint URL")
	}
	h := req.Header
	h.Set("Authorization", authz)
	h.Set("Content-Encoding", "aes128gcm")
	h.Set("Content-Type", "application/octet-stream")
	ttl := m.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	h.Set("TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	h.Set("Urgency", urgency(m.Urgency))
	if topic := Topic(m.Payload.Tag); topic != "" {
		h.Set("Topic", topic)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return model.Result{Outcome: model.OutcomeRetry, Err: transportError(err)}
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBytes))
	return mapResponse(resp.StatusCode, resp.Header.Get("Retry-After"), reply, s.o.Now())
}

// Topic is the RFC 8030 Topic header for a notification tag: the first 22 characters of
// base64url(SHA-256(tag)); empty for an empty tag.
func Topic(tag string) string {
	if tag == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tag))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:22]
}

func urgency(u string) string {
	switch u {
	case "very-low", "low", "normal", "high":
		return u
	}
	return "normal"
}

// mapResponse is the response table of push.md 5.6 for Web Push.
func mapResponse(status int, retryAfter string, reply []byte, now time.Time) model.Result {
	r := model.Result{HTTPStatus: status}
	switch {
	case status >= 200 && status < 300:
		r.Outcome = model.OutcomeSent
		return r
	case status == 404 || status == 410:
		r.Outcome = model.OutcomeGone
	case status == 429 || status >= 500:
		r.Outcome = model.OutcomeRetry
		r.RetryAfter = parseRetryAfter(retryAfter, now)
	default: // 400, 401, 403, 413 and anything unexpected (3xx, other 4xx)
		r.Outcome = model.OutcomeFailed
	}
	r.Err = strconv.Itoa(status) + " " + cleanReply(reply)
	r.Err = strings.TrimSpace(r.Err)
	return r
}

// parseRetryAfter reads seconds or an HTTP date, capped at one hour; 0 when absent or bad.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0
		}
		if n > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
		if d < 0 {
			d = 0
		}
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

func cleanReply(b []byte) string {
	if len(b) > maxErrBytes {
		b = b[:maxErrBytes]
	}
	s := strings.ToValidUTF8(string(b), "")
	return strings.Map(func(r rune) rune {
		if r < ' ' && r != '\t' {
			return ' '
		}
		return r
	}, s)
}

func failed(status int, msg string) model.Result {
	return model.Result{Outcome: model.OutcomeFailed, HTTPStatus: status, Err: msg}
}

// transportError describes a connection failure without the endpoint URL (which is a
// capability: whoever has it can push to the browser).
func transportError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errBlockedAddr):
		return "refused: push endpoint resolves to a non-public address"
	}
	s := err.Error()
	if len(s) > maxErrBytes {
		s = s[:maxErrBytes]
	}
	return s
}

// payloadJSON marshals the payload and, when it exceeds MaxPayloadJSON, shortens the alert
// list, then cuts the body and
// then the title at rune boundaries (with an ellipsis) until it fits.
func payloadJSON(p model.Payload) ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	// A summary of many alerts lists at most as many ids as fit (the app refetches the
	// list); the first id stays in "alert".
	for len(b) > MaxPayloadJSON && len(p.Alerts) > 1 {
		p.Alerts = p.Alerts[:len(p.Alerts)/2]
		if b, err = json.Marshal(p); err != nil {
			return nil, err
		}
	}
	for _, field := range []*string{&p.Body, &p.Title} {
		for len(b) > MaxPayloadJSON && len(*field) > 0 {
			over := len(b) - MaxPayloadJSON
			n := len(*field) - over - 3
			if n < 0 {
				n = 0
			}
			*field = cutRunes(*field, n)
			if n > 0 {
				*field += "..."
			}
			if b, err = json.Marshal(p); err != nil {
				return nil, err
			}
		}
	}
	if len(b) > MaxPayloadJSON {
		return nil, ErrTooLarge
	}
	return b, nil
}

// cutRunes returns the longest prefix of s of at most n bytes that ends on a rune boundary.
func cutRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
