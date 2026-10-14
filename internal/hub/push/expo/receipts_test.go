package expo_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/clock"
	"github.com/mraj602/switchyard/internal/hub/push/expo"
	"github.com/mraj602/switchyard/internal/hub/push/expotest"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
)

type rig struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	clk *clock.Fake
	f   *expotest.Server
	s   *expo.Sender
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clk := clock.NewFake(gNow)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	f := expotest.New()
	t.Cleanup(func() { f.Close(); st.Close() })
	r := &rig{t: t, ctx: context.Background(), st: st, clk: clk, f: f}
	r.s = expo.New(expo.Options{BaseURL: f.URL(), Client: f.Client(), Now: clk.Now})
	return r
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if err := r.st.Write(r.ctx, func(tx *store.Tx) error { _, err := tx.ExecContext(r.ctx, q, args...); return err }); err != nil {
		r.t.Fatal(err)
	}
}

// device adds a phone device with prefs (JSON or "").
func (r *rig) device(id, prefs string) {
	r.t.Helper()
	var p any
	if prefs != "" {
		p = prefs
	}
	r.exec(`INSERT INTO device (id, kind, name, token_hash, token_hint, scopes, created_at, created_by, prefs)
		VALUES (?, 'phone', ?, randomblob(32), 'h', '["read","push"]', 1, 'test', ?)`, id, id, p)
}

func (r *rig) target(device, token string) model.Target {
	r.t.Helper()
	tg, err := r.st.UpsertTarget(r.ctx, model.Target{Kind: model.KindExpo, DeviceID: device, Endpoint: token,
		Meta: &model.TargetMeta{Platform: "ios"}})
	if err != nil {
		r.t.Fatal(err)
	}
	return tg
}

// sent sends one message to the fake (so it issues a ticket) and books it as a delivery
// sent ago before now.
func (r *rig) sent(tg model.Target, ago time.Duration) (int64, string) {
	r.t.Helper()
	res := r.s.Send(r.ctx, tg, testMessage())
	if res.Outcome != model.OutcomeSent || res.Ref == "" {
		r.t.Fatalf("%+v", res)
	}
	at := r.clk.Now().Add(-ago).UnixMilli()
	alert := fmt.Sprintf("al_%s", res.Ref)
	r.exec(`INSERT INTO alert (id, episode, trigger, status, created_at, updated_at) VALUES (?, ?, 'asked', 'fired', 1, 1)`, alert, alert)
	var id int64
	err := r.st.Write(r.ctx, func(tx *store.Tx) error {
		d := store.DeliveryRow{AlertID: alert, TargetID: tg.ID, TargetKind: model.KindExpo, Fire: 1, Status: "sent", Tries: 1,
			CreatedAt: at, UpdatedAt: at}
		if _, err := tx.DeliveryInsert(&d); err != nil {
			return err
		}
		d.Ticket, id = res.Ref, d.ID
		return tx.DeliverySave(d)
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return id, res.Ref
}

func (r *rig) receipt(id int64) (at int64, lastErr string) {
	_ = r.st.R().QueryRow(`SELECT coalesce(receipt_at, 0), coalesce(last_error, '') FROM delivery WHERE id = ?`, id).Scan(&at, &lastErr)
	return
}

func TestReceiptsPoll(t *testing.T) {
	r := newRig(t)
	r.device("dev_1", "")
	a := r.target("dev_1", "ExponentPushToken[aaaaaaaaaaaa]")
	b := r.target("dev_1", "ExponentPushToken[bbbbbbbbbbbb]")
	c := r.target("dev_1", "ExponentPushToken[cccccccccccc]")
	fresh, freshTk := r.sent(a, 10*time.Minute)
	ok, _ := r.sent(a, 20*time.Minute)
	gone, goneTk := r.sent(b, 20*time.Minute)
	bad, badTk := r.sent(c, 3*time.Hour)
	missing, missingTk := r.sent(a, 23*time.Hour)
	old, oldTk := r.sent(a, 25*time.Hour)
	r.f.SetReceipt(goneTk, expotest.ReceiptDeviceNotRegistered)
	r.f.SetReceipt(badTk, "InvalidCredentials")
	r.f.SetReceipt(missingTk, expotest.ReceiptMissing)

	p := expo.NewReceipts(expo.ReceiptsOptions{Store: r.st, Sender: r.s, Clock: r.clk})
	st, err := p.Poll(r.ctx)
	if err != nil || st != (expo.ReceiptStats{Asked: 4, OK: 1, Gone: 1, Errors: 1, Expired: 1, Missing: 1}) {
		t.Fatalf("%+v %v", st, err)
	}
	reqs := r.f.ReceiptRequests()
	if len(reqs) != 1 || len(reqs[0].IDs) != 4 || reqs[0].Err != nil {
		t.Fatalf("%+v", reqs)
	}
	for _, id := range reqs[0].IDs {
		if id == freshTk || id == oldTk {
			t.Errorf("asked for %s", id)
		}
	}
	now := r.clk.Now().UnixMilli()
	for _, c := range []struct {
		id      int64
		at      int64
		lastErr string
	}{{fresh, 0, ""}, {ok, now, ""}, {gone, now, "DeviceNotRegistered"}, {bad, now, expo.ErrTextCredentials}, {missing, 0, ""}, {old, now, ""}} {
		if at, le := r.receipt(c.id); at != c.at || le != c.lastErr {
			t.Errorf("delivery %d: %d %q, want %d %q", c.id, at, le, c.at, c.lastErr)
		}
	}
	gb, _ := r.st.TargetByID(r.ctx, b.ID)
	gc, _ := r.st.TargetByID(r.ctx, c.ID)
	ga, _ := r.st.TargetByID(r.ctx, a.ID)
	if gb.GoneAt == 0 || gb.Enabled || gc.GoneAt != 0 || !gc.Enabled || gc.LastError != expo.ErrTextCredentials || ga.GoneAt != 0 || ga.LastError != "" {
		t.Fatalf("a %+v\nb %+v\nc %+v", ga, gb, gc)
	}

	// The missing ticket stays open until Expo has forgotten it (24 h), then is closed.
	r.clk.Advance(30 * time.Minute)
	if st, _ = p.Poll(r.ctx); st.Missing != 1 || st.Asked != 2 || st.OK != 1 { // fresh is now ready
		t.Fatalf("%+v", st)
	}
	r.clk.Advance(time.Hour)
	if st, _ = p.Poll(r.ctx); st.Expired != 1 || st.Asked != 0 {
		t.Fatalf("%+v", st)
	}
	if at, _ := r.receipt(missing); at == 0 {
		t.Fatal("missing ticket never closed")
	}
}

func TestReceiptsChunksAndErrors(t *testing.T) {
	r := newRig(t)
	r.device("dev_1", "")
	a := r.target("dev_1", "ExponentPushToken[aaaaaaaaaaaa]")
	// 1500 tickets: two requests of 1000 and 500 ids.
	err := r.st.Write(r.ctx, func(tx *store.Tx) error {
		if _, err := tx.ExecContext(r.ctx, `INSERT INTO alert (id, episode, trigger, status, created_at, updated_at) VALUES ('al_x', 'e', 'asked', 'fired', 1, 1)`); err != nil {
			return err
		}
		at := r.clk.Now().Add(-time.Hour).UnixMilli()
		for i := range 1500 {
			d := store.DeliveryRow{AlertID: "al_x", TargetID: a.ID, TargetKind: "expo", Fire: i + 1, Status: "sent", CreatedAt: at, UpdatedAt: at}
			if _, err := tx.DeliveryInsert(&d); err != nil {
				return err
			}
			d.Ticket = fmt.Sprintf("tk-%04d", i)
			if err := tx.DeliverySave(d); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 1500 {
		r.f.SetReceipt(fmt.Sprintf("tk-%04d", i), expotest.ReceiptOK)
	}
	p := expo.NewReceipts(expo.ReceiptsOptions{Store: r.st, Sender: r.s, Clock: r.clk})
	// A failing service: nothing is marked, the error is returned, the next poll retries.
	r.f.ScriptReceipts(expotest.Reply{Status: 503})
	if st, err := p.Poll(r.ctx); err == nil || st.Asked != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	st, err := p.Poll(r.ctx)
	if err != nil || st.Asked != 1500 || st.OK != 1500 {
		t.Fatalf("%+v %v", st, err)
	}
	reqs := r.f.ReceiptRequests()
	if len(reqs) != 3 || len(reqs[1].IDs) != 1000 || len(reqs[2].IDs) != 500 {
		t.Fatalf("%d requests", len(reqs))
	}
}

func TestReceiptsLoopOnFakeClock(t *testing.T) {
	r := newRig(t)
	r.device("dev_1", "")
	a := r.target("dev_1", "ExponentPushToken[aaaaaaaaaaaa]")
	id, tk := r.sent(a, time.Hour)
	r.f.SetReceipt(tk, expotest.ReceiptDeviceNotRegistered)
	p := expo.NewReceipts(expo.ReceiptsOptions{Store: r.st, Sender: r.s, Clock: r.clk})
	p.Start()
	defer p.Stop()
	waitTimer(t, r.clk)
	r.clk.Advance(4 * time.Minute)
	time.Sleep(50 * time.Millisecond)
	if len(r.f.ReceiptRequests()) != 0 {
		t.Fatal("polled before 5 minutes")
	}
	r.clk.Advance(time.Minute)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if at, _ := r.receipt(id); at != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no poll after 5 minutes")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if g, _ := r.st.TargetByID(r.ctx, a.ID); g.GoneAt == 0 {
		t.Fatal("target not gone")
	}
}

// waitTimer waits until a goroutine has armed a timer on the fake clock.
func waitTimer(t *testing.T, clk *clock.Fake) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for clk.Pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no timer armed")
		}
		time.Sleep(time.Millisecond)
	}
}
