package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// A snooze re-push dropped as stale must not leave the old snooze
// token usable.
func TestStaleSnoozeRepushInvalidatesOldToken(t *testing.T) {
	h := newHarness(t, withSweep(time.Hour))
	h.askQ(s1, 0)
	h.advanceTo(60 * time.Second)
	tok := h.expectSends(1)[0].Msg.Payload.Actions
	h.advanceTo(2 * time.Minute)
	if _, applied, err := h.eng.Act(h.ctx, tok.Snooze); err != nil || !applied {
		t.Fatal(applied, err)
	}
	h.stop() // the hub is down past the snooze and past max_age
	h.advanceTo(55 * time.Minute)
	h.start()
	h.advanceTo(56 * time.Minute)
	a := h.alertOf(s1, model.TriggerAsked)
	if a.Status != model.StatusFired || a.FireCount != 2 {
		t.Fatalf("status %s fire_count %d, want fired / 2", a.Status, a.FireCount)
	}
	if _, applied, err := h.eng.Act(h.ctx, tok.Snooze); err != nil || applied {
		t.Fatalf("the old snooze token was replayed: applied=%v err=%v", applied, err)
	}
}

// A bad or expired token is rejected without the write lock.
func TestActRejectsBadTokenWithoutWriteTransaction(t *testing.T) {
	h := newHarness(t, withSweep(time.Hour))
	h.askQ(s1, 0)
	h.advanceTo(60 * time.Second)
	tok := h.expectSends(1)[0].Msg.Payload.Actions
	_, _, _ = h.eng.Act(h.ctx, "a1.!!") // loads and caches the key
	h.advanceTo(25 * time.Hour)
	h.write(func(tx *store.Tx) error { // the write lock is held while the checks run
		ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
		defer cancel()
		if _, _, err := h.eng.Act(ctx, tamperToken(tok.Seen)); err != model.ErrBadMAC {
			t.Errorf("tampered token: %v", err)
		}
		if _, _, err := h.eng.Act(ctx, tok.Seen); err != model.ErrExpired {
			t.Errorf("expired token: %v", err)
		}
		return nil
	})
}
