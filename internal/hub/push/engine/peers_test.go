package engine_test

import (
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

// push.md 11.1 rows 39-45: sessions coordinating through peers (peers.md 6.4). A turn a peer
// message started, or one that ends waiting on another session's reply, does not push its
// plain-text question; the board still shows the session as asked. Permission prompts,
// AskUserQuestion and turns a person started still push.

// derivedTrue sets derived.<key> = true, as `sy hook` does for peer_prompt and peer_wait.
func derivedTrue(key string) hookOpt {
	return func(r *record.Record, _ map[string]any) {
		if r == nil {
			return
		}
		if r.Derived == nil {
			r.Derived = record.Derived{}
		}
		r.Derived.Set(key, true)
	}
}

// peerPrompt is a turn started by a cross-session message.
func (h *harness) peerPrompt(sid string, ts time.Duration) {
	h.hook(sid, "UserPromptSubmit", ts, map[string]any{"prompt": "<cross-session-message from=\"uds:x\">"}, derivedTrue("peer_prompt"))
}

// expectBoard checks the session's state as the board reads it.
func (h *harness) expectBoard(sid, state string, needsYou bool) {
	h.t.Helper()
	var st string
	var needs int
	h.write(func(tx *store.Tx) error {
		return tx.QueryRowContext(tx.Ctx(), `SELECT state, needs_you FROM session_state WHERE session_id = ?`, sid).Scan(&st, &needs)
	})
	if st != state || (needs == 1) != needsYou {
		h.t.Fatalf("board shows %s needs_you=%d, want %s %v", st, needs, state, needsYou)
	}
}

func TestRow39PeerMessageAnsweredWithAQuestion(t *testing.T) {
	h := newHarness(t)
	h.peerPrompt(s1, 0)
	h.askQ(s1, 5*time.Second) // answered the peer, ended on a question meant for it
	h.advanceTo(5 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusSkipped, model.ReasonPeers)
	h.expectBoard(s1, "asked", true)
}

func TestRow40WaitingOnAPeersReply(t *testing.T) {
	h := newHarness(t)
	h.answer(s1, 0) // a person asked it to coordinate
	h.askQ(s1, 5*time.Second, derivedTrue("peer_wait"))
	h.advanceTo(5 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusSkipped, model.ReasonPeers)
	h.expectBoard(s1, "asked", true)
	// The reply arrives and the session ends on a question again: still coordination.
	h.peerPrompt(s1, 6*time.Minute)
	h.askQ(s1, 6*time.Minute+5*time.Second)
	h.advanceTo(10 * time.Minute)
	h.expectSends(0)
}

func TestRow41PermissionInAPeerTurnStillPushes(t *testing.T) {
	h := newHarness(t)
	h.peerPrompt(s1, 0)
	h.permission(s1, time.Second)
	h.advanceTo(31 * time.Second)
	if got := h.expectSends(1); got[0].Msg.Payload.Trigger != model.TriggerNeedsPermission {
		t.Fatalf("%+v", got[0])
	}
}

func TestRow42AskUserQuestionInAPeerTurnStillPushes(t *testing.T) {
	h := newHarness(t)
	h.peerPrompt(s1, 0)
	h.hook(s1, "PreToolUse", time.Second, map[string]any{"tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": []any{map[string]any{"question": "Deploy to prod?"}}}})
	h.advanceTo(61 * time.Second)
	got := h.expectSends(1)
	if got[0].Msg.Payload.Trigger != model.TriggerAsked {
		t.Fatalf("%+v", got[0])
	}
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusFired, "")
}

func TestRow43PersonsTurnAfterAPeerTurnPushes(t *testing.T) {
	h := newHarness(t)
	h.peerPrompt(s1, 0)
	h.askQ(s1, 5*time.Second)
	h.advanceTo(time.Minute)
	h.answer(s1, time.Minute) // a person types: the next turn is theirs
	h.advanceTo(2 * time.Minute)
	h.askQ(s1, 2*time.Minute)
	h.advanceTo(2*time.Minute + 59*time.Second)
	h.expectSends(0)
	h.advanceTo(3 * time.Minute)
	h.expectSends(1)
}

func TestRow44SkipPeersOff(t *testing.T) {
	h := newHarness(t)
	h.settings(`{"skip_peers": false}`)
	h.peerPrompt(s1, 0)
	h.askQ(s1, time.Second)
	h.advanceTo(61 * time.Second)
	h.expectSends(1)
}

func TestRow45WaitFoldedDuringTheDelay(t *testing.T) {
	// A person's turn ends on a question (pending), then a Stop that waits on a peer's reply
	// lands in the same episode: at fire time the alert is cancelled, not pushed.
	h := newHarness(t)
	h.answer(s1, 0)
	h.askQ(s1, time.Second)
	h.advanceTo(10 * time.Second)
	h.askQ(s1, 10*time.Second, derivedTrue("peer_wait"))
	h.advanceTo(5 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusCancelled, model.ReasonPeers)
	h.expectBoard(s1, "asked", true)
}
