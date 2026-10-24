package hublink

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/link"
)

// PollRequest is the body of POST /v1/link/poll (link.md 2.1).
type PollRequest struct {
	Op    string            `json:"op"`
	Hello *link.Hello       `json:"hello,omitempty"`
	Up    []json.RawMessage `json:"up,omitempty"`
}

// PollReply is the reply to op poll. op challenge answers the challenge frame itself.
type PollReply struct {
	Welcome link.Frame     `json:"welcome"`
	Frames  []link.Frame   `json:"frames"`
	Results []link.Frame   `json:"results"`
	Next    link.Challenge `json:"next"`
}

// pollStatus maps a refused login to the HTTP status of the poll (the phone stops on 403
// and 426, and backs off on the rest).
func pollStatus(code int) int {
	switch code {
	case link.CloseRevoked:
		return http.StatusForbidden
	case link.CloseUpgrade:
		return http.StatusUpgradeRequired
	case link.CloseRateLimited:
		return http.StatusTooManyRequests
	case link.CloseFrozen:
		return http.StatusServiceUnavailable
	case link.CloseBadFrame:
		return http.StatusBadRequest
	}
	return http.StatusUnauthorized
}

// ServePoll is POST /v1/link/poll (link.md 2.1): the same login and frames over plain HTTPS.
func (h *Hub) ServePoll(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, link.MaxMessage+1))
	if err != nil || len(b) > link.MaxMessage {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body unreadable or over 1 MiB")
		return
	}
	var req PollRequest
	if err := json.Unmarshal(b, &req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "malformed JSON")
		return
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		auth.WriteError(w, http.StatusServiceUnavailable, "draining", "the hub is shutting down")
		return
	}
	switch {
	case req.Op == "challenge":
		ch, err := h.newChallenge()
		if errors.Is(err, errBusy) {
			auth.WriteError(w, http.StatusServiceUnavailable, "busy", "too many logins in progress")
			return
		}
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "internal", "challenge")
			return
		}
		writeJSON(w, ch)
		return
	case req.Op != "poll" || req.Hello == nil:
		auth.WriteError(w, http.StatusBadRequest, "bad_request", `op is "challenge" or "poll" with a hello`)
		return
	case len(req.Up) > MaxUp:
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "at most 64 frames in up")
		return
	}
	ups := make([]link.Up, 0, len(req.Up))
	for _, raw := range req.Up {
		u, err := link.ParseUp(raw)
		if err != nil || u.T == link.TypeAck || u.T == link.TypeHello {
			auth.WriteError(w, http.StatusBadRequest, "bad_frame", "up holds device frames other than hello and ack")
			return
		}
		ups = append(ups, u)
	}
	ctx := r.Context()
	s, err := h.login(ctx, *req.Hello, "poll", auth.ClientIP(r))
	if err != nil {
		var le *loginError
		if errors.As(err, &le) {
			auth.WriteError(w, pollStatus(le.code), le.reason, link.CloseWord(le.code))
		} else {
			h.log.Printf("link poll login: %v", err)
			auth.WriteError(w, http.StatusInternalServerError, "internal", "login")
		}
		return
	}
	if h.afterLogin != nil {
		h.afterLogin(s.device)
	}
	// 6. one connection per device: a poll replaces a WebSocket and an older poll
	cancel := make(chan struct{})
	h.mu.Lock()
	old := h.conns[s.device]
	delete(h.conns, s.device)
	if p, ok := h.polls[s.device]; ok {
		close(p)
	}
	h.polls[s.device] = cancel
	sent := h.pollSent[s.device]
	h.mu.Unlock()
	if old != nil {
		go old.close(link.CloseSuperseded, "superseded") // the close handshake waits for the old peer
	}
	defer func() {
		h.mu.Lock()
		if h.polls[s.device] == cancel {
			delete(h.polls, s.device)
		}
		h.mu.Unlock()
	}()
	if live, err := h.stillLive(ctx, s.device); err != nil {
		h.log.Printf("link poll %s: %v", s.device, err)
		auth.WriteError(w, http.StatusInternalServerError, "internal", "login")
		return
	} else if !live {
		auth.WriteError(w, pollStatus(link.CloseRevoked), "revoked", link.CloseWord(link.CloseRevoked))
		return
	}

	reply := PollReply{Frames: []link.Frame{}, Results: []link.Frame{}}
	if reply.Welcome, err = h.welcome(ctx, s); err != nil {
		h.log.Printf("link poll %s: welcome: %v", s.device, err)
		auth.WriteError(w, http.StatusInternalServerError, "internal", "welcome")
		return
	}
	for _, u := range ups {
		if u.T == link.TypeReject {
			if u.Seq > s.acked && u.Seq <= sent {
				h.reject(ctx, s.device, u.Seq)
			}
			continue
		}
		f, err := h.up(ctx, s, u)
		if err != nil {
			h.log.Printf("link poll %s: %s: %v", s.device, u.T, err)
			auth.WriteError(w, http.StatusInternalServerError, "internal", u.T)
			return
		}
		if f != nil {
			reply.Results = append(reply.Results, *f)
		}
	}
	if err := h.sendKnown(ctx, s.device, s.kind, s.hello.Known); err != nil {
		h.log.Printf("link poll %s: documents: %v", s.device, err)
	}

	// hold the request while nothing is queued past the ack (and nothing else to answer)
	timer := time.NewTimer(h.cfg.PollWait)
	defer timer.Stop()
	for {
		wake := h.waitChan(s.device)
		rows, err := h.cfg.Store.LinkPending(ctx, s.device, s.acked, h.now().Unix(), InFlight)
		if err != nil {
			if ctx.Err() == nil {
				h.log.Printf("link poll %s: reading the queue: %v", s.device, err)
				auth.WriteError(w, http.StatusInternalServerError, "internal", "queue")
			}
			return
		}
		if len(rows) > 0 || len(reply.Results) > 0 {
			for _, row := range rows {
				reply.Frames = append(reply.Frames, frameOf(row))
			}
			break
		}
		done := false
		select {
		case <-wake:
		case <-cancel:
			done = true
		case <-timer.C:
			done = true
		case <-ctx.Done():
			return
		}
		if done {
			break
		}
	}
	if n := len(reply.Frames); n > 0 {
		h.mu.Lock()
		h.pollSent[s.device] = max(h.pollSent[s.device], reply.Frames[n-1].Seq)
		h.mu.Unlock()
	}
	if reply.Next, err = h.newChallenge(); err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "internal", "challenge")
		return
	}
	// link_state keeps connected_at: a poll device counts as connected while it polls
	_ = h.cfg.Store.LinkSeen(ctx, s.device, h.now().UnixMilli())
	writeJSON(w, reply)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
