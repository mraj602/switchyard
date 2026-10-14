package engine_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
)

const s1 = "c41e0000-0000-4000-8000-000000000001"

func sid(n int) string { return "c41e0000-0000-4000-8000-00000000000" + string(rune('0'+n)) }

// push.md 11.1, one test per row. Each starts from an empty hub at T0 with one webpush
// target (fake sender) and the default rules; a real store in a temp directory; a fake clock.

func TestRow01QuestionPushedAfterOneMinute(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(500 * time.Millisecond)
	h.askQ(s1, 0)
	h.advanceTo(59 * time.Second)
	h.expectSends(0)
	h.advanceTo(60 * time.Second)
	got := h.expectSends(1)
	if !got[0].At.Equal(at(60*time.Second)) || got[0].Msg.Payload.Title != "client · api · asked a question" ||
		got[0].Msg.Payload.Body != "Should I also update the migration?" {
		t.Fatalf("%+v", got[0])
	}
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusFired, "")
}

func TestRow02AnsweredAt40s(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(500 * time.Millisecond)
	h.askQ(s1, 0)
	h.advanceTo(42 * time.Second)
	h.answer(s1, 40*time.Second)
	h.advanceTo(120 * time.Second)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusCancelled, model.ReasonAnswered)
}

func TestRow03Permission30s(t *testing.T) {
	h := newHarness(t)
	h.permission(s1, 0)
	h.advanceTo(200 * time.Millisecond)
	h.hook(s1, "Notification", 200*time.Millisecond, map[string]any{"notification_type": "permission_prompt",
		"message": "Claude needs your permission to use Bash"})
	h.advanceTo(29 * time.Second)
	h.expectSends(0)
	h.advanceTo(30 * time.Second)
	got := h.expectSends(1)
	if got[0].Msg.Payload.Title != "client · api · needs permission" || got[0].Msg.Payload.Body != "Claude needs your permission to use Bash · gpu-box" {
		t.Fatalf("%q / %q", got[0].Msg.Payload.Title, got[0].Msg.Payload.Body)
	}
	h.alertOf(s1, model.TriggerNeedsPermission) // exactly one: same episode
}

func TestRow04PermissionAnsweredByStop(t *testing.T) {
	h := newHarness(t)
	h.permission(s1, 0)
	h.advanceTo(10 * time.Second)
	h.stopDone(s1, 10*time.Second)
	h.advanceTo(2 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerNeedsPermission), model.StatusCancelled, model.ReasonAnswered)
}

func TestRow05AskedThenPermission(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.advanceTo(20 * time.Second)
	h.permission(s1, 20*time.Second)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusCancelled, model.ReasonSuperseded)
	h.advanceTo(49 * time.Second)
	h.expectSends(0)
	h.advanceTo(50 * time.Second)
	got := h.expectSends(1)
	if got[0].Msg.Payload.Trigger != model.TriggerNeedsPermission || !got[0].At.Equal(at(50*time.Second)) {
		t.Fatalf("%+v", got[0])
	}
}

func TestRow06FailureImmediateWithRoom(t *testing.T) {
	h := newHarness(t)
	// Another seat ("work") with room in its 5-hour window.
	h.sample("work@example.com", 0, 41, at(5*time.Hour), -1, time.Time{})
	h.failure(s1, "rate_limit", 0)
	got := h.expectSends(1)
	if !got[0].At.Equal(T0) || got[0].Msg.Payload.Title != "client · api · rate limited" ||
		!strings.Contains(got[0].Msg.Payload.Body, "work has 59% left") {
		t.Fatalf("%q / %q", got[0].Msg.Payload.Title, got[0].Msg.Payload.Body)
	}
}

func TestRow07OverloadedIsNoPush(t *testing.T) {
	h := newHarness(t)
	h.failure(s1, "overloaded", 0)
	h.advanceTo(5 * time.Minute)
	h.expectSends(0)
	if n := len(h.allAlerts()); n != 0 {
		t.Fatalf("%d alerts", n)
	}
}

func TestRow08RepeatFailureSameEpisode(t *testing.T) {
	h := newHarness(t)
	h.failure(s1, "rate_limit", 0)
	h.advanceTo(time.Second)
	h.failure(s1, "billing_error", time.Second)
	h.advanceTo(5 * time.Minute)
	h.expectSends(1)
	h.alertOf(s1, model.TriggerFailed)
}

func TestRow09LateRecord(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(50 * time.Second)
	h.askQ(s1, 0)
	h.advanceTo(59 * time.Second)
	h.expectSends(0)
	h.advanceTo(60 * time.Second)
	if got := h.expectSends(1); !got[0].At.Equal(at(60 * time.Second)) {
		t.Fatal(got[0].At)
	}
}

func TestRow10VeryLateRecordIsStale(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(30 * time.Minute)
	h.askQ(s1, 0)
	h.advanceTo(40 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusSkipped, model.ReasonStale)
}

func TestRow11ClockSkewedMachine(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 10*time.Minute) // ts 10 min in the future, arrives at 0
	h.advanceTo(59 * time.Second)
	h.expectSends(0)
	h.advanceTo(60 * time.Second)
	if got := h.expectSends(1); !got[0].At.Equal(at(60 * time.Second)) {
		t.Fatal(got[0].At)
	}
}

func TestRow12Limit80Once(t *testing.T) {
	h := newHarness(t)
	r := at(4 * time.Hour)
	for i, p := range []float64{79, 81, 85, 82} {
		h.advanceTo(time.Duration(i) * time.Minute)
		h.sample("", time.Duration(i)*time.Minute, p, r, -1, time.Time{})
		if want := map[bool]int{true: 0, false: 1}[i == 0]; len(h.wp.sends()) != want {
			t.Fatalf("after sample %d: %d sends", i, len(h.wp.sends()))
		}
	}
	got := h.expectSends(1)
	if !got[0].At.Equal(at(time.Minute)) || got[0].Msg.Payload.Title != "client · 5-hour at 81%" {
		t.Fatalf("%v %q", got[0].At, got[0].Msg.Payload.Title)
	}
}

func TestRow13LimitJumpPast95(t *testing.T) {
	h := newHarness(t)
	h.sample("", 0, 97, at(4*time.Hour), -1, time.Time{})
	got := h.expectSends(1)
	if got[0].Msg.Payload.Trigger != model.TriggerLimit95 {
		t.Fatal(got[0].Msg.Payload.Trigger)
	}
	l80 := h.byTrigger(model.TriggerLimit80)
	if len(l80) != 1 {
		t.Fatal(h.dump())
	}
	h.expectStatus(l80[0], model.StatusSkipped, model.ReasonSuperseded)
}

func TestRow14NewWindow(t *testing.T) {
	h := newHarness(t)
	h.sample("", 0, 81, at(time.Hour), -1, time.Time{})
	h.advanceTo(time.Hour + time.Minute)
	h.sample("", time.Hour+time.Minute, 81, at(6*time.Hour), -1, time.Time{})
	h.expectSends(2)
	l80 := h.byTrigger(model.TriggerLimit80)
	if len(l80) != 2 {
		t.Fatal(h.dump())
	}
	h.expectStatus(l80[0], model.StatusClosed, model.ReasonResetPassed)
}

func TestRow15StaleSample(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(2 * time.Hour)
	h.sample("", 0, 85, at(4*time.Hour), -1, time.Time{})
	h.expectSends(0)
	if len(h.allAlerts()) != 0 {
		t.Fatal(h.dump())
	}
}

func quietHub(h *harness) {
	h.settings(`{"quiet_hours": {"enabled": true, "start": "23:00", "end": "08:00", "tz": "UTC"}}`)
}

// T0 is 12:00; 23:10 is 11 h 10 min later, 08:00 next day 20 h.
const (
	at2310 = 11*time.Hour + 10*time.Minute
	at0100 = 13 * time.Hour
	at0200 = 14 * time.Hour
	at0800 = 20 * time.Hour
)

func TestRow16QuietHoursHold(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(map[bool]string{false: "still asked", true: "answered at 02:00"}[answered], func(t *testing.T) {
			h := newHarness(t, withSweep(time.Hour))
			quietHub(h)
			h.advanceTo(at2310)
			h.askQ(s1, at2310)
			h.advanceTo(at2310 + 2*time.Minute)
			h.expectSends(0)
			a := h.alertOf(s1, model.TriggerAsked)
			if d := h.deliveries(a.ID); len(d) != 1 || d[0].Status != "held" || deref(d[0].Reason) != "quiet" {
				t.Fatalf("%+v", d)
			}
			if answered {
				h.advanceTo(at0200)
				h.stopDone(s1, at0200) // answered and finished: no live session, so no silence alert either
			}
			h.advanceTo(at0800 - time.Second)
			h.expectSends(0)
			h.advanceTo(at0800)
			d := h.deliveries(a.ID)
			if answered {
				h.expectSends(0)
				if d[0].Status != "skipped" || deref(d[0].Reason) != "resolved" {
					t.Fatalf("%+v", d)
				}
				return
			}
			got := h.expectSends(1)
			if !got[0].At.Equal(at(at0800)) || d[0].Status != "sent" {
				t.Fatalf("%v %+v", got[0].At, d)
			}
		})
	}
}

func TestRow17FailureBypassesQuiet(t *testing.T) {
	h := newHarness(t, withSweep(time.Hour))
	quietHub(h)
	h.advanceTo(at0100)
	h.failure(s1, "billing_error", at0100)
	got := h.expectSends(1)
	if !got[0].At.Equal(at(at0100)) || got[0].Msg.Payload.Title != "client · api · billing problem" {
		t.Fatalf("%v %q", got[0].At, got[0].Msg.Payload.Title)
	}
}

func TestRow18DeviceQuietWithoutCritical(t *testing.T) {
	h := newHarness(t, withSweep(time.Hour))
	quietHub(h)
	silent := h.addTarget(h.addDevice("Pixel", `{"quiet_hours": {"enabled": true, "start": "22:00", "end": "07:00", "tz": "UTC", "critical": false}}`),
		model.KindWebPush, "")
	h.advanceTo(at0100)
	h.failure(s1, "billing_error", at0100)
	if len(h.wp.to(h.target)) != 1 || len(h.wp.to(silent)) != 0 {
		t.Fatalf("%+v", h.wp.sends())
	}
	h.advanceTo(19 * time.Hour) // 07:00
	if len(h.wp.to(silent)) != 1 {
		t.Fatal("held failure not released at the device's quiet end")
	}
}

func TestRow19Snooze(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.advanceTo(70 * time.Second)
	a := h.alertOf(s1, model.TriggerAsked)
	if _, err := h.eng.Snooze(h.ctx, a.ID, 0, h.phone); err != nil {
		t.Fatal(err)
	}
	h.eng.Sync()
	h.advanceTo(16*time.Minute + 9*time.Second)
	h.expectSends(1)
	h.advanceTo(16*time.Minute + 10*time.Second)
	got := h.expectSends(2)
	if !strings.HasPrefix(got[1].Msg.Payload.Title, "Still waiting · 16 min · ") {
		t.Fatalf("%q", got[1].Msg.Payload.Title)
	}
	b, _ := h.st.AlertByID(h.ctx, a.ID)
	if b.FireCount != 2 || b.Status != model.StatusFired {
		t.Fatalf("%+v", b.AlertBrief)
	}
}

func TestRow20SnoozeThenAnswered(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.advanceTo(70 * time.Second)
	a := h.alertOf(s1, model.TriggerAsked)
	if _, err := h.eng.Snooze(h.ctx, a.ID, 0, h.phone); err != nil {
		t.Fatal(err)
	}
	h.eng.Sync()
	h.advanceTo(5 * time.Minute)
	h.answer(s1, 5*time.Minute)
	h.advanceTo(20 * time.Minute)
	h.expectSends(1)
	h.expectStatus(a, model.StatusClosed, model.ReasonAnswered)
}

func TestRow21Seen(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.advanceTo(70 * time.Second)
	a := h.alertOf(s1, model.TriggerAsked)
	if b, err := h.eng.Seen(h.ctx, a.ID, h.phone); err != nil || b.Status != model.StatusSeen {
		t.Fatal(b, err)
	}
	h.eng.Sync()
	h.advanceTo(31 * time.Minute)
	h.expectSends(1)
	h.expectStatus(a, model.StatusSeen, "")
	h.answer(s1, 31*time.Minute)
	h.expectStatus(a, model.StatusClosed, model.ReasonAnswered)
	// Snooze or seen on a closed alert: conflict.
	if _, err := h.eng.Snooze(h.ctx, a.ID, 0, h.phone); err != model.ErrConflict {
		t.Fatal(err)
	}
}

func TestRow22ActionTokenReplay(t *testing.T) {
	h := newHarness(t, withSweep(time.Hour))
	h.askQ(s1, 0)
	h.advanceTo(60 * time.Second)
	tok := h.expectSends(1)[0].Msg.Payload.Actions
	h.advanceTo(2 * time.Minute)
	b, applied, err := h.eng.Act(h.ctx, tok.Snooze)
	if err != nil || !applied || b.Status != model.StatusSnoozed {
		t.Fatal(b, applied, err)
	}
	_, applied, err = h.eng.Act(h.ctx, tok.Snooze)
	if err != nil || applied {
		t.Fatal("replay applied", err)
	}
	// Seen by token: applied once, recorded against the target's device.
	_, applied, err = h.eng.Act(h.ctx, tok.Seen)
	if err != nil || !applied {
		t.Fatal(applied, err)
	}
	if _, applied, _ = h.eng.Act(h.ctx, tok.Seen); applied {
		t.Fatal("seen applied twice")
	}
	var seenBy string
	h.write(func(tx *store.Tx) error {
		a, err := tx.AlertGet(b.ID)
		seenBy = a.SeenBy
		return err
	})
	if seenBy != h.phone {
		t.Fatal(seenBy)
	}
	// Tampered and expired tokens.
	if _, _, err := h.eng.Act(h.ctx, tok.Seen[:len(tok.Seen)-2]+"AA"); err != model.ErrBadMAC {
		t.Fatal(err)
	}
	if _, _, err := h.eng.Act(h.ctx, "a1.!!"); err != model.ErrBadToken {
		t.Fatal(err)
	}
	h.advanceTo(25 * time.Hour)
	if _, _, err := h.eng.Act(h.ctx, tok.Seen); err != model.ErrExpired {
		t.Fatal(err)
	}
}

func TestRow23Batch(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.advanceTo(time.Duration(i-1) * 500 * time.Millisecond)
		h.askQ(sid(i), time.Duration(i-1)*500*time.Millisecond, cwd("/home/u/p"+string(rune('0'+i))))
	}
	h.advanceTo(2 * time.Minute)
	got := h.expectSends(1)
	p := got[0].Msg.Payload
	if p.Type != model.TypeSummary || p.Title != "3 sessions need you" || len(p.Alerts) != 3 || p.Tag != "sy-summary" || p.URL != "/#/" {
		t.Fatalf("%+v", p)
	}
	batches := map[string]bool{}
	for _, id := range p.Alerts {
		for _, d := range h.deliveries(id) {
			batches[deref(d.Batch)] = true
		}
	}
	if len(batches) != 1 {
		t.Fatalf("batches %v", batches)
	}
}

func TestRow24FloodCap(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		email := string(rune('a'+i)) + "@example.com"
		h.advanceTo(time.Duration(i) * 10 * time.Second)
		h.sample(email, time.Duration(i)*10*time.Second, 85, at(4*time.Hour), 85, at(5*24*time.Hour))
	}
	h.advanceTo(9*time.Minute + 59*time.Second)
	h.expectSends(6)
	h.advanceTo(10 * time.Minute)
	got := h.expectSends(7)
	last := got[6].Msg.Payload
	if last.Type != model.TypeSummary || last.Title != "4 alerts" || len(last.Alerts) != 4 {
		t.Fatalf("%+v", last)
	}
}

func TestRow25MutedTrigger(t *testing.T) {
	h := newHarness(t)
	other := h.addTarget(h.addDevice("Pixel", `{"mute": ["limit_80"]}`), model.KindWebPush, "")
	h.sample("", 0, 85, at(4*time.Hour), -1, time.Time{})
	if len(h.wp.to(h.target)) != 1 || len(h.wp.to(other)) != 0 {
		t.Fatalf("%+v", h.wp.sends())
	}
	a := h.byTrigger(model.TriggerLimit80)[0]
	for _, d := range h.deliveries(a.ID) {
		if d.Target == other && (d.Status != "skipped" || deref(d.Reason) != "muted") {
			t.Fatalf("%+v", d)
		}
	}
}

func TestRow26RuleOffDuringDelay(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.advanceTo(30 * time.Second)
	h.write(func(tx *store.Tx) error {
		r, err := tx.RuleGet("rule_asked")
		if err != nil {
			return err
		}
		r.Enabled = false
		return tx.RulePut(r)
	})
	h.advanceTo(2 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusCancelled, model.ReasonRuleOff)
}

func TestRow27Override(t *testing.T) {
	h := newHarness(t)
	h.write(func(tx *store.Tx) error {
		return tx.RulePut(model.Rule{ID: "rule_infra", Trigger: model.TriggerAsked, Enabled: true, DelayS: 300,
			Scope: model.RuleScope{Projects: []string{"infra"}}, UpdatedAt: model.Time(T0.UnixMilli())})
	})
	h.askQ(sid(1), 0, cwd("/srv/infra"))
	h.askQ(sid(2), 0)
	h.advanceTo(61 * time.Second)
	if got := h.expectSends(1); got[0].Msg.Payload.Session != sid(2) {
		t.Fatal(got[0].Msg.Payload.Session)
	}
	h.advanceTo(299 * time.Second)
	h.expectSends(1)
	h.advanceTo(300 * time.Second)
	if got := h.expectSends(2); got[1].Msg.Payload.Session != sid(1) {
		t.Fatal(got[1].Msg.Payload.Session)
	}
}

func TestRow28RemoteControlSkip(t *testing.T) {
	h := newHarness(t)
	h.settings(`{"skip_remote": true}`)
	h.askQ(s1, 0, remote("https://claude.ai/code/session_x"))
	h.advanceTo(5 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusSkipped, model.ReasonRemoteControl)
}

func TestRow29RestartMidDelay(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.advanceTo(20 * time.Second)
	h.stop()
	h.advanceTo(40 * time.Second)
	h.start()
	h.advanceTo(59 * time.Second)
	h.expectSends(0)
	h.advanceTo(60 * time.Second)
	if got := h.expectSends(1); !got[0].At.Equal(at(60 * time.Second)) {
		t.Fatal(got[0].At)
	}
}

func TestRow30DownPastMaxAge(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.stop()
	h.advanceTo(20 * time.Minute)
	h.start()
	h.advanceTo(30 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusSkipped, model.ReasonStale)
}

func TestRow31MachineSilent(t *testing.T) {
	h := newHarness(t)
	h.answer(s1, 0) // working
	h.advanceTo(59 * time.Minute)
	h.expectSends(0)
	h.advanceTo(61 * time.Minute)
	got := h.expectSends(1)
	if got[0].Msg.Payload.Title != "gpu-box silent for 1 h" || got[0].Msg.Payload.Body != "1 live session · last record 12:00" {
		t.Fatalf("%q / %q", got[0].Msg.Payload.Title, got[0].Msg.Payload.Body)
	}
	first := h.byTrigger(model.TriggerMachineSilent)[0]
	h.hook(s1, "Notification", 61*time.Minute, map[string]any{"notification_type": "auth_success"})
	h.expectStatus(first, model.StatusClosed, model.ReasonRecovered)
	h.advanceTo(2*time.Hour + 2*time.Minute)
	h.expectSends(2)
	if n := len(h.byTrigger(model.TriggerMachineSilent)); n != 2 {
		t.Fatal(h.dump())
	}
}

func TestRow32HubDownNotTheMachine(t *testing.T) {
	h := newHarness(t)
	h.answer(s1, 0)
	h.stop()
	h.advanceTo(2 * time.Hour)
	h.start()
	h.advanceTo(2*time.Hour + 59*time.Minute)
	h.expectSends(0)
	h.advanceTo(3*time.Hour + time.Minute)
	h.expectSends(1)
}

func TestRow33LaptopSuspend(t *testing.T) {
	h := newHarness(t)
	h.askQ(s1, 0)
	h.advanceTo(10 * time.Second)
	h.clk.Jump(3 * time.Hour) // wall clock only
	h.eng.Sync()
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusSkipped, model.ReasonStale)
	awake := h.eng.AwakeAt()
	if !awake.Equal(at(3*time.Hour + 10*time.Second)) {
		t.Fatal(awake)
	}
	// Silence counts from awake_at, not from the last record 3 h ago.
	for i := 0; i < 58; i++ {
		h.clk.Advance(time.Minute)
		h.eng.Sync()
	}
	h.expectSends(0)
	for i := 0; i < 3; i++ {
		h.clk.Advance(time.Minute)
		h.eng.Sync()
	}
	if got := h.expectSends(1); got[0].Msg.Payload.Trigger != model.TriggerMachineSilent {
		t.Fatal(got[0].Msg.Payload.Trigger)
	}
}

func TestRow34Baseline(t *testing.T) {
	h := newHarness(t, noStart())
	h.askQ(sid(1), 0)
	h.askQ(sid(2), 0)
	h.sample("", 0, 90, at(4*time.Hour), -1, time.Time{})
	h.start()
	h.advanceTo(10 * time.Minute)
	h.expectSends(0)
	all := h.allAlerts()
	if len(all) != 3 {
		t.Fatal(h.dump())
	}
	for _, a := range all {
		if a.Status != model.StatusSkipped || deref(a.Reason) != model.ReasonBaseline {
			t.Fatal(h.dump())
		}
	}
	// The log hides baseline rows; later starts reconcile normally.
	if len(h.alerts()) != 0 {
		t.Fatal("baseline listed")
	}
	h.stop()
	h.start()
	h.askQ(sid(3), 10*time.Minute)
	h.advanceTo(11 * time.Minute)
	h.expectSends(1)
}

func TestRow35Refold(t *testing.T) {
	h := newHarness(t)
	h.advanceTo(10 * time.Second)
	h.askQ(s1, 10*time.Second)
	h.advanceTo(20 * time.Second)
	h.hook(s1, "Notification", 20*time.Second, map[string]any{"notification_type": "auth_success"})
	h.advanceTo(30 * time.Second)
	h.answer(s1, 15*time.Second) // late: refold makes the session working since 15 s
	h.advanceTo(3 * time.Minute)
	h.expectSends(0)
	h.expectStatus(h.alertOf(s1, model.TriggerAsked), model.StatusCancelled, model.ReasonAnswered)
}

func TestRow36GoneTarget(t *testing.T) {
	h := newHarness(t)
	h.wp.script(h.target, model.Result{Outcome: model.OutcomeGone, HTTPStatus: 410})
	h.failure(sid(1), "rate_limit", 0)
	a := h.alertOf(sid(1), model.TriggerFailed)
	if d := h.deliveries(a.ID); len(d) != 1 || d[0].Status != "gone" || *d[0].HTTPStatus != 410 {
		t.Fatalf("%+v", d)
	}
	var enabled int
	var gone int64
	_ = h.st.R().QueryRow(`SELECT enabled, coalesce(gone_at, 0) FROM push_target WHERE id = ?`, h.target).Scan(&enabled, &gone)
	if enabled != 0 || gone == 0 {
		t.Fatal(enabled, gone)
	}
	h.failure(sid(2), "rate_limit", 0)
	h.expectSends(1)
	if d := h.deliveries(h.alertOf(sid(2), model.TriggerFailed).ID); len(d) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestRow37Retry(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(map[bool]string{false: "sent", true: "answered in between"}[answered], func(t *testing.T) {
			h := newHarness(t)
			busy := model.Result{Outcome: model.OutcomeRetry, HTTPStatus: 503, Err: "unavailable"}
			h.wp.script(h.target, busy, busy)
			h.failure(s1, "billing_error", 0)
			if answered {
				h.advanceTo(20 * time.Second)
				h.answer(s1, 20*time.Second)
			}
			h.advanceTo(10 * time.Minute)
			a := h.alertOf(s1, model.TriggerFailed)
			d := h.deliveries(a.ID)
			if answered {
				h.expectSends(2)
				if d[0].Status != "skipped" || deref(d[0].Reason) != "resolved" {
					t.Fatalf("%+v", d)
				}
				return
			}
			got := h.expectSends(3)
			if !got[1].At.Equal(at(15*time.Second)) || !got[2].At.Equal(at(75*time.Second)) || d[0].Status != "sent" || d[0].Tries != 3 {
				t.Fatalf("%v %v %+v", got[1].At, got[2].At, d)
			}
		})
	}
}

func TestRow38Private(t *testing.T) {
	h := newHarness(t, noTarget())
	pt := h.addTarget(h.addDevice("iPhone", `{"private": true}`), model.KindWebPush, "")
	h.askQ(s1, 0)
	h.advanceTo(time.Minute)
	got := h.wp.to(pt)
	if len(got) != 1 {
		t.Fatal(len(got))
	}
	p := got[0].Msg.Payload
	a := h.alertOf(s1, model.TriggerAsked)
	if p.Title != "Switchyard" || p.Body != "1 session needs you" || p.URL != "/#/alerts/"+a.ID || !p.Private ||
		strings.Contains(p.Body+p.Title, "migration") {
		t.Fatalf("%+v", p)
	}
}

// ntfy targets on ntfy.sh are private unless their prefs say otherwise; the Message carries
// the hub URL and the ntfy priority.
func TestNtfyTargetPrivateOnNtfySh(t *testing.T) {
	h := newHarness(t, noTarget())
	h.addTarget("", model.KindNtfy, "")
	h.exec(`UPDATE push_target SET endpoint = 'https://ntfy.sh/sy-abc'`)
	h.askQ(s1, 0)
	h.advanceTo(time.Minute)
	got := h.nf.sends()
	if len(got) != 1 || got[0].Msg.Payload.Title != "Switchyard" || got[0].Msg.Priority != 4 || got[0].Msg.HubURL != "https://hub.example.ts.net" {
		t.Fatalf("%+v", got)
	}
}

// Every status change publishes one alert stream event.
func TestAlertStreamEvents(t *testing.T) {
	h := newHarness(t)
	ch, _, cancel := h.broker.Subscribe(0)
	defer cancel()
	h.askQ(s1, 0)
	h.advanceTo(time.Minute)
	h.answer(s1, time.Minute)
	var changes []string
	for len(ch) > 0 {
		e := <-ch
		if e.Type == "alert" {
			var v struct {
				Change string `json:"change"`
			}
			_ = json.Unmarshal(e.Data, &v)
			changes = append(changes, v.Change)
		}
	}
	if strings.Join(changes, ",") != "created,fired,closed" {
		t.Fatalf("%q", changes)
	}
}
