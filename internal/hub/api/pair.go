package api

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/version"
)

// PairPepperPath is where the pairing HMAC key lives; empty means <state>/pair_pepper
// (pair.DefaultPepperPath). Set it before building a Server (tests, embedding).
var PairPepperPath string

var (
	pairSvcMu sync.Mutex
	pairSvcs  = map[*Server]*pair.Service{}
)

// pairService returns the pairing service of s: one per server, so the rate limiter is
// shared by /v1/pair/claim and /v1/session/owner.
func pairService(s *Server) *pair.Service {
	pairSvcMu.Lock()
	defer pairSvcMu.Unlock()
	if svc, ok := pairSvcs[s]; ok {
		return svc
	}
	svc := pair.NewService(s.Store(), PairPepperPath)
	svc.Log = s.Config().Log
	pairSvcs[s] = svc
	return svc
}

// hubPublicURL is the configured public URL: --public-url, else the hub.public_url setting.
func hubPublicURL(ctx context.Context, s *Server) string {
	if u := s.Config().PublicURL; u != "" {
		return u
	}
	v, _, _ := s.Store().PairSetting(ctx, "hub.public_url")
	return v
}

// pairWire connects the authenticator to the hub's public URL (for the Origin check of
// cookie requests). Every factory of this file and devices.go calls it.
func pairWire(s *Server) {
	s.Auth.PublicURL = func(ctx context.Context) string { return hubPublicURL(ctx, s) }
}

// pairRequire wires the authenticator and returns h; server.go wraps registered handlers
// that have x-scopes in Auth.Require itself, so the scopes argument is documentation.
func pairRequire(s *Server, h http.HandlerFunc, scopes ...string) http.Handler {
	_ = scopes
	pairWire(s)
	return h
}

func init() {
	Register("GET", "/v1/me", func(s *Server) http.Handler {
		return pairRequire(s, s.pairMe, "read", "write", "push", "owner")
	})
	Register("POST", "/v1/session/owner", func(s *Server) http.Handler {
		pairWire(s)
		return http.HandlerFunc(s.pairOwnerSignIn)
	})
	Register("POST", "/v1/pair/codes", func(s *Server) http.Handler { return pairRequire(s, s.pairMakeCode, "owner") })
	Register("GET", "/v1/pair/codes/{id}", func(s *Server) http.Handler { return pairRequire(s, s.pairGetCode, "owner") })
	Register("DELETE", "/v1/pair/codes/{id}", func(s *Server) http.Handler { return pairRequire(s, s.pairCancelCode, "owner") })
	Register("POST", "/v1/pair/claim", func(s *Server) http.Handler {
		pairWire(s)
		return http.HandlerFunc(s.pairClaim)
	})
}

// pairDecode reads a small JSON body into v; it writes the 400 itself.
func pairDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "the body must be a JSON object")
		return false
	}
	return true
}

// pairSessionCheck is the CSRF rule of the unauthenticated cookie-setting endpoints: when
// the request comes from a browser (it has an Origin) or asks for a cookie, it must send a
// JSON content type and a same-origin Origin (pairing.md 3.2). It writes the 403 itself.
func pairSessionCheck(s *Server, w http.ResponseWriter, r *http.Request, cookie bool) bool {
	if !cookie && r.Header.Get("Origin") == "" {
		return true
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" || !s.Auth.SameOrigin(r) {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "cookie requests must be same-origin JSON")
		return false
	}
	return true
}

// pairFail maps a pair-service error to its api.yaml answer.
func pairFail(w http.ResponseWriter, err error) {
	var le *pair.LimitError
	var ce *pair.ClaimError
	switch {
	case errors.As(err, &le):
		w.Header().Set("Retry-After", strconv.Itoa(int(le.RetryAfter.Seconds())+1))
		auth.WriteError(w, http.StatusTooManyRequests, "rate_limited", le.Error())
	case errors.Is(err, pair.ErrInvalid):
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired code")
	case errors.As(err, &ce):
		auth.WriteError(w, ce.Status, ce.Code, ce.Message)
	case errors.Is(err, pair.ErrNoURL):
		auth.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, store.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such pairing code")
	case errors.Is(err, store.ErrFrozen):
		w.Header().Set("Retry-After", strconv.Itoa(ingest.FrozenRetryAfter))
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "hub is frozen for a move")
	default:
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "could not complete that; see the hub log")
	}
}

// pairDropSession revokes the session behind the request's cookie when it is a board or
// owner session: a new cookie replaces it, as on logout.
func pairDropSession(r *http.Request, s *Server) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		if d, err := s.Auth.Lookup(r.Context(), c.Value); err == nil && auth.IsSessionDevice(d) {
			_ = auth.Revoke(r.Context(), s.Store(), d.ID)
		}
	}
}

func (s *Server) pairClaim(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Secret     string `json:"secret"`
		Code       string `json:"code"`
		Name       string `json:"name"`
		Machine    string `json:"machine"`
		ExpectKind string `json:"expect_kind"`
		Cookie     bool   `json:"cookie"`
		Client     string `json:"client"`
	}
	if !pairDecode(w, r, &body) {
		return
	}
	if !pairSessionCheck(s, w, r, body.Cookie) {
		return
	}
	res, err := pairService(s).Claim(r.Context(), auth.ClientIP(r), pair.ClaimRequest{Secret: body.Secret, Code: body.Code,
		Name: body.Name, Machine: body.Machine, ExpectKind: body.ExpectKind, Cookie: body.Cookie, Client: body.Client})
	if err != nil {
		pairFail(w, err)
		return
	}
	reply := map[string]any{"device": res.Device.ID, "kind": res.Device.Kind, "name": res.Device.Name,
		"scopes": res.Device.Scopes, "hub": map[string]string{"url": res.HubURL, "version": version.Version}}
	if res.Machine != "" {
		reply["machine"] = res.Machine
	}
	if body.Cookie {
		pairDropSession(r, s)
		http.SetCookie(w, auth.SessionCookie(r, res.Token, auth.ClaimedCookieAge))
	} else {
		reply["token"] = res.Token
	}
	writeJSON(w, http.StatusOK, reply)
}

func (s *Server) pairOwnerSignIn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Secret string `json:"secret"`
		Name   string `json:"name"`
	}
	if !pairDecode(w, r, &body) {
		return
	}
	if !pairSessionCheck(s, w, r, true) {
		return
	}
	c, err := pairService(s).OwnerSignIn(r.Context(), auth.ClientIP(r), body.Secret, body.Name)
	if err != nil {
		pairFail(w, err)
		return
	}
	pairDropSession(r, s)
	http.SetCookie(w, auth.SessionCookie(r, c.Token, auth.OwnerSessionAge))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pairMakeCode(w http.ResponseWriter, r *http.Request) {
	dev, _ := auth.DeviceFrom(r.Context())
	var body struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Machine string `json:"machine"`
		URL     string `json:"url"`
		TTLS    int    `json:"ttl_s"`
	}
	if !pairDecode(w, r, &body) {
		return
	}
	hub, err := pair.ResolveURL(body.URL, hubPublicURL(r.Context(), s), pair.RequestOrigin(r))
	if err != nil {
		pairFail(w, err)
		return
	}
	if body.TTLS < 0 || (body.TTLS != 0 && (time.Duration(body.TTLS)*time.Second < pair.MinTTL || time.Duration(body.TTLS)*time.Second > pair.MaxTTL)) {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "ttl_s must be between 60 and 3600")
		return
	}
	c, err := pairService(s).Create(r.Context(), pair.CreateRequest{Kind: body.Kind, Name: body.Name, Machine: body.Machine, URL: hub,
		TTL: time.Duration(body.TTLS) * time.Second, CreatedBy: "owner:" + dev.ID})
	if err != nil {
		pairFail(w, err)
		return
	}
	var name, command any
	if c.Name != "" {
		name = c.Name
	}
	if c.Command != "" {
		command = c.Command
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"id": c.ID, "kind": c.Kind, "name": name, "code": c.Code, "secret": c.Secret,
		"url": c.URL, "expires_at": store.Time(c.ExpiresAt.UnixMilli()), "command": command, "qr": c.QR})
}

func (s *Server) pairGetCode(w http.ResponseWriter, r *http.Request) {
	st, err := pairService(s).Status(r.Context(), r.PathValue("id"))
	if err != nil {
		pairFail(w, err)
		return
	}
	var dev any
	if st.Device != nil {
		dev = map[string]string{"id": st.Device.ID, "name": st.Device.Name, "kind": st.Device.Kind}
	}
	var claimed any
	if !st.ClaimedAt.IsZero() {
		claimed = store.Time(st.ClaimedAt.UnixMilli())
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": st.ID, "kind": st.Kind, "status": st.State,
		"expires_at": store.Time(st.ExpiresAt.UnixMilli()), "claimed_at": claimed, "device": dev})
}

func (s *Server) pairCancelCode(w http.ResponseWriter, r *http.Request) {
	if err := pairService(s).Cancel(r.Context(), r.PathValue("id")); err != nil {
		pairFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pairMe(w http.ResponseWriter, r *http.Request) {
	dev, _ := auth.DeviceFrom(r.Context())
	det, err := s.Store().DeviceDetailByID(r.Context(), dev.ID)
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	hub := map[string]any{"version": version.Version, "public_url": nil}
	if u := hubPublicURL(r.Context(), s); u != "" {
		hub["public_url"] = u
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": devView(det, dev.ID, nil), "owner": dev.HasScope("owner"), "hub": hub})
}
