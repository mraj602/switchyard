package engine_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

// Phase 5 (WP-C): direct_push records, the catch-up hold, the frozen pause, removed machines
// and the pull hello (push.md 3.1, 3.3, 3.6; machines.md 5.5, 6.2, 8.1). Same harness as the
// push.md 11.1 rows: fake clock, real store, fake senders.

const s2 = "c41e0000-0000-4000-8000-000000000002"

// hookRec builds (does not ingest) a hook record of session sid on the default seat.
func (h *harness) hookRec(sid, event string, ts time.Duration, fields map[string]any) *record.Record {
	h.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + sid + ".jsonl"}
	for k, v := range fields {
		p[k] = v
	}
	seat, slot := h.seatMeta("")
	raw, _ := json.Marshal(p)
	r, err := record.NewHookEvent(record.Meta{Machine: h.machine, Slot: slot, Seat: seat, Client: "sy/0.0.0-test", Now: at(ts)},
		raw, record.HookOptions{LastMessages: true})
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) askRec(sid string, ts time.Duration) *record.Record {
	return h.hookRec(sid, "Stop", ts, map[string]any{"last_assistant_message": "Done with the schema.\n\nShould I also update the migration?"})
}

func (h *harness) workRec(sid string, ts time.Duration) *record.Record {
	return h.hookRec(sid, "UserPromptSubmit", ts, map[string]any{"prompt": "go on"})
}

// episode is the hub's session key for a state entered by the record at T0+since.
func episode(sid, state string, since time.Duration) string {
	return "s:" + sid + ":" + state + ":" + strconv.FormatInt(at(since).UnixMilli(), 10)
}

// directRec builds a direct_push record of machine for session sid, state entered at
// T0+since, sent at T0+sent.
func (h *harness) directRec(machine, sid, trigger string, since, sent time.Duration, ok bool) *record.Record {
	h.t.Helper()
	_, slot := h.seatMeta("")
	p := record.DirectPushPayload{Episode: episode(sid, trigger, since), Trigger: trigger, SessionID: sid,
		Since: at(since).Format(time.RFC3339Nano), Record: record.NewID(at(since)), SentAt: at(sent).Format(time.RFC3339Nano),
		Channel: record.DirectChannelNtfy, ServerHost: "ntfy.sh", OK: ok}
	if !ok {
		p.Status = 503
	}
	r, err := record.NewDirectPush(record.Meta{Machine: machine, Slot: slot, Client: "sy/0.0.0-test", Now: at(sent)}, p)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

// batch ingests records in one batch from dev with the given options, then syncs the engine.
func (h *harness) batchFrom(dev store.Device, opt ingest.Options, recs ...*record.Record) ingest.Reply {
	h.t.Helper()
	var raws []json.RawMessage
	for _, r := range recs {
		b, err := r.Encode()
		if err != nil {
			h.t.Fatal(err)
		}
		raws = append(raws, b)
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "sy/0.0.0-test", "records": raws})
	rep, err := h.ing.IngestWith(h.ctx, dev, body, opt)
	if err != nil {
		h.t.Fatal(err)
	}
	if len(rep.Rejected) > 0 {
		h.t.Fatalf("rejected: %+v", rep.Rejected)
	}
	if h.eng != nil {
		h.eng.Sync()
	}
	return rep
}

func (h *harness) batch(more bool, recs ...*record.Record) ingest.Reply {
	return h.batchFrom(h.dev, ingest.Options{More: more}, recs...)
}

func (h *harness) alertNow(id string) model.Alert {
	h.t.Helper()
	a, err := h.st.AlertByID(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// Every alert status a direct_push can meet (machines.md 5.5), and its absence.
func TestDirectPushTable(t *testing.T) {
	type want struct {
		status model.Status
		reason string
		sends  int
		fires  int
	}
	cases := []struct {
		name string
		run  func(h *harness)
		want want
	}{
		{"no alert yet: created fired/direct, nothing sent", func(h *harness) {
			h.advanceTo(80 * time.Second)
			h.batch(false, h.askRec(s1, 0), h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusFired, model.ReasonDirect, 0, 1}},
		{"pending: becomes fired/direct and never fires", func(h *harness) {
			h.askQ(s1, 0)
			h.advanceTo(30 * time.Second)
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 29*time.Second, true))
		}, want{model.StatusFired, model.ReasonDirect, 0, 1}},
		{"skipped/stale: becomes fired/direct", func(h *harness) {
			h.advanceTo(20 * time.Minute)
			h.askQ(s1, 0)
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusFired, model.ReasonDirect, 0, 1}},
		{"fired by the hub: unchanged (both pushed)", func(h *harness) {
			h.askQ(s1, 0)
			h.advanceTo(70 * time.Second)
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusFired, "", 1, 1}},
		{"snoozed: unchanged", func(h *harness) {
			h.askQ(s1, 0)
			h.advanceTo(70 * time.Second)
			if _, err := h.eng.Snooze(h.ctx, h.alertOf(s1, model.TriggerAsked).ID, 0, h.phone); err != nil {
				h.t.Fatal(err)
			}
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusSnoozed, "", 1, 1}},
		{"seen: unchanged", func(h *harness) {
			h.askQ(s1, 0)
			h.advanceTo(70 * time.Second)
			if _, err := h.eng.Seen(h.ctx, h.alertOf(s1, model.TriggerAsked).ID, h.phone); err != nil {
				h.t.Fatal(err)
			}
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusSeen, "", 1, 1}},
		{"cancelled: unchanged", func(h *harness) {
			h.askQ(s1, 0)
			h.advanceTo(10 * time.Second)
			h.answer(s1, 10*time.Second)
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusCancelled, model.ReasonAnswered, 0, 0}},
		{"closed: unchanged", func(h *harness) {
			h.askQ(s1, 0)
			h.advanceTo(70 * time.Second)
			h.answer(s1, 70*time.Second)
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusClosed, model.ReasonAnswered, 1, 1}},
		{"skipped rule_off: kept", func(h *harness) {
			h.exec(`UPDATE rule SET enabled = 0 WHERE id = 'rule_asked'`)
			h.askQ(s1, 0)
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusSkipped, model.ReasonRuleOff, 0, 0}},
		{"ok false changes nothing: the hub still pushes", func(h *harness) {
			h.askQ(s1, 0)
			h.advanceTo(30 * time.Second)
			h.batch(false, h.directRec(h.machine, s1, "asked", 0, 29*time.Second, false))
		}, want{model.StatusFired, "", 1, 1}},
		{"episode already over in the same batch: created, then closed by reconcile", func(h *harness) {
			h.advanceTo(90 * time.Second)
			h.batch(false, h.askRec(s1, 0), h.workRec(s1, 80*time.Second), h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
		}, want{model.StatusClosed, model.ReasonAnswered, 0, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			c.run(h)
			h.advanceTo(10 * time.Minute)
			a := h.alertOf(s1, model.TriggerAsked)
			if a.Status != c.want.status || deref(a.Reason) != c.want.reason || a.FireCount != c.want.fires {
				t.Fatalf("alert %s/%s fires %d, want %s/%s fires %d%s", a.Status, deref(a.Reason), a.FireCount,
					c.want.status, c.want.reason, c.want.fires, h.dump())
			}
			h.expectSends(c.want.sends)
			if c.want.reason == model.ReasonDirect && a.FiredAt.Std().IsZero() {
				t.Fatalf("fired_at not set: %+v", a.AlertBrief)
			}
		})
	}
}

// The created fired/direct alert takes fired_at from the record's sent_at.
func TestDirectPushFiredAtIsSentAt(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(3 * time.Minute)
	h.batch(false, h.askRec(s1, 0), h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
	a := h.alertOf(s1, model.TriggerAsked)
	if !a.FiredAt.Std().Equal(at(75*time.Second)) || !a.SinceAt.Std().Equal(at(0)) {
		t.Fatalf("fired_at %v since %v", a.FiredAt.Std(), a.SinceAt.Std())
	}
}

// A direct_push about a session of another machine is stored and ignored.
func TestDirectPushForeignSessionIgnored(t *testing.T) {
	h := newHarness(t)
	other := "m_" + record.NewID(T0.Add(time.Second))
	c, err := auth.Create(h.ctx, h.st, auth.CreateRequest{Kind: "machine", Name: "other", Machine: other})
	if err != nil {
		t.Fatal(err)
	}
	h.askQ(s1, 0)
	h.advanceTo(30 * time.Second)
	rep := h.batchFrom(c.Device, ingest.Options{}, h.directRec(other, s1, "asked", 0, 29*time.Second, true))
	if len(rep.Accepted) != 1 {
		t.Fatalf("%+v", rep)
	}
	h.advanceTo(2 * time.Minute)
	h.expectSends(1)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusFired, "")
}

// Catch-up hold (push.md 3.3): an alert created overdue from a machine with more queued waits;
// the direct_push two batches later marks it, and the hub sends nothing.
func TestCatchUpHoldDirectPushTwoBatchesLater(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(5 * time.Minute)
	h.batch(true, h.askRec(s1, 0)) // the question, 5 minutes late, with more behind it
	a := h.alertOf(s1, model.TriggerAsked)
	if a.Status != model.StatusPending || !a.DueAt.Std().Equal(at(5*time.Minute+10*time.Second)) {
		t.Fatalf("held alert: %+v", a.AlertBrief)
	}
	h.advanceTo(5*time.Minute + 5*time.Second)
	h.batch(true, h.workRec(s2, 3*time.Minute))
	h.advanceTo(5*time.Minute + 10*time.Second) // due: the machine still catches up -> postponed
	if got := h.alertNow(a.ID); got.Status != model.StatusPending || !got.DueAt.Std().Equal(at(5*time.Minute+20*time.Second)) {
		t.Fatalf("at fire time: %+v%s", got.AlertBrief, h.dump())
	}
	h.advanceTo(5*time.Minute + 12*time.Second)
	h.batch(false, h.directRec(h.machine, s1, "asked", 0, 75*time.Second, true))
	h.advanceTo(15 * time.Minute)
	h.expectSends(0)
	h.expectStatus(a, model.StatusFired, model.ReasonDirect)
}

// Without the direct_push the hub pushes when the backlog is gone (the hold ends).
func TestCatchUpHoldEndsWhenBacklogDrained(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(5 * time.Minute)
	h.batch(true, h.askRec(s1, 0))
	h.advanceTo(5*time.Minute + 5*time.Second)
	h.batch(true, h.workRec(s2, 3*time.Minute))
	h.advanceTo(5*time.Minute + 12*time.Second)
	h.batch(false, h.hookRec(s2, "Notification", 4*time.Minute, map[string]any{"notification_type": "auth_success"}))
	h.advanceTo(5*time.Minute + 19*time.Second)
	h.expectSends(0)
	h.advanceTo(5*time.Minute + 20*time.Second)
	got := h.expectSends(1)
	if !got[0].At.Equal(at(5*time.Minute + 20*time.Second)) {
		t.Fatalf("sent at %v", got[0].At)
	}
}

// A machine that keeps reporting a backlog holds the alert for at most 2 minutes.
func TestCatchUpHoldCappedAtTwoMinutes(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(5 * time.Minute)
	h.batch(true, h.askRec(s1, 0))
	for d := 5*time.Minute + 5*time.Second; d <= 8*time.Minute; d += 5 * time.Second {
		h.advanceTo(d)
		h.batch(true, h.workRec(s2, d-5*time.Minute))
	}
	got := h.expectSends(1)
	if !got[0].At.Equal(at(7 * time.Minute)) {
		t.Fatalf("sent at %v, want created + 2 min", got[0].At)
	}
}

// An upload whose X-Sy-Backlog is above its batch size counts as "more" (no drain flag).
func TestCatchUpHoldFromUploadBacklog(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(5 * time.Minute)
	n := int64(40)
	h.batchFrom(h.dev, ingest.Options{Backlog: &n}, h.askRec(s1, 0))
	a := h.alertOf(s1, model.TriggerAsked)
	if !a.DueAt.Std().Equal(at(5*time.Minute + 10*time.Second)) {
		t.Fatalf("not held: %+v", a.AlertBrief)
	}
	// Backlog equal to the batch: nothing more queued, no hold.
	h2 := newHarness(t)
	h2.advanceTo(5 * time.Minute)
	one := int64(1)
	h2.batchFrom(h2.dev, ingest.Options{Backlog: &one}, h2.askRec(s1, 0))
	h2.expectSends(1)
}

// An alert that is not overdue at creation is not held, even from a machine with a backlog.
func TestCatchUpHoldOnlyForOverdueAlerts(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(10 * time.Second)
	h.batch(true, h.askRec(s1, 5*time.Second))
	a := h.alertOf(s1, model.TriggerAsked)
	if !a.DueAt.Std().Equal(at(65 * time.Second)) {
		t.Fatalf("due %v", a.DueAt.Std())
	}
}

func (h *harness) freeze() {
	h.t.Helper()
	h.write(func(tx *store.Tx) error {
		return tx.PutAlertSetting(store.KeyHubFrozen, []byte(`{"since":`+strconv.FormatInt(h.clk.Now().UnixMilli(), 10)+`,"moved_to":null}`), h.clk.Now().UnixMilli())
	})
	h.eng.Sync()
}

func (h *harness) unfreeze() {
	h.t.Helper()
	h.exec(`DELETE FROM setting WHERE key = ?`, store.KeyHubFrozen)
	h.eng.Poke()
	h.eng.Sync()
}

// Frozen (push.md 3.6): nothing is sent, the sweep creates nothing; unfreezing resumes
// subject to the stale guard.
func TestFrozenSendsNothingThenStaleGuard(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.answer(s2, 0) // a working session on the machine
	h.advanceTo(10 * time.Second)
	h.freeze()
	h.advanceTo(2 * time.Hour)
	h.expectSends(0)
	if n := len(h.byTrigger(model.TriggerMachineSilent)); n != 0 {
		t.Fatalf("silent alerts while frozen: %s", h.dump())
	}
	a := h.alertOf(s1, model.TriggerAsked)
	if a.Status != model.StatusPending {
		t.Fatalf("%+v", a.AlertBrief)
	}
	h.unfreeze()
	h.advanceTo(2*time.Hour + 30*time.Second)
	h.expectSends(0)
	h.expectStatus(a, model.StatusSkipped, model.ReasonStale)
	// Silence counts from the unfreeze, not from before the freeze.
	h.advanceTo(2*time.Hour + 30*time.Minute)
	if n := len(h.byTrigger(model.TriggerMachineSilent)); n != 0 {
		t.Fatalf("silent alert right after unfreeze: %s", h.dump())
	}
}

func TestFrozenShortlyThenFires(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.advanceTo(10 * time.Second)
	h.freeze()
	h.advanceTo(3 * time.Minute)
	h.expectSends(0)
	h.unfreeze()
	got := h.expectSends(1)
	if !got[0].At.Equal(at(3 * time.Minute)) {
		t.Fatalf("sent at %v", got[0].At)
	}
}

// A removed machine never raises machine_silent, and its pending alerts end as cancelled.
func TestRemovedMachineNoSilentAlert(t *testing.T) {
	h := newHarness(t)
	h.answer(s2, 0) // working: would go silent after an hour
	h.askQ(s1, 0)
	h.advanceTo(20 * time.Second)
	var rm store.MachineRemoval
	h.write(func(tx *store.Tx) error {
		var err error
		rm, err = tx.MachineRemove(h.machine, h.clk.Now().UnixMilli())
		return err
	})
	if len(rm.Devices) != 1 || rm.Devices[0] != h.dev.ID || len(rm.Alerts) != 1 {
		t.Fatalf("%+v", rm)
	}
	h.eng.Notify(ingest.Changes{Machines: map[string]bool{h.machine: true}})
	h.advanceTo(3 * time.Hour)
	h.expectSends(0)
	if n := len(h.byTrigger(model.TriggerMachineSilent)); n != 0 {
		t.Fatalf("silent alert for a removed machine: %s", h.dump())
	}
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusCancelled, model.ReasonEnded)
}

// A pull hello counts as hearing from the machine: the silent alert recovers (push.md 3.1).
func TestPullHelloRecoversSilentMachine(t *testing.T) {
	h := newHarness(t)
	h.answer(s1, 0)
	h.advanceTo(61 * time.Minute)
	h.expectSends(1)
	silent := h.byTrigger(model.TriggerMachineSilent)[0]
	if err := h.ing.Contact(h.ctx, h.dev, ingest.Contact{Reach: "pull", SyVersion: "0.5.0"}); err != nil {
		t.Fatal(err)
	}
	h.eng.Sync()
	h.expectStatus(silent, model.StatusClosed, model.ReasonRecovered)
	ms, err := h.st.Machines(h.ctx, []string{h.machine}, h.clk.Now())
	if err != nil || len(ms) != 1 || ms[0].Reach == nil || *ms[0].Reach != "pull" || int64(ms[0].LastSeen) != at(61*time.Minute).UnixMilli() {
		t.Fatalf("%+v %v", ms, err)
	}
}
