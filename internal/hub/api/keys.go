package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
)

// Key endpoints (keys.md 3.3, 4.2, 6; api.yaml tag keys). Every handler answers 503 when
// the server has no hub signing key (Config.Keys nil: tests and hubs without a key).

func init() {
	reg := func(method, path string, h func(*Server, http.ResponseWriter, *http.Request)) {
		Register(method, path, func(s *Server) http.Handler {
			pairWire(s)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if s.cfg.Keys == nil {
					auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "this hub has no signing key; restart `sy hub serve`")
					return
				}
				h(s, w, r)
			})
		})
	}
	reg("GET", "/v1/keys", (*Server).keysGet)
	reg("POST", "/v1/keys/owner", (*Server).keysPropose)
	reg("POST", "/v1/keys/owner/confirm", (*Server).keysConfirm)
	reg("GET", "/v1/keys/revocations", (*Server).keysRevocationsGet)
	reg("POST", "/v1/keys/revocations", (*Server).keysRevocationsPost)
	reg("PUT", "/v1/devices/{id}/keys", (*Server).keysPutDevice)
	reg("GET", "/v1/devices/{id}/cert", (*Server).keysGetCert)
	reg("POST", "/v1/devices/{id}/cert", (*Server).keysCertify)
}

// ownerKeyJSON is api.yaml OwnerKey.
type ownerKeyJSON struct {
	FP          string   `json:"fp"`
	Pub         string   `json:"pub"`
	State       string   `json:"state"`
	ProposedBy  string   `json:"proposed_by,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	ConfirmedAt *string  `json:"confirmed_at"`
	Doc         keys.Doc `json:"doc"`
}

func msTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

func ownerKeyOut(o hubkeys.OwnerKeyInfo) ownerKeyJSON {
	v := ownerKeyJSON{FP: o.FP, Pub: keys.B64(o.Pub), State: o.State, ProposedBy: o.ProposedBy, Doc: o.Doc}
	if o.CreatedAt != 0 {
		v.CreatedAt = msTime(o.CreatedAt)
	}
	if o.ConfirmedAt != 0 {
		t := msTime(o.ConfirmedAt)
		v.ConfirmedAt = &t
	}
	return v
}

// keysFail maps a hubkeys error to its api.yaml answer.
func keysFail(w http.ResponseWriter, err error) {
	var ke *hubkeys.Error
	switch {
	case errors.As(err, &ke):
		auth.WriteError(w, ke.Status, ke.Code, ke.Msg)
	case errors.Is(err, store.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such device, or it has no keys")
	case errors.Is(err, hubkeys.ErrBadCert):
		auth.WriteError(w, http.StatusNotFound, "not_found", "the stored certificate does not verify; register the keys again")
	case errors.Is(err, store.ErrFrozen):
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "hub is frozen for a move")
	default:
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
	}
}

// keysDecode reads a small JSON body strictly into v; it writes the 400 itself.
func keysDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "the body must be a JSON object of this endpoint's shape")
		return false
	}
	return true
}

func (s *Server) keysGet(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	k := s.cfg.Keys
	out := map[string]any{"hub_key": k.Key.Public(), "owner_key": nil}
	o, err := k.ActiveOwner(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	if o != nil {
		out["owner_key"] = ownerKeyOut(*o)
	}
	if caller.HasScope("owner") {
		p, err := k.PendingOwners(r.Context())
		if err != nil {
			keysFail(w, err)
			return
		}
		list := []ownerKeyJSON{}
		for _, x := range p {
			list = append(list, ownerKeyOut(x))
		}
		out["pending"] = list
	}
	rv := 0
	if rl, err := k.Revocations(r.Context()); err != nil {
		keysFail(w, err)
		return
	} else if rl != nil {
		rv = rl.Body.Version
	}
	out["revocations_version"] = rv
	// For `sy keys certify` on the owner machine (a write token): the live devices with keys
	// and whether the owner signed them. Viewer tokens do not get it.
	if caller.HasScope("owner") || caller.HasScope("write") {
		list, err := s.keysDevices(r.Context())
		if err != nil {
			keysFail(w, err)
			return
		}
		out["devices"] = list
	}
	writeJSON(w, http.StatusOK, out)
}

// keyDeviceJSON is one entry of KeysInfo.devices.
type keyDeviceJSON struct {
	ID   string     `json:"id"`
	Kind string     `json:"kind"`
	Name string     `json:"name"`
	Keys devKeyJSON `json:"keys"`
}

func (s *Server) keysDevices(ctx context.Context) ([]keyDeviceJSON, error) {
	certs, err := s.cfg.Keys.Certs(ctx)
	if err != nil {
		return nil, err
	}
	live, err := s.Store().Devices(ctx, false)
	if err != nil {
		return nil, err
	}
	out := []keyDeviceJSON{}
	for _, d := range live {
		i, ok := certs[d.ID]
		if !ok || d.RevokedAt != 0 {
			continue
		}
		out = append(out, keyDeviceJSON{ID: d.ID, Kind: i.Kind, Name: i.Name, Keys: devKeyJSON{SignFP: i.SignFP, BoxFP: i.BoxFP,
			Reg: i.Reg, OwnerSigned: i.OwnerSigned, CertifiedAt: store.Time(i.CertifiedAt)}})
	}
	return out, nil
}

func (s *Server) keysPropose(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	var body struct {
		Doc keys.Doc `json:"doc"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	o, err := s.cfg.Keys.Propose(r.Context(), body.Doc, caller.ID)
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ownerKeyOut(o))
}

func (s *Server) keysConfirm(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	var body struct {
		FP string `json:"fp"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	fp, ok := keys.NormalizeFP(body.FP)
	if !ok {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "fp is 24 base32 characters")
		return
	}
	o, err := s.cfg.Keys.Confirm(r.Context(), fp, caller.ID)
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ownerKeyOut(o))
}

func (s *Server) keysRevocationsGet(w http.ResponseWriter, r *http.Request) {
	rl, err := s.cfg.Keys.Revocations(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	if rl == nil {
		writeJSON(w, http.StatusOK, map[string]any{"doc": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc": rl.Doc})
}

func (s *Server) keysRevocationsPost(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	var body struct {
		Doc keys.Doc `json:"doc"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	ids, err := s.cfg.Keys.AcceptOwnerRevocations(r.Context(), body.Doc, caller.ID)
	if err != nil {
		keysFail(w, err)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	s.linkRevoked(r.Context(), ids...)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": ids})
}

func (s *Server) keysPutDevice(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	id := r.PathValue("id")
	if id == "self" {
		id = caller.ID
	}
	if id != caller.ID {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "a device registers only its own keys (self)")
		return
	}
	var k keys.DeviceKeysNew
	if !keysDecode(w, r, &k) {
		return
	}
	d, err := s.cfg.Keys.RegisterKeys(r.Context(), id, k)
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cert": d})
}

func (s *Server) keysGetCert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "self" {
		caller, _ := auth.DeviceFrom(r.Context())
		id = caller.ID
	}
	c, err := s.cfg.Keys.Cert(r.Context(), id)
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cert": c.Doc, "owner_signed": c.OwnerSigned})
}

func (s *Server) keysCertify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FP  string `json:"fp"`
		Sig string `json:"sig"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	if err := s.cfg.Keys.Certify(r.Context(), r.PathValue("id"), body.FP, body.Sig); err != nil {
		keysFail(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// LinkDefaults are the link channel's defaults (push.md 13.6).
const (
	LinkPingDefault = 180
	LinkPingMin     = 60
	LinkPingMax     = 900
)

// linkInfo is the claim reply's link object: the link channel's urls and ping_s settings
// (push.md 13.6), defaulting to wss://<hub.public_url host>/v1/link (or the code's hub
// address) and 180 s.
func (s *Server) linkInfo(ctx context.Context, fallbackOrigin string) map[string]any {
	var set struct {
		URLs  []string `json:"urls"`
		PingS int      `json:"ping_s"`
	}
	if ch, err := s.Store().ChannelByKind(ctx, "link"); err == nil && len(ch.Settings) > 0 {
		_ = json.Unmarshal(ch.Settings, &set)
	}
	if len(set.URLs) == 0 {
		origin := hubPublicURL(ctx, s)
		if origin == "" {
			origin = fallbackOrigin
		}
		if u, err := url.Parse(origin); err == nil && u.Host != "" {
			scheme := "wss"
			if u.Scheme == "http" {
				scheme = "ws"
			}
			set.URLs = []string{scheme + "://" + u.Host + "/v1/link"}
		}
	}
	if set.URLs == nil {
		set.URLs = []string{}
	}
	if set.PingS < LinkPingMin || set.PingS > LinkPingMax {
		set.PingS = LinkPingDefault
	}
	return map[string]any{"urls": set.URLs, "ping_s": set.PingS}
}

// claimKeys adds keys.md 7.3's fields to a claim reply.
func (s *Server) claimKeys(ctx context.Context, reply map[string]any, cert *keys.Doc, reg, hubURL string) {
	k := s.cfg.Keys
	if k == nil {
		return
	}
	reply["hub_key"] = k.Key.Public()
	reply["owner_key"] = nil
	if o, err := k.ActiveOwner(ctx); err == nil && o != nil {
		reply["owner_key"] = o.Doc
	}
	reply["link"] = s.linkInfo(ctx, hubURL)
	if cert != nil {
		reply["cert"] = *cert
		reply["reg"] = reg
	}
}
