package auth

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
)

// CookieName is the browser session cookie of the web board.
const CookieName = "sy_session"

// ErrUnauthorized means the token is missing, unknown or revoked.
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator checks bearer tokens and browser cookies against the device table. There
// is no cache: a revoked token fails on its next request (tokens.md 4).
type Authenticator struct {
	Store *store.Store
	Now   func() time.Time

	// PublicURL, when set, returns the hub's configured public URL; its host is accepted as
	// an Origin for cookie requests besides the request's own Host (pairing.md 3.2).
	PublicURL func(ctx context.Context) string

	mu        sync.Mutex
	touched   map[string]time.Time
	refreshed map[string]time.Time // device id -> last cookie refresh
	fails     []time.Time
	ipFails   map[string][]time.Time
}

// New returns an Authenticator for s.
func New(s *store.Store) *Authenticator {
	return &Authenticator{Store: s, Now: time.Now, touched: map[string]time.Time{}}
}

// Lookup returns the live device a token belongs to: not revoked and not expired.
func (a *Authenticator) Lookup(ctx context.Context, token string) (store.Device, error) {
	d, err := a.LookupDetail(ctx, token)
	return d.Device, err
}

// LookupDetail is Lookup with the device's expiry and prefs.
func (a *Authenticator) LookupDetail(ctx context.Context, token string) (store.DeviceDetail, error) {
	if !LooksLikeToken(token) {
		return store.DeviceDetail{}, ErrUnauthorized
	}
	d, err := a.Store.DeviceDetailByHash(ctx, Hash(token))
	if errors.Is(err, store.ErrNotFound) || (err == nil && d.RevokedAt != 0) {
		return store.DeviceDetail{}, ErrUnauthorized
	}
	// A device with expires_at (owner sessions) is revoked from that instant; board
	// sessions (login:) also expire SessionMaxAge after creation, whatever the cookie says.
	if err == nil && a.expired(d) {
		return store.DeviceDetail{}, ErrUnauthorized
	}
	return d, err
}

// Alive reports whether a device that authenticated earlier still may use its token:
// not revoked and not expired. A database error counts as alive (a long-lived stream
// should survive a blip; the next request re-checks anyway).
func (a *Authenticator) Alive(ctx context.Context, d store.Device) bool {
	cur, err := a.Store.DeviceDetailByID(ctx, d.ID)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		return true
	}
	return cur.RevokedAt == 0 && !a.expired(cur)
}

func (a *Authenticator) expired(d store.DeviceDetail) bool {
	now := a.Now().UnixMilli()
	if d.ExpiresAt != 0 && d.ExpiresAt <= now {
		return true
	}
	return strings.HasPrefix(d.CreatedBy, "login:") && now-d.CreatedAt > SessionMaxAge.Milliseconds()
}

// touch updates last_used_at at most once a minute per device.
func (a *Authenticator) touch(ctx context.Context, d store.Device, ip string) {
	now := a.Now()
	a.mu.Lock()
	last, ok := a.touched[d.ID]
	if ok && now.Sub(last) < touchEvery {
		a.mu.Unlock()
		return
	}
	a.touched[d.ID] = now
	a.mu.Unlock()
	_ = a.Store.TouchDevice(ctx, d.ID, now.UnixMilli(), ip)
}

type ctxKey struct{}

// WithDevice stores the authenticated device in a context.
func WithDevice(ctx context.Context, d store.Device) context.Context {
	return context.WithValue(ctx, ctxKey{}, d)
}

// DeviceFrom returns the device of an authenticated request.
func DeviceFrom(ctx context.Context) (store.Device, bool) {
	d, ok := ctx.Value(ctxKey{}).(store.Device)
	return d, ok
}

// WriteError writes the api.yaml Error body.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

// Options for Require.
type Options struct {
	// QueryToken allows ?token= (only /v1/stream: EventSource cannot set headers).
	QueryToken bool
}

type tokenSource int

const (
	srcBearer tokenSource = iota
	srcQuery
	srcCookie
)

type cookieKey struct{}

// ViaCookie reports whether the request authenticated with the sy_session cookie.
func ViaCookie(ctx context.Context) bool {
	v, _ := ctx.Value(cookieKey{}).(bool)
	return v
}

// IsOwnerSession reports whether d is an owner session made by `sy hub owner-link`.
func IsOwnerSession(d store.Device) bool { return strings.HasPrefix(d.CreatedBy, "owner-link:") }

// Require authenticates the request (bearer header, ?token= when allowed, else the
// board's cookie) and checks that the device holds at least one of scopes. Unknown,
// revoked or expired tokens get 401, a missing scope 403 (tokens.md 3). A request that
// authenticated by cookie and changes state (any method but GET, HEAD, OPTIONS) must send
// a JSON content type and, when it has an Origin, one that is the hub's own (pairing.md
// 3.2): 403 otherwise. Bearer requests are not affected.
func (a *Authenticator) Require(opt Options, scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := bearer(r)
			src := srcBearer
			if tok == "" && opt.QueryToken {
				tok = r.URL.Query().Get("token")
				src = srcQuery
			}
			if tok == "" {
				if c, err := r.Cookie(CookieName); err == nil {
					tok = c.Value
					src = srcCookie
				}
			}
			if tok == "" {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "missing token")
				return
			}
			d, err := a.LookupDetail(r.Context(), tok)
			if err != nil {
				if errors.Is(err, ErrUnauthorized) {
					WriteError(w, http.StatusUnauthorized, "unauthorized", "unknown or revoked token")
				} else {
					WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
				}
				return
			}
			ok := len(scopes) == 0
			for _, s := range scopes {
				if d.HasScope(s) {
					ok = true
				}
			}
			if !ok {
				WriteError(w, http.StatusForbidden, "forbidden", "this token cannot do that")
				return
			}
			if src == srcCookie {
				if !a.CookieRequestOK(r) {
					WriteError(w, http.StatusForbidden, "forbidden", "cookie requests that change state must be same-origin JSON")
					return
				}
				a.refreshCookie(w, r, d.Device, tok)
				r = r.WithContext(context.WithValue(r.Context(), cookieKey{}, true))
			}
			a.touch(r.Context(), d.Device, ClientIP(r))
			next.ServeHTTP(w, r.WithContext(WithDevice(r.Context(), d.Device)))
		})
	}
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// CookieRequestOK applies the CSRF rules of cookie requests (pairing.md 3.2): a request
// that changes state needs Content-Type application/json, and an Origin header, when
// present, must name the hub's own host. SameSite=Strict already blocks cross-site sends;
// these are defence in depth.
func (a *Authenticator) CookieRequestOK(r *http.Request) bool {
	if !unsafeMethod(r.Method) {
		return true
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return false
	}
	return a.SameOrigin(r)
}

// SameOrigin reports whether the request's Origin header (if any) is the hub's own: the
// request's Host, or the host of the configured public URL (a proxy may rewrite Host).
// The scheme is not compared: a proxy may or may not say it terminated TLS.
func (a *Authenticator) SameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false // includes the literal "null"
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if a.PublicURL != nil {
		if p, err := url.Parse(a.PublicURL(r.Context())); err == nil && p.Host != "" && strings.EqualFold(u.Host, p.Host) {
			return true
		}
	}
	return false
}

// CookieRefreshEvery is how often an in-use claimed cookie is re-set with a fresh Max-Age.
const CookieRefreshEvery = 24 * time.Hour

// refreshCookie re-sets the long-lived cookie of a claimed device (created_by "pair:")
// at most once a day, so an app in use never loses it (pairing.md 3.2).
func (a *Authenticator) refreshCookie(w http.ResponseWriter, r *http.Request, d store.Device, token string) {
	if !strings.HasPrefix(d.CreatedBy, "pair:") {
		return
	}
	now := a.Now()
	a.mu.Lock()
	last, ok := a.refreshed[d.ID]
	if ok && now.Sub(last) < CookieRefreshEvery {
		a.mu.Unlock()
		return
	}
	if a.refreshed == nil {
		a.refreshed = map[string]time.Time{}
	}
	a.refreshed[d.ID] = now
	a.mu.Unlock()
	http.SetCookie(w, SessionCookie(r, token, ClaimedCookieAge))
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// ClientIP is the caller's address. The hub listens on loopback behind tailscale serve or
// Caddy, so RemoteAddr is the proxy; when it is a loopback address and X-Forwarded-For is
// present, the client is the right-most entry (the one the proxy added; earlier entries
// are whatever the caller claimed). Otherwise it is RemoteAddr (pairing.md 3.3).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := strings.Join(r.Header.Values("X-Forwarded-For"), ","); xff != "" {
			parts := strings.Split(xff, ",")
			last := strings.TrimSpace(parts[len(parts)-1])
			if p := net.ParseIP(last); p != nil {
				return p.String()
			}
		}
	}
	return host
}

func clientIP(r *http.Request) string { return ClientIP(r) }

func (a *Authenticator) fail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.Now()
	a.fails = append(a.fails, now)
	if a.ipFails == nil {
		a.ipFails = map[string][]time.Time{}
	}
	a.ipFails[ip] = append(a.ipFails[ip], now)
}

// Failed logins allowed per minute: from one address, and hub-wide.
const (
	maxFailsPerIP = 5
	maxFailsTotal = 10
)

// tooManyFails reports whether ip (or the hub as a whole) has failed too many logins in
// the last minute.
func (a *Authenticator) tooManyFails(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cut := a.Now().Add(-time.Minute)
	for k, ts := range a.ipFails {
		keep := ts[:0]
		for _, t := range ts {
			if t.After(cut) {
				keep = append(keep, t)
			}
		}
		if len(keep) == 0 {
			delete(a.ipFails, k)
		} else {
			a.ipFails[k] = keep
		}
	}
	if len(a.ipFails[ip]) >= maxFailsPerIP {
		return true
	}
	keep := a.fails[:0]
	for _, t := range a.fails {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	a.fails = keep
	return len(a.fails) >= maxFailsTotal
}
