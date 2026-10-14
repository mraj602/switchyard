package hublink

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/link"
)

// loginError is a refused login: the close code of link.md 8 and a reason word.
type loginError struct {
	code   int
	reason string
}

func (e *loginError) Error() string { return fmt.Sprintf("%d %s", e.code, e.reason) }

func refuse(code int, reason string) error { return &loginError{code, reason} }

// session is a logged-in device.
type session struct {
	device, kind string
	cert         keys.Cert
	nonce        string
	hello        link.Hello
	pingS        int
	acked, next  int64
}

// newChallenge makes a challenge with a fresh single-use nonce (link.md 3.1).
// errBusy: MaxNonces challenges are outstanding.
var errBusy = errors.New("too many outstanding challenges")

func (h *Hub) newChallenge() (link.Challenge, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return link.Challenge{}, err
	}
	nonce := keys.B64(b)
	exp := h.now().Add(ChallengeTTL).Unix()
	h.mu.Lock()
	if len(h.nonces) >= MaxNonces { // a flood of challenges: forget the expired ones now
		now := h.now().Unix()
		for n, e := range h.nonces {
			if e < now {
				delete(h.nonces, n)
			}
		}
	}
	if len(h.nonces) >= MaxNonces { // op challenge needs no credential: bound what it can pin
		h.mu.Unlock()
		return link.Challenge{}, errBusy
	}
	h.nonces[nonce] = exp
	h.mu.Unlock()
	return link.NewChallenge(h.key, h.key.FP, nonce, exp)
}

// login runs link.md 3.2's steps 1-5 and 7-8 for a hello (step 6, closing the device's other
// connection, is the caller's). transport is "ws" or "poll".
func (h *Hub) login(ctx context.Context, hl link.Hello, transport, ip string) (*session, error) {
	// 1. the nonce: known, unused, not past exp, and the hello's exp is the challenge's
	h.mu.Lock()
	exp, ok := h.nonces[hl.Nonce]
	delete(h.nonces, hl.Nonce)
	h.mu.Unlock()
	if hl.T != link.TypeHello {
		return nil, refuse(link.CloseUnauthorized, "bad_hello")
	}
	if hl.V != link.Version {
		return nil, refuse(link.CloseUpgrade, "upgrade")
	}
	if !ok || h.now().Unix() > exp || hl.Exp != exp {
		return nil, refuse(link.CloseUnauthorized, "nonce")
	}
	// 2. the device, not revoked, with keys; 3. its certificate verifies (keys from it only)
	var s session
	err := func() error {
		d, err := h.cfg.Store.DeviceDetailByID(ctx, hl.Device)
		if errors.Is(err, store.ErrNotFound) {
			return refuse(link.CloseUnauthorized, "unknown_device")
		}
		if err != nil {
			return err
		}
		if d.RevokedAt != 0 {
			return refuse(link.CloseRevoked, "revoked")
		}
		s.kind = d.Kind
		s.cert, err = h.cfg.Keys.Cert(ctx, hl.Device)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return refuse(link.CloseUnauthorized, "no_keys")
		case errors.Is(err, hubkeys.ErrBadCert):
			h.log.Printf("link: warning: device_key row for %s does not match its certificate", hl.Device)
			return refuse(link.CloseUnauthorized, "bad_cert")
		case err != nil:
			return err
		}
		if s.cert.Body.Device != hl.Device || s.cert.Body.Hub != h.key.FP {
			h.log.Printf("link: warning: device_key row for %s does not match its certificate", hl.Device)
			return refuse(link.CloseUnauthorized, "bad_cert")
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	// 4. the signature, with the certificate's sign key
	if err := link.VerifyHello(hl, h.key.FP, s.cert.Body.Sign.Alg, s.cert.Body.SignPub()); err != nil {
		return nil, refuse(link.CloseUnauthorized, "signature")
	}
	// 5. rate
	h.mu.Lock()
	var allowed bool
	if transport == "poll" {
		allowed = h.pollLim.take(hl.Device, h.now(), PollsPerMinute, time.Minute)
	} else {
		allowed = h.logins.take(hl.Device, h.now(), LoginsPerMinute, time.Minute)
	}
	h.mu.Unlock()
	if !allowed {
		return nil, refuse(link.CloseRateLimited, "rate_limited")
	}
	if fr, err := h.cfg.Store.Frozen(ctx); err == nil && fr != nil {
		return nil, refuse(link.CloseFrozen, "frozen")
	}
	s.device, s.nonce, s.hello = hl.Device, hl.Nonce, hl
	set := h.settings(ctx)
	s.pingS = set.PingS
	if p := hl.Client.PingS; p >= 60 && p <= 900 && p > s.pingS {
		s.pingS = p
	}
	// 7. resync after a restored backup, or 8. the signed `last` as a cumulative ack
	helloJSON, _ := json.Marshal(map[string]any{"client": hl.Client, "known": hl.Known})
	err = h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.LinkConnected(hl.Device, transport, ip, string(helloJSON), tx.NowMS()); err != nil {
			return err
		}
		st, err := tx.LinkState(hl.Device)
		if err != nil {
			return err
		}
		if hl.Last >= st.NextSeq {
			if err := h.resyncTx(tx, hl.Device, hl.Last, st.NextSeq); err != nil {
				return err
			}
		} else if err := h.ackTx(tx, hl.Device, hl.Last); err != nil {
			return err
		}
		st, err = tx.LinkState(hl.Device)
		s.acked, s.next = st.AckedSeq, st.NextSeq
		return err
	})
	if err != nil {
		return nil, err
	}
	h.log.Printf("link login %s (%s) via %s", hl.Device, s.kind, transport)
	return &s, nil
}

// resyncTx renumbers a device's queue after its last (link.md 3.2 step 7): the device is
// ahead of a hub restored from a backup. The sealed bodies do not change; each row is signed
// again for its new seq. Nothing queued is lost.
func (h *Hub) resyncTx(tx *store.Tx, device string, last, next int64) error {
	rows, err := tx.LinkQueued(device)
	if err != nil {
		return err
	}
	for i := range rows {
		r := &rows[i]
		r.Seq = last + 1 + int64(i)
		if r.Sig, err = h.key.SignLink(link.FrameInput(device, r.Type, r.Seq, r.Exp, r.Tag, link.CtxQueue, r.Body)); err != nil {
			return err
		}
	}
	h.log.Printf("link: warning: link resync for %s: device at %d, hub at %d", device, last, next-1)
	return tx.LinkReplace(device, rows, last+1+int64(len(rows)), last)
}

// welcome is the welcome frame of a session (link.md 3.3), signed with its nonce.
func (h *Hub) welcome(ctx context.Context, s *session) (link.Frame, error) {
	set := h.settings(ctx)
	w := link.Welcome{V: link.Version, Device: s.device, Next: s.next, PingS: set.PingS, URLs: set.URLs,
		ServerTime: keys.FormatTime(h.now())}
	if o, err := h.cfg.Keys.ActiveOwner(ctx); err == nil && o != nil {
		d := o.Doc
		w.Owner = &d
	}
	return h.ephemeral(s, link.TypeWelcome, w)
}

// ephemeral signs an ephemeral frame for the session's nonce (link.md 4.3).
func (h *Hub) ephemeral(s *session, t string, body any) (link.Frame, error) {
	return link.EphemeralFrame(h.key, s.device, t, s.nonce, h.now().Add(ChallengeTTL).Unix(), body)
}

// frameOf is a queued row as a frame.
func frameOf(r store.LinkRow) link.Frame {
	return link.Frame{T: r.Type, Seq: r.Seq, Exp: r.Exp, Tag: r.Tag, Body: keys.B64(r.Body), Sig: keys.B64(r.Sig)}
}

// up handles one device→hub frame other than ack and reject (link.md 6.2) and returns the
// ephemeral reply, if any.
func (h *Hub) up(ctx context.Context, s *session, u link.Up) (*link.Frame, error) {
	var t string
	var body any
	switch u.T {
	case link.TypeAction:
		t, body = link.TypeActionResult, h.action(ctx, s.device, u)
	case link.TypePeerMsg, link.TypePeerResult, link.TypePeerLog, link.TypePeersList:
		switch {
		case !isMachine(s.kind):
			t, body = link.TypeNotice, link.Notice{V: 1, Code: link.NoticeForbidden, Message: "peer frames come from machines"}
		case h.cfg.Peers == nil:
			t, body = link.TypeNotice, link.Notice{V: 1, Code: link.NoticeUnknownType, Message: "this hub does not relay peer messages yet"}
		default:
			rt, rb, err := h.cfg.Peers.Up(ctx, s.device, u)
			if err != nil {
				return nil, err
			}
			if rt == "" {
				return nil, nil
			}
			t, body = rt, rb
		}
	default:
		t, body = link.TypeNotice, link.Notice{V: 1, Code: link.NoticeUnknownType, Message: "unknown frame type " + u.T}
	}
	f, err := h.ephemeral(s, t, body)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// action applies an action token as POST /v1/actions/{token} (link.md 6.2), with the same
// limit of 30 a minute, per device.
func (h *Hub) action(ctx context.Context, device string, u link.Up) link.ActionResult {
	r := link.ActionResult{V: 1, ID: u.ID}
	h.mu.Lock()
	ok := h.actLim.take(device, h.now(), 30, time.Minute)
	h.mu.Unlock()
	switch {
	case !ok:
		r.Status = 429
	case h.cfg.Action == nil:
		r.Status = 503
	default:
		r.Alert, r.Applied, r.Status = h.cfg.Action(ctx, u.Token)
	}
	return r
}
