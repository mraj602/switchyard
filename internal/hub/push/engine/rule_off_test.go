package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// push.md 3.6 step 2b: turning a rule off cancels its pending alerts at once; alerts that
// already fired stay as they are.
func TestRuleOffCancelsPendingAlerts(t *testing.T) {
	r := newAPIRig(t)
	h := r.h
	h.askQ(sid(2), 0)
	h.advanceTo(60 * time.Second)
	h.expectSends(1)
	h.askQ(s1, 50*time.Second) // pending until 110 s
	// The reset rule (off by default) on: a window at 85% gets a reset alert due at its reset.
	r.must("PUT", "/v1/rules/rule_reset", r.owner, `{"enabled":true}`, 200, nil)
	h.sample("", 61*time.Second, 85, at(3*time.Hour), -1, time.Time{})
	reset := h.byTrigger(model.TriggerReset)
	if len(reset) != 1 || reset[0].Status != model.StatusPending {
		t.Fatalf("reset alerts: %+v", reset)
	}

	h.advanceTo(70 * time.Second)
	r.must("PUT", "/v1/rules/rule_asked", r.owner, `{"enabled":false}`, 200, nil)
	r.must("PUT", "/v1/rules/rule_reset", r.owner, `{"enabled":false}`, 200, nil)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusCancelled, model.ReasonRuleOff)
	h.expectStatus(h.byTrigger(model.TriggerReset)[0], model.StatusCancelled, model.ReasonRuleOff)
	h.expectStatus(h.alertOf(sid(2), model.TriggerAsked), model.StatusFired, "")
	h.advanceTo(4 * time.Hour)
	for _, s := range h.wp.sends() {
		if title := s.Msg.Payload.Title; strings.Contains(title, "reset") || (strings.Contains(title, "asked") && s.At.After(at(time.Minute))) {
			t.Errorf("sent after its rule was turned off: %s %q", s.At, title)
		}
	}
	h.expectStatus(h.byTrigger(model.TriggerReset)[0], model.StatusCancelled, model.ReasonRuleOff)
}

// An override that says "do not notify" for one account cancels that account's pending
// alerts; the others still fire.
func TestOffOverrideCancelsOnlyItsScope(t *testing.T) {
	r := newAPIRig(t)
	h := r.h
	h.askQ(s1, 0)
	h.hookOn(sid(2), "other@example.com", "Stop", 0, map[string]any{"last_assistant_message": "Ok.\n\nNext?"})
	seat := deref(h.alertOf(s1, model.TriggerAsked).Seat)
	r.must("POST", "/v1/rules", r.owner, `{"trigger":"asked","enabled":false,"scope":{"seats":["`+seat+`"]}}`, 201, nil)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusCancelled, model.ReasonRuleOff)
	h.advanceTo(60 * time.Second)
	h.expectSends(1)
	h.expectStatus(h.alertOf(sid(2), model.TriggerAsked), model.StatusFired, "")
}
