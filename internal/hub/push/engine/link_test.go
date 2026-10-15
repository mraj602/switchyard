package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	hublink "github.com/mraj602/switchyard/internal/hub/link"
	"github.com/mraj602/switchyard/internal/hub/push/engine"
	pushlink "github.com/mraj602/switchyard/internal/hub/push/link"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/webpush"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/link"
	"github.com/mraj602/switchyard/internal/link/linktest"
)

// The link channel end to end (push.md 13): the engine fires an alert, the link sender
// queues it, test phones receive it over /v1/link, ack it (delivered_at), and Seen on one
// phone, or the alert resolving, dismisses it on every phone.

type linkRig struct {
	*harness
	lk  *hublink.Hub
	ks  *hubkeys.Service
	srv *httptest.Server
}

func newLinkRig(t *testing.T) *linkRig {
	h := newHarness(t, noTarget(), noStart())
	k, _ := hubkeys.FromSeed(bytes.Repeat([]byte{1}, 32))
	r := &linkRig{harness: h, ks: hubkeys.New(h.st, k)}
	r.lk = hublink.New(hublink.Config{Store: h.st, Keys: r.ks, Now: h.clk.Now})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/link", r.lk.ServeWS)
	r.srv = httptest.NewServer(mux)
	t.Cleanup(func() { r.lk.Close(); r.srv.Close() })
	h.eng = engine.New(engine.Config{Store: h.st, Clock: h.clk, Senders: []model.Sender{pushlink.New(r.lk)}, Broker: h.broker,
		SweepEvery: h.sweep, PublicURL: "https://hub.example.ts.net", Link: r.lk})
	if err := h.eng.Start(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.eng.Sync()
	return r
}

// phone pairs a test phone: device, keys, certificate and link target.
func (r *linkRig) phone(seed byte, name string) *linktest.Device {
	d := linktest.NewPhone(seed)
	c, err := auth.Create(r.ctx, r.st, auth.CreateRequest{Kind: "phone", Name: name})
	if err != nil {
		r.t.Fatal(err)
	}
	d.ID = c.Device.ID
	r.write(func(tx *store.Tx) error { _, err := r.ks.RegisterTx(tx, c.Device, d.KeysNew(), keys.RegQR); return err })
	d.Pin(r.ks.Key.Pub)
	return d
}

func (r *linkRig) dial(d *linktest.Device) *linktest.Conn {
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()
	c, err := linktest.Dial(ctx, linktest.WSURL(r.srv.URL), d)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(c.Close)
	return c
}

type linkBody struct {
	model.Payload
	Missed int `json:"missed"`
}

func recvBody(t *testing.T, c *linktest.Conn) (linktest.Msg, linkBody) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := c.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	var b linkBody
	if err := json.Unmarshal(m.Body, &b); err != nil {
		t.Fatalf("body %s: %v", m.Body, err)
	}
	return m, b
}

func TestLinkAlertAckAndSeenDismissesOnOtherPhone(t *testing.T) {
	r := newLinkRig(t)
	a, b := r.phone(1, "pixel-a"), r.phone(2, "pixel-b")
	ca, cb := r.dial(a), r.dial(b)
	r.advanceTo(500 * time.Millisecond)
	r.askQ(s1, 0)
	r.advanceTo(60 * time.Second)

	ma, pa := recvBody(t, ca)
	_, pb := recvBody(t, cb)
	al := r.alertOf(s1, model.TriggerAsked)
	if ma.Frame.T != link.TypeAlert || pa.Type != model.TypeAlert || pa.Alert != al.ID || pb.Alert != al.ID {
		t.Fatalf("alert frames %s %+v %+v", ma.Frame.T, pa, pb)
	}
	// the payload is the Web Push payload plus origin
	if pa.Origin != "https://hub.example.ts.net" || pa.Actions == nil || pa.Title == "" {
		t.Fatalf("payload %+v", pa)
	}
	want := pa.Payload
	want.Origin = ""
	wp, _ := webpush.PayloadJSON(want)
	var plain map[string]any
	json.Unmarshal(ma.Body, &plain)
	delete(plain, "origin")
	got, _ := json.Marshal(plain)
	var wpm map[string]any
	json.Unmarshal(wp, &wpm)
	wpj, _ := json.Marshal(wpm)
	if string(got) != string(wpj) {
		t.Fatalf("link payload\n%s\nwebpush\n%s", got, wpj)
	}
	ds := r.deliveries(al.ID)
	if len(ds) != 2 || ds[0].Status != "sent" {
		t.Fatalf("deliveries %+v", ds)
	}

	// delivered_at only on ack
	delivered := func() int {
		var n int
		r.write(func(tx *store.Tx) error {
			return tx.QueryRowContext(r.ctx, `SELECT count(*) FROM delivery WHERE alert_id = ? AND delivered_at IS NOT NULL`, al.ID).Scan(&n)
		})
		return n
	}
	if delivered() != 0 {
		t.Fatal("delivered before ack")
	}
	ca.Ack(r.ctx)
	waitUntil(t, func() bool { return delivered() == 1 })

	// Seen on phone a: both phones get a dismiss with the alert's tag
	if _, err := r.eng.Seen(r.ctx, al.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*linktest.Conn{ca, cb} {
		m, d := recvBody(t, c)
		if d.Type != model.TypeDismiss || d.Tag != pa.Tag || m.Frame.Tag != ma.Frame.Tag || len(d.Alerts) != 1 || d.Alerts[0] != al.ID {
			t.Fatalf("dismiss %+v (frame tag %q)", d, m.Frame.Tag)
		}
	}
}

func TestLinkDismissCollapsesUndeliveredAlert(t *testing.T) {
	r := newLinkRig(t)
	p := r.phone(3, "pixel-c")
	r.advanceTo(500 * time.Millisecond)
	r.askQ(s1, 0)
	r.advanceTo(60 * time.Second)
	r.answer(s1, 70*time.Second) // resolved while the phone was away
	r.advanceTo(71 * time.Second)
	c := r.dial(p)
	m, d := recvBody(t, c)
	if d.Type != model.TypeDismiss || m.Frame.Seq != 2 {
		t.Fatalf("first frame after reconnect: seq %d %+v", m.Frame.Seq, d)
	}
	// the alert's row was collapsed: its delivery says so and is not a miss
	var lastErr string
	r.write(func(tx *store.Tx) error {
		return tx.QueryRowContext(r.ctx, `SELECT coalesce(last_error, '') FROM delivery WHERE target_kind = 'link'`).Scan(&lastErr)
	})
	if lastErr != "collapsed" {
		t.Fatalf("delivery last_error %q", lastErr)
	}
}

func TestLinkSenderOutcomes(t *testing.T) {
	r := newLinkRig(t)
	s := pushlink.New(r.lk)
	m := model.Message{Payload: model.Payload{V: 1, Type: model.TypeTest, Title: "t", Tag: "sy-test"}}
	if res := s.Send(r.ctx, model.Target{Kind: model.KindLink, DeviceID: "dev_gone"}, m); res.Outcome != model.OutcomeGone {
		t.Fatalf("no device: %+v", res)
	}
	p := r.phone(4, "pixel-d")
	if res := s.Send(r.ctx, model.Target{Kind: model.KindLink, DeviceID: p.ID}, m); res.Outcome != model.OutcomeSent {
		t.Fatalf("sent: %+v", res)
	}
	// a peer_msg-full queue is not this sender's; the hub-wide cap answers retry
	r.exec(`UPDATE device SET revoked_at = 1 WHERE id = ?`, p.ID)
	if res := s.Send(r.ctx, model.Target{Kind: model.KindLink, DeviceID: p.ID}, m); res.Outcome != model.OutcomeGone {
		t.Fatalf("revoked: %+v", res)
	}
	full := pushlink.New(fullQueue{})
	if res := full.Send(r.ctx, model.Target{Kind: model.KindLink, DeviceID: "x"}, m); res.Outcome != model.OutcomeRetry {
		t.Fatalf("full: %+v", res)
	}
}

type fullQueue struct{}

func (fullQueue) Enqueue(context.Context, string, string, string, time.Duration, []byte, string) (int64, error) {
	return 0, hublink.ErrQueueFull
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
