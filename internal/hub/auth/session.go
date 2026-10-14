package auth

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
)

// SessionMaxAge is how long the board's cookie lives (api.yaml /v1/session/login).
const SessionMaxAge = 30 * 24 * time.Hour

// ClaimedCookieAge is the Max-Age of a claimed phone's cookie: the longest Chrome keeps one
// (pairing.md 3.2).
const ClaimedCookieAge = 400 * 24 * time.Hour

// SecureRequest reports whether the request reached the hub over TLS, directly or through
// a loopback proxy that says so.
func SecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
	}
	return false
}

// SessionCookie is the sy_session cookie: HttpOnly, SameSite=Strict, Path=/, Secure when
// the request came over https (a Secure cookie would be dropped on plain http).
func SessionCookie(r *http.Request, token string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: SecureRequest(r), MaxAge: int(maxAge.Seconds())}
}

// ClearCookie clears the sy_session cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// IsSessionDevice reports whether d is a cookie session that logout revokes: made by a
// login or an owner link (not a claimed phone, which is unpaired from the Devices list).
func IsSessionDevice(d store.Device) bool {
	return strings.HasPrefix(d.CreatedBy, "login:") || IsOwnerSession(d)
}

// LoginHandler is POST /v1/session/login with {"token": "..."}. A valid token holding the
// read scope is exchanged for a new browser device (scopes ["read"] only, created_by
// "login:<device id>") whose own token goes into the HttpOnly, SameSite=Strict cookie
// sy_session. The pasted token is never stored in the browser, the cookie can only read
// (even when a desktop token was pasted), and revoking the pasted device revokes the
// session too. Unknown or revoked token: 401; no read scope: 403; malformed body: 400.
func (a *Authenticator) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if a.tooManyFails(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many failed logins; wait a minute")
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Token) == "" {
			WriteError(w, http.StatusBadRequest, "bad_request", `body must be {"token": "..."}`)
			return
		}
		d, err := a.Lookup(r.Context(), strings.TrimSpace(body.Token))
		if err != nil {
			a.fail(clientIP(r))
			WriteError(w, http.StatusUnauthorized, "unauthorized", "unknown or revoked token")
			return
		}
		if !d.HasScope("read") {
			WriteError(w, http.StatusForbidden, "forbidden", "this token cannot read (machine tokens only upload)")
			return
		}
		c, err := Create(r.Context(), a.Store, CreateRequest{Kind: "browser", Name: "board via " + d.Name,
			CreatedBy: "login:" + d.ID, Parent: d.ID, Scopes: []string{"read"}})
		if errors.Is(err, ErrUnauthorized) {
			a.fail(clientIP(r))
			WriteError(w, http.StatusUnauthorized, "unauthorized", "unknown or revoked token")
			return
		}
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, "unavailable", "could not create a session")
			return
		}
		http.SetCookie(w, SessionCookie(r, c.Token, SessionMaxAge))
		w.WriteHeader(http.StatusNoContent)
	})
}

// LogoutHandler is POST /v1/session/logout: it revokes the browser session behind the
// cookie (the pasted token stays valid) and clears the cookie.
func (a *Authenticator) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(CookieName); err == nil {
			if d, err := a.Lookup(r.Context(), c.Value); err == nil && IsSessionDevice(d) {
				_ = Revoke(r.Context(), a.Store, d.ID)
			}
		}
		ClearCookie(w)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})
}
