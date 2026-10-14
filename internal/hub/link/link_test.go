package hublink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/link"
	"github.com/mraj602/switchyard/internal/link/linktest"
)

func TestQueueSeqCollapseAndAck(t *testing.T) {
	f := newFix(t)
	p := f.phone(1)
	a := f.enqueue(p, link.TypeAlert, "sy-s-1", `{"v":1,"n":1}`)
	b := f.enqueue(p, link.TypeAlert, "sy-s-2", `{"v":1,"n":2}`)
	c := f.enqueue(p, link.TypeAlert, "sy-s-1", `{"v":1,"n":3}`) // collapses a
	if a != 1 || b != 2 || c != 3 {
		t.Fatalf("seqs %d %d %d", a, b, c)
	}
	if q := f.queued(p); len(q) != 2 || q[0] != 2 || q[1] != 3 {
		t.Fatalf("queued %v (collapse leaves a gap)", q)
	}
	if _, err := f.h.Enqueue(f.ctx, p.ID, "bogus", "", time.Hour, []byte("{}"), ""); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("unknown type: %v", err)
	}
	if _, err := f.h.Enqueue(f.ctx, p.ID, link.TypeAlert, "bad tag!", time.Hour, []byte("{}"), ""); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("bad tag: %v", err)
	}
	if _, err := f.h.Enqueue(f.ctx, "dev_nobody", link.TypeAlert, "", time.Hour, []byte("{}"), ""); !errors.Is(err, ErrNoLink) {
		t.Fatalf("unknown device: %v", err)
	}

	conn := f.dial(p)
	if conn.Welcome.Device != p.ID || conn.Welcome.Next != 4 || conn.Welcome.PingS != 180 {
		t.Fatalf("welcome %+v", conn.Welcome)
	}
	m := recv(t, conn)
	if m.Frame.Seq != 2 || string(m.Body) != `{"v":1,"n":2}` {
		t.Fatalf("first %d %s", m.Frame.Seq, m.Body)
	}
	m = recv(t, conn)
	if m.Frame.Seq != 3 || string(m.Body) != `{"v":1,"n":3}` {
		t.Fatalf("second %d %s", m.Frame.Seq, m.Body)
	}
	// live: an enqueue while connected arrives at once
	f.enqueue(p, link.TypeRefresh, "refresh", `{"v":1}`)
	if m = recv(t, conn); m.Frame.T != link.TypeRefresh || m.Frame.Seq != 4 {
		t.Fatalf("live %+v", m.Frame)
	}
	if err := conn.Ack(f.ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ack", func() bool { return len(f.queued(p)) == 0 })
	if s := f.state(p); s.AckedSeq != 4 || s.Transport != "ws" || s.ConnectedAt == 0 {
		t.Fatalf("state %+v", s)
	}
}

func TestReplayFromLastAndImplicitAck(t *testing.T) {
	f := newFix(t)
	m := f.machine(2)
	for i := 0; i < 3; i++ {
		f.enqueue(m, link.TypePeerStatus, "", `{"v":1}`)
	}
	c := f.dial(m)
	recv(t, c)
	recv(t, c) // processed 1 and 2, never acked
	c.Close()
	// a fresh connection: hello.last = 2 acks 1 and 2 implicitly; 3 is replayed
	c = f.dial(m)
	if got := recv(t, c); got.Frame.Seq != 3 {
		t.Fatalf("replay starts at %d", got.Frame.Seq)
	}
	waitFor(t, "implicit ack", func() bool { q := f.queued(m); return len(q) == 1 && q[0] == 3 })
	if s := f.state(m); s.AckedSeq != 2 {
		t.Fatalf("acked %d", s.AckedSeq)
	}
}

func TestHubRestartReplays(t *testing.T) {
	f := newFix(t)
	p := f.phone(3)
	f.enqueue(p, link.TypeAlert, "a", `{"v":1,"n":1}`)
	c := f.dial(p)
	recv(t, c)
	f.restart() // drains: notice, then 1001
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	f1, err := c.Next(ctx)
	if err != nil || f1.T != link.TypeNotice {
		t.Fatalf("draining notice: %v %+v", err, f1)
	}
	if code := closed(t, c); code != link.CloseGoingAway {
		t.Fatalf("close %d", code)
	}
	f.enqueue(p, link.TypeAlert, "b", `{"v":1,"n":2}`)
	c = f.dial(p)
	if got := recv(t, c); got.Frame.Seq != 2 || string(got.Body) != `{"v":1,"n":2}` {
		t.Fatalf("after restart: %d %s (no duplicate of 1)", got.Frame.Seq, got.Body)
	}
}

func TestResyncAfterRestoredBackup(t *testing.T) {
	f := newFix(t)
	m := f.machine(4)
	f.enqueue(m, link.TypePeerStatus, "", `{"v":1,"n":1}`)
	f.enqueue(m, link.TypePeerStatus, "", `{"v":1,"n":2}`)
	m.SetLast(41) // the device is ahead: the hub was restored from a backup
	c := f.dial(m)
	a, b := recv(t, c), recv(t, c)
	if a.Frame.Seq != 42 || b.Frame.Seq != 43 || string(b.Body) != `{"v":1,"n":2}` {
		t.Fatalf("renumbered %d %d %s", a.Frame.Seq, b.Frame.Seq, b.Body)
	}
	if s := f.state(m); s.NextSeq != 44 || s.AckedSeq != 41 {
		t.Fatalf("state %+v", s)
	}
	if !strings.Contains(f.logs.String(), "link resync for "+m.ID+": device at 41, hub at 2") {
		t.Fatalf("log: %s", f.logs.String())
	}
}

func TestBadSeqAndNoAckBeforeLogin(t *testing.T) {
	f := newFix(t)
	p := f.phone(5)
	f.enqueue(p, link.TypeAlert, "", `{"v":1}`)
	c := f.dial(p)
	recv(t, c)
	c.Send(f.ctx, map[string]any{"t": "ack", "seq": 7})
	if code := closed(t, c); code != link.CloseBadFrame {
		t.Fatalf("bad_seq close %d", code)
	}
	if q := f.queued(p); len(q) != 1 {
		t.Fatalf("bad ack deleted rows: %v", q)
	}
	// an ack in place of the hello: refused, nothing deleted
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, f.wsURL(), &websocket.DialOptions{Subprotocols: []string{link.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ws.Read(ctx)
	ws.Write(ctx, websocket.MessageText, []byte(`{"t":"ack","seq":1}`))
	_, _, err = ws.Read(ctx)
	if websocket.CloseStatus(err) != link.CloseUnauthorized {
		t.Fatalf("ack before login: %v", err)
	}
	if q := f.queued(p); len(q) != 1 {
		t.Fatalf("pre-login ack deleted rows: %v", q)
	}
}

// A database-only attacker can change rows but has no hub key: every changed frame is
// rejected by the device, its row deleted, and `last` does not move.
func TestTamperedRowsRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    string
	}{
		{"body", `UPDATE link_queue SET body = CAST(body || x'00' AS BLOB) WHERE seq = 1`},
		{"tag", `UPDATE link_queue SET tag = 'other' WHERE seq = 1`},
		{"exp", `UPDATE link_queue SET exp = exp + 1 WHERE seq = 1`},
		{"type", `UPDATE link_queue SET type = 'refresh' WHERE seq = 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFix(t)
			p := f.phone(6)
			f.enqueue(p, link.TypeAlert, "t", `{"v":1}`)
			f.exec(tc.q)
			c := f.dial(p)
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			if _, err := c.Recv(ctx); err == nil {
				t.Fatal("tampered frame accepted")
			}
			if p.Last() != 0 {
				t.Fatalf("last moved to %d", p.Last())
			}
			waitFor(t, "row deleted", func() bool { return len(f.queued(p)) == 0 })
			if f.h.Rejected(p.ID) != 1 || f.state(p).AckedSeq != 0 {
				t.Fatalf("rejected %d, acked %d", f.h.Rejected(p.ID), f.state(p).AckedSeq)
			}
		})
	}
}

func TestForgedHighSeqDoesNotBlock(t *testing.T) {
	f := newFix(t)
	p := f.phone(7)
	f.enqueue(p, link.TypeAlert, "", `{"v":1,"n":1}`)
	f.exec(`INSERT INTO link_queue (device_id, seq, type, tag, body, sig, exp, created_at) VALUES (?, 1000000000, 'alert', '', x'00', zeroblob(64), ?, 0)`,
		p.ID, t0.Add(time.Hour).Unix())
	c := f.dial(p)
	if m := recv(t, c); m.Frame.Seq != 1 {
		t.Fatalf("first %d", m.Frame.Seq)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if m, err := c.Recv(ctx); err == nil || m.Frame.Seq != 1e9 {
		t.Fatalf("forged frame: %v %d", err, m.Frame.Seq)
	}
	f.enqueue(p, link.TypeAlert, "", `{"v":1,"n":2}`)
	if m := recv(t, c); m.Frame.Seq != 2 {
		t.Fatalf("later frame %d", m.Frame.Seq)
	}
	if p.Last() != 2 {
		t.Fatalf("last %d", p.Last())
	}
}

func TestLoginRefusals(t *testing.T) {
	f := newFix(t)
	m := f.machine(8)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	dialRaw := func() (*websocket.Conn, link.Challenge) {
		ws, _, err := websocket.Dial(ctx, f.wsURL(), &websocket.DialOptions{Subprotocols: []string{link.Subprotocol}})
		if err != nil {
			t.Fatal(err)
		}
		_, b, _ := ws.Read(ctx)
		var ch link.Challenge
		json.Unmarshal(b, &ch)
		return ws, ch
	}
	send := func(ws *websocket.Conn, v any) int {
		b, _ := json.Marshal(v)
		ws.Write(ctx, websocket.MessageText, b)
		_, _, err := ws.Read(ctx)
		for err == nil {
			_, _, err = ws.Read(ctx)
		}
		return int(websocket.CloseStatus(err))
	}
	// a used nonce
	ws, ch := dialRaw()
	h, _ := m.Hello(ch)
	b, _ := json.Marshal(h)
	ws.Write(ctx, websocket.MessageText, b)
	ws.Read(ctx) // welcome
	ws2, _ := dialRaw()
	if code := send(ws2, h); code != link.CloseUnauthorized {
		t.Fatalf("replayed hello: %d", code)
	}
	ws.CloseNow()
	// a challenge past exp
	ws, ch = dialRaw()
	f.clk.Add(61 * time.Second)
	h, _ = m.Hello(ch)
	if code := send(ws, h); code != link.CloseUnauthorized {
		t.Fatalf("expired challenge: %d", code)
	}
	// a wrong signature (another key)
	ws, ch = dialRaw()
	other := linktest.NewMachine(99)
	other.ID, other.HubPub, other.HubFP = m.ID, m.HubPub, m.HubFP
	h, _ = other.Hello(ch)
	if code := send(ws, h); code != link.CloseUnauthorized {
		t.Fatalf("wrong key: %d", code)
	}
	// sign_pub changed in the database: the certificate no longer matches its copy
	f.exec(`UPDATE device_key SET sign_pub = ? WHERE device_id = ?`, other.Sign.Public(), m.ID)
	ws, ch = dialRaw()
	h, _ = other.Hello(ch)
	if code := send(ws, h); code != link.CloseUnauthorized {
		t.Fatalf("db-swapped sign key: %d", code)
	}
	// hello.v from the future
	ws, ch = dialRaw()
	h, _ = m.Hello(ch)
	h.V = 2
	if code := send(ws, h); code != link.CloseUpgrade {
		t.Fatalf("v2 hello: %d", code)
	}
	// no subprotocol: 426
	req, _ := http.NewRequest("GET", f.srv.URL+"/v1/link", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("no subprotocol: %v %v", err, resp)
	}
	resp.Body.Close()
}

func TestRateLimitAndFrozen(t *testing.T) {
	f := newFix(t)
	m := f.machine(9)
	for i := 0; i < LoginsPerMinute; i++ {
		f.dial(m).Close()
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	_, err := linktest.Dial(ctx, f.wsURL(), m)
	if linktest.CloseCode(err) != link.CloseRateLimited {
		t.Fatalf("11th login: %v", err)
	}
	f.clk.Add(time.Minute)
	if _, _, err := f.st.Freeze(f.ctx, ""); err != nil {
		t.Fatal(err)
	}
	_, err = linktest.Dial(ctx, f.wsURL(), m)
	if linktest.CloseCode(err) != link.CloseFrozen {
		t.Fatalf("frozen: %v", err)
	}
}

func TestSupersedeIdleAndLimits(t *testing.T) {
	f := newFix(t, func(c *Config) { c.Idle = 300 * time.Millisecond })
	m := f.machine(10)
	a := f.dial(m)
	b := f.dial(m)
	if code := closed(t, a); code != link.CloseSuperseded {
		t.Fatalf("superseded %d", code)
	}
	if code := closed(t, b); code != link.CloseIdle {
		t.Fatalf("idle %d", code)
	}
	waitFor(t, "last_close", func() bool { return f.state(m).LastClose == "4408 idle" })

	c := f.dial(m)
	c.WS.Write(f.ctx, websocket.MessageBinary, []byte("{}"))
	if code := closed(t, c); code != link.CloseBadFrame {
		t.Fatalf("binary %d", code)
	}
	c = f.dial(m)
	c.WS.Write(f.ctx, websocket.MessageText, []byte(`{"t":"x","pad":"`+strings.Repeat("a", link.MaxMessage)+`"}`))
	if code := closed(t, c); code != link.CloseTooBig {
		t.Fatalf("too big %d", code)
	}
}

func TestUnknownAndPeerFrames(t *testing.T) {
	f := newFix(t)
	p := f.phone(11)
	c := f.dial(p)
	c.Send(f.ctx, map[string]any{"t": "future_thing"})
	m := recv(t, c)
	var n link.Notice
	json.Unmarshal(m.Body, &n)
	if m.Frame.T != link.TypeNotice || n.Code != link.NoticeUnknownType {
		t.Fatalf("unknown: %s %+v", m.Frame.T, n)
	}
	c.Send(f.ctx, map[string]any{"t": "peer_msg", "doc": map[string]any{}})
	m = recv(t, c)
	json.Unmarshal(m.Body, &n)
	if n.Code != link.NoticeForbidden {
		t.Fatalf("phone peer_msg: %+v", n)
	}
	// action without an engine: 503 with the id
	c.Send(f.ctx, map[string]any{"t": "action", "id": "act_1", "token": "x"})
	m = recv(t, c)
	var r link.ActionResult
	json.Unmarshal(m.Body, &r)
	if m.Frame.T != link.TypeActionResult || r.ID != "act_1" || r.Status != 503 {
		t.Fatalf("action %+v", r)
	}
}

type relay struct{ got []link.Up }

func (r *relay) Up(_ context.Context, device string, u link.Up) (string, any, error) {
	r.got = append(r.got, u)
	return link.TypePeerAck, map[string]any{"v": 1, "msg": "pm_1", "status": "queued"}, nil
}

func TestPeerRelaySeam(t *testing.T) {
	r := &relay{}
	f := newFix(t, func(c *Config) { c.Peers = r })
	m := f.machine(12)
	c := f.dial(m)
	c.Send(f.ctx, map[string]any{"t": "peer_msg", "doc": map[string]any{"x": 1}})
	got := recv(t, c)
	if got.Frame.T != link.TypePeerAck || len(r.got) != 1 || !strings.Contains(string(r.got[0].Raw), `"x":1`) {
		t.Fatalf("relay: %s %+v", got.Frame.T, r.got)
	}
}

func TestRevokeClosesAndBroadcasts(t *testing.T) {
	f := newFix(t)
	p := f.phone(13)
	m := f.machine(14)
	if err := f.h.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.enqueue(p, link.TypeAlert, "", `{"v":1}`)
	pc := f.dial(p)
	mc := f.dial(m)
	recv(t, pc)
	if err := f.ks.RevokeDevice(f.ctx, p.ID, "test", "revoked"); err != nil {
		t.Fatal(err)
	}
	f.h.Revoked(f.ctx, p.ID)
	if code := closed(t, pc); code != link.CloseRevoked {
		t.Fatalf("revoked close %d", code)
	}
	if q := f.queued(p); len(q) != 0 {
		t.Fatalf("queue kept %v", q)
	}
	got := recv(t, mc)
	if got.Frame.T != link.TypeRevoke {
		t.Fatalf("machine got %s", got.Frame.T)
	}
	var d keys.Doc
	if err := json.Unmarshal(got.Body, &d); err != nil || d.Type != keys.TypeRevocations {
		t.Fatalf("revoke doc: %v %+v", err, d)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if _, err := linktest.Dial(ctx, f.wsURL(), p); linktest.CloseCode(err) != link.CloseRevoked {
		t.Fatalf("revoked login: %v", err)
	}
	// a machine that logs in later with an older `known` gets the list queued
	m2 := f.machine(15)
	m2.Known = &link.Known{}
	c2 := f.dial(m2)
	if got := recv(t, c2); got.Frame.T != link.TypeRevoke {
		t.Fatalf("known behind: %s", got.Frame.T)
	}
}

func TestExpiryAndCaps(t *testing.T) {
	f := newFix(t)
	p := f.phone(16)
	if _, err := f.h.Enqueue(f.ctx, p.ID, link.TypeAlert, "", time.Minute, []byte(`{"v":1}`), "d:1"); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(2 * time.Minute)
	f.h.Sweep(f.ctx)
	if q := f.queued(p); len(q) != 0 {
		t.Fatalf("expired kept %v", q)
	}
	// 500 rows: the 501st alert evicts the oldest and says how many were missed
	for i := 0; i < MaxRows; i++ {
		f.enqueue(p, link.TypeAlert, "", `{"v":1}`)
	}
	f.enqueue(p, link.TypeAlert, "", `{"v":1,"n":"last"}`)
	q := f.queued(p)
	if len(q) != MaxRows || q[0] != 3 {
		t.Fatalf("after eviction %d rows from %d", len(q), q[0])
	}
	m := f.machine(17)
	for i := 0; i < MaxRows; i++ {
		f.enqueue(m, link.TypePeerStatus, "", `{"v":1}`)
	}
	if _, err := f.h.Enqueue(f.ctx, m.ID, link.TypePeerMsg, "", time.Hour, []byte(`{"v":1}`), "pm:x"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("peer_msg over cap: %v", err)
	}
	c := f.dial(p)
	var last linktest.Msg
	for i := 0; i < MaxRows; i++ {
		last = recv(t, c)
		if (i+1)%InFlight == 0 {
			c.Ack(f.ctx)
		}
	}
	if !strings.Contains(string(last.Body), `"missed":1`) || !strings.Contains(string(last.Body), `"n":"last"`) {
		t.Fatalf("last body %s", last.Body)
	}
}

func TestPollParity(t *testing.T) {
	f := newFix(t)
	p := f.phone(18)
	f.enqueue(p, link.TypeAlert, "a", `{"v":1,"n":1}`)
	f.enqueue(p, link.TypeAlert, "b", `{"v":1,"n":2}`)
	f.exec(`UPDATE link_queue SET body = CAST(body || x'00' AS BLOB) WHERE seq = 2`)
	pl := &linktest.Poller{D: p, URL: f.pollURL()}
	r, err := pl.Poll(f.ctx)
	if err != nil || r.Status != 200 || r.Welcome.Device != p.ID || len(r.Frames) != 2 {
		t.Fatalf("poll: %v %+v", err, r)
	}
	if r.Errs[0] != nil || r.Errs[1] == nil || p.Last() != 1 {
		t.Fatalf("checks: %v, last %d", r.Errs, p.Last())
	}
	f.enqueue(p, link.TypeAlert, "c", `{"v":1,"n":3}`)
	// next poll: last=1 acks 1, the reject deletes 2, and 3 arrives with an action result
	up := append(r.Rejects(), map[string]any{"t": "action", "id": "act_9", "token": "t"})
	r, err = pl.Poll(f.ctx, up...)
	if err != nil || len(r.Frames) != 1 || r.Frames[0].Frame.Seq != 3 || len(r.Results) != 1 {
		t.Fatalf("second poll: %v %+v", err, r)
	}
	// held while nothing is queued, answered at the enqueue
	go func() { time.Sleep(100 * time.Millisecond); f.enqueue(p, link.TypeAlert, "d", `{"v":1,"n":4}`) }()
	start := time.Now()
	r, err = pl.Poll(f.ctx)
	if err != nil || len(r.Frames) != 1 || r.Frames[0].Frame.Seq != 4 || time.Since(start) < 80*time.Millisecond {
		t.Fatalf("held poll: %v %+v", err, r)
	}
	// and with nothing queued, it answers empty after PollWait
	r, err = pl.Poll(f.ctx)
	if err != nil || len(r.Frames) != 0 {
		t.Fatalf("empty poll: %v %+v", err, r)
	}
	if q := f.queued(p); len(q) != 0 {
		t.Fatalf("queue %v", q)
	}
	if s := f.state(p); s.AckedSeq != 4 || s.Transport != "poll" || f.h.Rejected(p.ID) != 1 {
		t.Fatalf("state %+v rejected %d", s, f.h.Rejected(p.ID))
	}
	// revoked: 403
	f.ks.RevokeDevice(f.ctx, p.ID, "test", "revoked")
	if r, _ := pl.Poll(f.ctx); r.Status != http.StatusForbidden {
		t.Fatalf("revoked poll %d", r.Status)
	}
}

// The same scripted session over the WebSocket gives the same frames and final state.
func TestWSParity(t *testing.T) {
	f := newFix(t)
	p := f.phone(18)
	f.enqueue(p, link.TypeAlert, "a", `{"v":1,"n":1}`)
	f.enqueue(p, link.TypeAlert, "b", `{"v":1,"n":2}`)
	f.exec(`UPDATE link_queue SET body = CAST(body || x'00' AS BLOB) WHERE seq = 2`)
	c := f.dial(p)
	recv(t, c)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if _, err := c.Recv(ctx); err == nil {
		t.Fatal("tampered accepted")
	}
	f.enqueue(p, link.TypeAlert, "c", `{"v":1,"n":3}`)
	recv(t, c)
	f.enqueue(p, link.TypeAlert, "d", `{"v":1,"n":4}`)
	recv(t, c)
	c.Ack(f.ctx)
	waitFor(t, "ack", func() bool { return len(f.queued(p)) == 0 })
	if s := f.state(p); s.AckedSeq != 4 || f.h.Rejected(p.ID) != 1 {
		t.Fatalf("state %+v", s)
	}
}

func TestDeliveredAtOnAck(t *testing.T) {
	f := newFix(t)
	p := f.phone(19)
	var tid string
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error {
		return tx.QueryRowContext(f.ctx, `SELECT id FROM push_target WHERE kind = 'link' AND device_id = ?`, p.ID).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO alert (id, episode, trigger, status, created_at, updated_at) VALUES ('al_1', 'e1', 'needs_you', 'fired', 0, 0)`)
	f.exec(`INSERT INTO delivery (id, alert_id, target_id, target_kind, fire, status, created_at, updated_at) VALUES (7, 'al_1', ?, 'link', 1, 'sent', 0, 0)`, tid)
	if _, err := f.h.Enqueue(f.ctx, p.ID, link.TypeAlert, "", time.Hour, []byte(`{"v":1}`), store.DeliveryRef([]int64{7})); err != nil {
		t.Fatal(err)
	}
	c := f.dial(p)
	recv(t, c)
	c.Ack(f.ctx)
	waitFor(t, "delivered_at", func() bool {
		var at *int64
		f.st.Write(f.ctx, func(tx *store.Tx) error {
			return tx.QueryRowContext(f.ctx, `SELECT delivered_at FROM delivery WHERE id = 7`).Scan(&at)
		})
		return at != nil && *at > 0
	})
}
