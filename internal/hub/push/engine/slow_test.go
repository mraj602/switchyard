package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/engine"
	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// slowSender ignores its context and takes a long time: a tarpit server.
type slowSender struct{ d time.Duration }

func (slowSender) Kind() string { return model.KindNtfy }
func (s slowSender) Send(ctx context.Context, t model.Target, m model.Message) model.Result {
	time.Sleep(s.d)
	return model.Result{Outcome: model.OutcomeRetry, Err: "timeout"}
}

func (h *harness) restart(cfg engine.Config, senders ...model.Sender) {
	h.t.Helper()
	cfg.Store, cfg.Clock, cfg.Broker, cfg.SweepEvery, cfg.PublicURL = h.st, h.clk, h.broker, h.sweep, "https://hub.example.ts.net"
	cfg.Senders = senders
	h.eng = engine.New(cfg)
	if err := h.eng.Start(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	h.eng.Sync()
}

// Slow targets must not make the write-back fail or hold up the
// pass, and a push that did go out must never go out again.
func TestSlowTargetsDoNotCauseResends(t *testing.T) {
	h := newHarness(t, noStart())
	for i := 0; i < 8; i++ {
		h.addTarget(h.phone, model.KindNtfy, "")
	}
	h.restart(engine.Config{PassBudget: 100 * time.Millisecond}, h.wp, slowSender{d: 700 * time.Millisecond})
	h.advanceTo(500 * time.Millisecond)
	h.askQ(s1, 0)
	start := time.Now()
	h.advanceTo(60 * time.Second) // the question's push, and its retry slots, run here
	h.advanceTo(66 * time.Second)
	h.advanceTo(100 * time.Second)
	if took := time.Since(start); took > 25*time.Second {
		t.Errorf("passes took %v of real time", took)
	}
	if n := len(h.wp.sends()); n != 1 {
		t.Fatalf("the web push target got %d sends, want exactly 1", n)
	}
	a := h.alertOf(s1, model.TriggerAsked)
	sentDeliveries := 0
	for _, d := range h.deliveries(a.ID) {
		if d.Status == "sent" {
			sentDeliveries++
			if d.Tries != 1 {
				t.Errorf("sent delivery %s has %d tries", d.Target, d.Tries)
			}
		}
	}
	if sentDeliveries != 1 {
		t.Errorf("%d sent deliveries, want 1", sentDeliveries)
	}
}

// The per-pass cap leaves the rest pending for later passes; each target is sent once.
func TestPerPassCapDefersTheRest(t *testing.T) {
	h := newHarness(t, noStart())
	for i := 0; i < 5; i++ {
		h.addTarget(h.phone, model.KindNtfy, "")
	}
	h.restart(engine.Config{MaxPushesPerPass: 2}, h.wp, h.nf)
	h.advanceTo(500 * time.Millisecond)
	h.askQ(s1, 0)
	h.advanceTo(61 * time.Second)
	if n := len(h.wp.sends()) + len(h.nf.sends()); n < 2 || n > 3 {
		t.Fatalf("first pass sent %d, want the cap (2, or 3 with a same-pass retry)", n)
	}
	h.advanceTo(10 * time.Minute)
	if len(h.wp.sends()) != 1 || len(h.nf.sends()) != 5 {
		t.Fatalf("webpush %d, ntfy %d sends; want 1 and 5", len(h.wp.sends()), len(h.nf.sends()))
	}
}
