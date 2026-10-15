package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// Link is the hub's link (internal/hub/link) as the server needs it: the two routes, which
// authenticate themselves with the challenge login (link.md 3), and the revocation close.
type Link interface {
	ServeWS(w http.ResponseWriter, r *http.Request)
	ServePoll(w http.ResponseWriter, r *http.Request)
	// Revoked closes the device's link at once (4403) and publishes the revocation list.
	Revoked(ctx context.Context, device string)
}

func init() {
	Register("GET", "/v1/link", func(s *Server) http.Handler { return s.linkRoute(Link.ServeWS) })
	Register("POST", "/v1/link/poll", func(s *Server) http.Handler { return s.linkRoute(Link.ServePoll) })
}

func (s *Server) linkRoute(serve func(Link, http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Link == nil {
			auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "the link is off on this hub")
			return
		}
		serve(s.cfg.Link, w, r)
	})
}

// linkRevoked tells the link that devices were revoked (nil-safe), and ends their open
// event streams: a revoked board must not keep getting live updates until the next ping.
func (s *Server) linkRevoked(ctx context.Context, ids ...string) {
	s.streams.kick(ids...)
	if s.cfg.Link == nil {
		return
	}
	for _, id := range ids {
		s.cfg.Link.Revoked(ctx, id)
	}
}

// LinkAction applies an action token from a link `action` frame exactly as POST
// /v1/actions/{token} does (link.md 6.2) and returns the alert brief, applied and the HTTP
// status that call would answer. The link applies its own per-device rate limit.
func (s *Server) LinkAction(ctx context.Context, token string) (any, bool, int) {
	as := s.alertService()
	if as == nil {
		return nil, false, http.StatusServiceUnavailable
	}
	b, applied, err := as.Act(ctx, token)
	switch {
	case err == nil:
		return b, applied, http.StatusOK
	case errors.Is(err, model.ErrNotFound):
		return nil, false, http.StatusNotFound
	case errors.Is(err, model.ErrConflict):
		return nil, false, http.StatusConflict
	case errors.Is(err, model.ErrBadToken):
		return nil, false, http.StatusBadRequest
	case errors.Is(err, model.ErrBadMAC):
		return nil, false, http.StatusUnauthorized
	case errors.Is(err, model.ErrExpired):
		return nil, false, http.StatusGone
	}
	return nil, false, http.StatusServiceUnavailable
}

// linkStatusJSON is api.yaml LinkStatus (link.md 9).
type linkStatusJSON struct {
	Connected   bool       `json:"connected"`
	Transport   *string    `json:"transport"`
	ConnectedAt store.Time `json:"connected_at"`
	LastSeenAt  store.Time `json:"last_seen_at"`
	Queued      int64      `json:"queued"`
	AckedSeq    int64      `json:"acked_seq"`
	LastClose   *string    `json:"last_close"`
}

// linkPollFresh: a poll device counts as connected while its last poll is this recent (a
// poll is held up to 25 s, and the next one follows at once).
const linkPollFresh = 90_000

// devLinks returns the link status of every device with link keys (nil on error).
func (s *Server) devLinks(ctx context.Context, keyed map[string]*devKeyJSON) map[string]*linkStatusJSON {
	st, err := s.Store().LinkStatuses(ctx)
	if err != nil {
		return nil
	}
	now := s.now().UnixMilli()
	out := map[string]*linkStatusJSON{}
	for id := range keyed {
		l := &linkStatusJSON{}
		if d, ok := st[id]; ok {
			r := d.State
			l.Queued, l.AckedSeq = d.Queued, r.AckedSeq
			l.Transport, l.LastClose = devStrp(r.Transport), devStrp(r.LastClose)
			l.ConnectedAt, l.LastSeenAt = store.Time(r.ConnectedAt), store.Time(r.LastSeen)
			l.Connected = r.ConnectedAt != 0 && (r.Transport != "poll" || now-r.LastSeen < linkPollFresh)
		}
		out[id] = l
	}
	return out
}

// checkLinkSettings validates PUT /v1/push/channels/link's settings (push.md 13.6): urls (at
// most 4, wss://) and ping_s (60-900).
func checkLinkSettings(raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var set struct {
		URLs  *[]string `json:"urls"`
		PingS *int      `json:"ping_s"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return errors.New("link settings are {urls: [wss://…], ping_s: 60-900}")
	}
	if set.URLs != nil {
		if len(*set.URLs) > 4 {
			return errors.New("settings.urls holds at most 4 URLs")
		}
		for _, u := range *set.URLs {
			if !strings.HasPrefix(u, "wss://") || len(u) > 512 {
				return errors.New("settings.urls must be wss:// URLs")
			}
		}
	}
	if set.PingS != nil && (*set.PingS < LinkPingMin || *set.PingS > LinkPingMax) {
		return errors.New("settings.ping_s must be between 60 and 900")
	}
	return nil
}

// LinkSettings is the link channel's urls and ping_s for `welcome` (push.md 13.6), with
// the defaults of the claim reply.
func (s *Server) LinkSettings(ctx context.Context) (urls []string, pingS int) {
	info := s.linkInfo(ctx, s.cfg.PublicURL)
	return info["urls"].([]string), info["ping_s"].(int)
}
