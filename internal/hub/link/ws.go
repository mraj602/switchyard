package hublink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/link"
)

// conn is one device's WebSocket after login.
type conn struct {
	h    *Hub
	ws   *websocket.Conn
	s    *session
	ctx  context.Context
	stop context.CancelFunc

	wmu sync.Mutex // one writer at a time, frames in order

	mu       sync.Mutex
	lastSent int64          // highest seq written on this connection
	sent     map[int64]bool // written and not yet acked or rejected
	gone     map[int64]bool // rejected on this connection: never written again
	acked    int64
	seenAt   time.Time
	kick     chan struct{} // an ack made room in the window

	once   sync.Once
	closed chan struct{}
	word   string // the close, for link_state.last_close
}

// wantsSubprotocol reports whether the request offers sy-link.1.
func wantsSubprotocol(r *http.Request) bool {
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if strings.TrimSpace(p) == link.Subprotocol {
				return true
			}
		}
	}
	return false
}

// ServeWS is GET /v1/link (link.md 2, 3): the upgrade, the challenge login, then the queue.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	if !wantsSubprotocol(r) {
		auth.WriteError(w, http.StatusUpgradeRequired, "upgrade", "subprotocol "+link.Subprotocol+" required")
		return
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		auth.WriteError(w, http.StatusServiceUnavailable, "draining", "the hub is shutting down")
		return
	}
	ip := auth.ClientIP(r)
	h.mu.Lock()
	over := h.loginIP[ip] >= MaxLoginsPerIP
	if !over {
		h.loginIP[ip]++
	}
	h.mu.Unlock()
	if over {
		auth.WriteError(w, http.StatusTooManyRequests, "busy", "too many link logins in progress from this address")
		return
	}
	if h.challenging.Add(1) > MaxChallenging {
		h.challenging.Add(-1)
		h.endLogin(ip)
		auth.WriteError(w, http.StatusServiceUnavailable, "busy", "too many link logins in progress")
		return
	}
	inLogin := true
	endLogin := func() {
		if inLogin {
			inLogin = false
			h.challenging.Add(-1)
			h.endLogin(ip)
		}
	}
	defer endLogin()
	var c *conn
	var seen func()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{link.Subprotocol},
		OnPingReceived: func(context.Context, []byte) bool {
			if seen != nil {
				seen()
			}
			return true
		},
	})
	if err != nil {
		return // Accept wrote the HTTP error
	}
	ws.SetReadLimit(-1) // read() applies link.md's 1 MiB with its own close code
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// login: challenge, then a valid hello within 10 s
	s, err := h.wsLogin(ctx, ws, ip)
	endLogin()
	if err != nil {
		code, reason := link.CloseInternal, "internal"
		var le *loginError
		if errors.As(err, &le) {
			code, reason = le.code, le.reason
		} else {
			h.log.Printf("link login: %v", err)
		}
		_ = ws.Close(websocket.StatusCode(code), reason)
		return
	}
	if h.afterLogin != nil {
		h.afterLogin(s.device)
	}
	c = &conn{h: h, ws: ws, s: s, ctx: ctx, stop: cancel, lastSent: s.acked, sent: map[int64]bool{}, gone: map[int64]bool{}, acked: s.acked, seenAt: h.now(),
		kick: make(chan struct{}, 1), closed: make(chan struct{})}
	idle := h.cfg.Idle
	if idle <= 0 {
		idle = 2*time.Duration(s.pingS)*time.Second + 30*time.Second
	}
	timer := time.AfterFunc(idle, func() { c.close(link.CloseIdle, "idle") })
	defer timer.Stop()
	var smu sync.Mutex
	seen = func() {
		timer.Reset(idle)
		smu.Lock()
		defer smu.Unlock()
		if now := h.now(); now.Sub(c.seenAt) >= time.Minute {
			c.seenAt = now
			_ = h.cfg.Store.LinkSeen(context.Background(), s.device, now.UnixMilli())
		}
	}

	// 6. one connection per device
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		c.close(link.CloseGoingAway, "going_away")
		c.finish()
		return
	}
	old := h.conns[s.device]
	h.conns[s.device] = c
	if p, ok := h.polls[s.device]; ok {
		close(p)
		delete(h.polls, s.device)
	}
	h.mu.Unlock()
	if old != nil {
		go old.close(link.CloseSuperseded, "superseded") // the close handshake waits for the old peer
	}
	defer c.finish()
	if live, err := h.stillLive(ctx, s.device); err != nil || !live {
		if err != nil {
			h.log.Printf("link %s: %v", s.device, err)
			c.close(link.CloseInternal, "internal")
		} else {
			c.close(link.CloseRevoked, "revoked")
		}
		return
	}

	wf, err := h.welcome(ctx, s)
	if err == nil {
		err = c.send(wf)
	}
	if err == nil {
		err = h.sendKnown(ctx, s.device, s.kind, s.hello.Known)
	}
	if err != nil {
		h.log.Printf("link %s: welcome: %v", s.device, err)
		c.close(link.CloseInternal, "internal")
		return
	}
	go c.pump()
	c.readLoop(seen)
}

func (h *Hub) loginTimeout() time.Duration {
	if h.cfg.LoginTimeout > 0 {
		return h.cfg.LoginTimeout
	}
	return LoginTimeout
}

// endLogin frees one of ip's login slots.
func (h *Hub) endLogin(ip string) {
	h.mu.Lock()
	if h.loginIP[ip] <= 1 {
		delete(h.loginIP, ip)
	} else {
		h.loginIP[ip]--
	}
	h.mu.Unlock()
}

// wsLogin sends a challenge and reads the hello (link.md 3.1, 3.2).
func (h *Hub) wsLogin(ctx context.Context, ws *websocket.Conn, ip string) (*session, error) {
	ch, err := h.newChallenge()
	if errors.Is(err, errBusy) {
		return nil, refuse(link.CloseRateLimited, "busy")
	}
	if err != nil {
		return nil, err
	}
	lctx, cancel := context.WithTimeout(ctx, h.loginTimeout())
	defer cancel()
	b, _ := json.Marshal(ch)
	if err := ws.Write(lctx, websocket.MessageText, b); err != nil {
		return nil, refuse(link.CloseUnauthorized, "login_timeout")
	}
	msg, err := readMessage(lctx, ws)
	if err != nil {
		if le := (*loginError)(nil); errors.As(err, &le) {
			return nil, err
		}
		return nil, refuse(link.CloseUnauthorized, "login_timeout")
	}
	var hl link.Hello
	if err := json.Unmarshal(msg, &hl); err != nil {
		return nil, refuse(link.CloseUnauthorized, "bad_hello")
	}
	return h.login(ctx, hl, "ws", ip)
}

// readMessage reads one text message of at most 1 MiB (link.md 2).
func readMessage(ctx context.Context, ws *websocket.Conn) ([]byte, error) {
	typ, rd, err := ws.Reader(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, refuse(link.CloseBadFrame, "binary")
	}
	b, err := io.ReadAll(io.LimitReader(rd, link.MaxMessage+1))
	if err != nil {
		return nil, err
	}
	if len(b) > link.MaxMessage {
		return nil, refuse(link.CloseTooBig, "too_big")
	}
	return b, nil
}

// send writes one frame.
func (c *conn) send(f link.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.Write(c.ctx, websocket.MessageText, b)
}

// pump sends the queued rows past the ack that this connection has not sent, keeping at most
// InFlight unacked (link.md 5.6), and waits for an enqueue, an ack or a reject when there is
// nothing to send or no room. Rows go out in seq order; a row a DB writer inserted with a
// high seq takes one slot until the device rejects it, and never hides the rows below it.
func (c *conn) pump() {
	h, dev := c.h, c.s.device
	for {
		wake := h.waitChan(dev) // before the read, so an enqueue after it is not missed
		c.mu.Lock()
		after, inflight := c.acked, len(c.sent)
		c.mu.Unlock()
		sentAny := false
		if inflight < InFlight {
			rows, err := h.cfg.Store.LinkPending(c.ctx, dev, after, h.now().Unix(), InFlight+inflight)
			if err != nil {
				if c.ctx.Err() == nil {
					h.log.Printf("link %s: reading the queue: %v", dev, err)
					c.close(link.CloseInternal, "internal")
				}
				return
			}
			for _, r := range rows {
				c.mu.Lock()
				skip, full := c.sent[r.Seq] || c.gone[r.Seq], len(c.sent) >= InFlight
				c.mu.Unlock()
				if full {
					break
				}
				if skip {
					continue
				}
				c.mu.Lock() // before the write: the device may ack it before Write returns
				c.sent[r.Seq] = true
				c.lastSent = max(c.lastSent, r.Seq)
				c.mu.Unlock()
				if err := c.send(frameOf(r)); err != nil {
					return
				}
				sentAny = true
			}
		}
		if sentAny {
			continue
		}
		select {
		case <-c.ctx.Done():
			return
		case <-wake:
		case <-c.kick:
		}
	}
}

func (c *conn) wakePump() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// readLoop handles the device's frames until the connection ends.
func (c *conn) readLoop(seen func()) {
	h, s := c.h, c.s
	for {
		msg, err := readMessage(c.ctx, c.ws)
		if err != nil {
			var le *loginError
			if errors.As(err, &le) {
				c.close(le.code, le.reason)
			} else {
				c.noteClose(err)
			}
			return
		}
		seen()
		u, err := link.ParseUp(msg)
		if err != nil {
			c.close(link.CloseBadFrame, "bad_frame")
			return
		}
		switch u.T {
		case link.TypeAck:
			c.mu.Lock()
			bad := u.Seq > c.lastSent
			c.mu.Unlock()
			if bad {
				c.close(link.CloseBadFrame, "bad_seq")
				return
			}
			if err := h.cfg.Store.Write(c.ctx, func(tx *store.Tx) error { return h.ackTx(tx, s.device, u.Seq) }); err != nil {
				if c.ctx.Err() == nil {
					h.log.Printf("link %s: ack: %v", s.device, err)
					c.close(link.CloseInternal, "internal")
				}
				return
			}
			c.mu.Lock()
			c.acked = max(c.acked, u.Seq)
			for q := range c.sent {
				if q <= c.acked {
					delete(c.sent, q)
				}
			}
			c.mu.Unlock()
			c.wakePump()
		case link.TypeReject:
			c.mu.Lock()
			sent := c.sent[u.Seq]
			if sent {
				delete(c.sent, u.Seq)
				c.gone[u.Seq] = true // a read of the queue racing the delete must not resend it
			}
			c.mu.Unlock()
			if sent {
				h.reject(c.ctx, s.device, u.Seq)
				c.wakePump()
			}
		case link.TypeHello:
			c.close(link.CloseBadFrame, "bad_frame")
			return
		default:
			f, err := h.up(c.ctx, s, u)
			if err != nil {
				if c.ctx.Err() == nil {
					h.log.Printf("link %s: %s: %v", s.device, u.T, err)
					c.close(link.CloseInternal, "internal")
				}
				return
			}
			if f != nil {
				if err := c.send(*f); err != nil {
					return
				}
			}
		}
	}
}

// noteClose records how the device ended the connection.
func (c *conn) noteClose(err error) {
	c.once.Do(func() {
		code := websocket.CloseStatus(err)
		if code == -1 {
			c.word = "closed"
		} else {
			c.word = closeText(int(code))
		}
		close(c.closed)
	})
}

func closeText(code int) string {
	return strconv.Itoa(code) + " " + link.CloseWord(code)
}

// close ends the connection with a link.md 8 code. It waits for the close handshake (the
// library bounds it at 5 s).
func (c *conn) close(code int, reason string) {
	first := false
	c.once.Do(func() {
		first = true
		c.word = closeText(code)
		close(c.closed)
	})
	if !first {
		return
	}
	_ = c.ws.Close(websocket.StatusCode(code), reason)
	c.stop()
}

// drain tells the device the hub is going away, then closes with 1001.
func (c *conn) drain() {
	if f, err := c.h.ephemeral(c.s, link.TypeNotice, link.Notice{V: 1, Code: link.NoticeDraining,
		Message: "the hub is shutting down; reconnect in 5-15 s"}); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		b, _ := json.Marshal(f)
		c.wmu.Lock()
		_ = c.ws.Write(ctx, websocket.MessageText, b)
		c.wmu.Unlock()
		cancel()
	}
	c.close(link.CloseGoingAway, "going_away")
}

// finish forgets the connection and records its close, unless a newer connection of the
// device has taken its place.
func (c *conn) finish() {
	c.stop()
	_ = c.ws.CloseNow()
	h := c.h
	h.mu.Lock()
	current := h.conns[c.s.device] == c
	if current {
		delete(h.conns, c.s.device)
	}
	h.mu.Unlock()
	c.once.Do(func() { c.word = "closed"; close(c.closed) })
	h.log.Printf("link close %s: %s", c.s.device, c.word)
	if !current {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		return tx.LinkClosed(c.s.device, c.word, tx.NowMS())
	})
}
