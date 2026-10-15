package rules

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

var update = flag.Bool("update", false, "rewrite testdata/text golden files")

// Golden texts of push.md 5.5: one file per case, title / body / url / tag.

var textNow = time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC) // a Monday

func ms(t time.Time) model.Time { return model.Time(t.UnixMilli()) }

func sp(s string) *string { return &s }

func sessionAlert(t model.Trigger, f model.Facts) model.Alert {
	a := model.Alert{AlertBrief: model.AlertBrief{ID: "al_1", Trigger: t, Status: model.StatusFired},
		Session: sp("sess-1"), Seat: sp("seat-work"), Machine: sp("m-gpu"), Facts: f}
	a.SinceAt = ms(textNow.Add(-19 * time.Minute))
	return a
}

func seatAlert(t model.Trigger, f model.Facts) model.Alert {
	return model.Alert{AlertBrief: model.AlertBrief{ID: "al_2", Trigger: t, Status: model.StatusFired},
		Seat: sp("seat-client"), WindowKey: sp(f.Window), Facts: f}
}

func TestRenderGolden(t *testing.T) {
	work := model.Facts{Seat: "work", Project: "api", Machine: "gpu-box"}
	with := func(f model.Facts, mod func(*model.Facts)) model.Facts { mod(&f); return f }
	resets := ms(time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC))
	room := &Room{Label: "work", Left: 59.4}
	cases := []struct {
		name string
		a    model.Alert
		o    TextOptions
	}{
		{"needs_permission", sessionAlert(model.TriggerNeedsPermission, with(work, func(f *model.Facts) {
			f.State, f.Tool, f.Message = "needs_permission", "Bash", "Claude needs your permission to use Bash"
		})), TextOptions{}},
		{"needs_permission_bare", sessionAlert(model.TriggerNeedsPermission, with(work, func(f *model.Facts) {
			f.State = "needs_permission"
		})), TextOptions{}},
		{"asked_question", sessionAlert(model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Question = "asked", "question", "Ship it to prod now, or wait for the review?"
		})), TextOptions{}},
		{"asked_last_message", sessionAlert(model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Message = "asked", "question", "Which table should I drop?\nMore detail here."
		})), TextOptions{}},
		{"asked_generic", sessionAlert(model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail = "asked", "question"
		})), TextOptions{}},
		{"asked_plan", sessionAlert(model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Tool = "asked", "tool", "ExitPlanMode"
		})), TextOptions{}},
		{"asked_tool_question", sessionAlert(model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Tool, f.Question = "asked", "tool", "AskUserQuestion", "Postgres or SQLite?"
		})), TextOptions{}},
		{"asked_elicitation", sessionAlert(model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Message = "asked", "elicitation", "The MCP server wants a token"
		})), TextOptions{}},
		{"failed_rate_limit", sessionAlert(model.TriggerFailed, with(model.Facts{Seat: "client", Project: "data-pipeline", Machine: "gpu-box"},
			func(f *model.Facts) { f.State, f.Detail, f.ResetsAt = "failed", "rate_limit", resets })), TextOptions{Room: room}},
		{"failed_auth", sessionAlert(model.TriggerFailed, with(work, func(f *model.Facts) {
			f.State, f.Detail = "failed", "authentication_failed"
		})), TextOptions{}},
		{"failed_billing", sessionAlert(model.TriggerFailed, with(work, func(f *model.Facts) {
			f.State, f.Detail = "failed", "billing_error"
		})), TextOptions{}},
		{"failed_other", sessionAlert(model.TriggerFailed, with(work, func(f *model.Facts) {
			f.State, f.Detail = "failed", "server_error"
		})), TextOptions{}},
		{"limit_80", seatAlert(model.TriggerLimit80, model.Facts{Seat: "client", Window: "five_hour", Pct: 82.7, ResetsAt: resets}),
			TextOptions{Room: room}},
		{"limit_95_reached", seatAlert(model.TriggerLimit95, model.Facts{Seat: "client", Window: "five_hour", Pct: 100, ResetsAt: resets}),
			TextOptions{}},
		{"limit_seven_day_later", seatAlert(model.TriggerLimit80, model.Facts{Seat: "client", Window: "seven_day", Pct: 81,
			ResetsAt: ms(time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC))}), TextOptions{}},
		{"reset", seatAlert(model.TriggerReset, model.Facts{Seat: "client", Window: "five_hour", Peak: 97.2}), TextOptions{}},
		{"machine_silent", model.Alert{AlertBrief: model.AlertBrief{ID: "al_3", Trigger: model.TriggerMachineSilent},
			Machine: sp("m-old"), Facts: model.Facts{Machine: "old-vps", Sessions: 3,
				LastSeen: ms(textNow.Add(-58 * time.Minute))}}, TextOptions{}},
		{"machine_silent_minutes", model.Alert{AlertBrief: model.AlertBrief{ID: "al_4", Trigger: model.TriggerMachineSilent},
			Machine: sp("m-old"), Facts: model.Facts{Machine: "old-vps", Sessions: 1,
				LastSeen: ms(textNow.Add(-20*time.Minute - 30*time.Second))}}, TextOptions{}},
		{"repush", sessionAlert(model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Question = "asked", "question", "Ship it?"
		})), TextOptions{Repush: true}},
		{"private_session", sessionAlert(model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Question = "asked", "question", "secret project question"
		})), TextOptions{Private: true}},
		{"private_limit", seatAlert(model.TriggerLimit95, model.Facts{Seat: "client", Window: "five_hour", Pct: 96}), TextOptions{Private: true}},
		{"private_reset", seatAlert(model.TriggerReset, model.Facts{Seat: "client", Window: "five_hour", Peak: 90}), TextOptions{Private: true}},
		{"private_machine", model.Alert{AlertBrief: model.AlertBrief{ID: "al_3", Trigger: model.TriggerMachineSilent},
			Machine: sp("m-old"), Facts: model.Facts{Machine: "old-vps"}}, TextOptions{Private: true}},
		{"long_cut", sessionAlert(model.TriggerAsked, with(model.Facts{Seat: strings.Repeat("s", 40), Project: strings.Repeat("p", 60)},
			func(f *model.Facts) { f.State, f.Detail, f.Question = "asked", "question", strings.Repeat("why ", 80) })), TextOptions{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := c.o
			o.Now = textNow
			got := Render(c.a, o)
			checkGolden(t, c.name, got)
			if n := len([]rune(got.Title)); n > TitleMax {
				t.Errorf("title %d runes", n)
			}
			if n := len([]rune(got.Body)); n > BodyMax {
				t.Errorf("body %d runes", n)
			}
		})
	}
}

func TestRenderSummaryGolden(t *testing.T) {
	var as []model.Alert
	for i, p := range []string{"api", "web", "infra", "docs", "ml", "ops"} {
		a := sessionAlert(model.TriggerAsked, model.Facts{Seat: "work", Project: p, Machine: "gpu-box", State: "asked", Detail: "question"})
		a.ID = fmt.Sprintf("al_%d", i)
		if i == 1 {
			a.Trigger, a.Facts.State = model.TriggerNeedsPermission, "needs_permission"
		}
		as = append(as, a)
	}
	checkGolden(t, "summary_sessions", RenderSummary(as, TextOptions{Now: textNow}))
	checkGolden(t, "summary_private", RenderSummary(as[:3], TextOptions{Now: textNow, Private: true}))
	mixed := []model.Alert{as[0], seatAlert(model.TriggerLimit80, model.Facts{Seat: "client", Window: "five_hour", Pct: 81})}
	checkGolden(t, "summary_mixed", RenderSummary(mixed, TextOptions{Now: textNow}))
}

func checkGolden(t *testing.T, name string, got Text) {
	t.Helper()
	text := fmt.Sprintf("title: %s\nbody: %s\nurl: %s\ntag: %s\n", got.Title, got.Body, got.URL, got.Tag)
	path := filepath.Join("testdata", "text", name+".txt")
	if *update {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != text {
		t.Errorf("%s:\n got:\n%s\nwant:\n%s", name, text, want)
	}
}

func TestClockAndLabels(t *testing.T) {
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC), "17:00"},
		{time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC), "Thu 17:00"},
	} {
		if got := Clock(c.at, textNow, time.UTC); got != c.want {
			t.Errorf("Clock(%v) = %q, want %q", c.at, got, c.want)
		}
	}
	if got := SeatLabel("", "client@example.com"); got != "client" {
		t.Errorf("SeatLabel email = %q", got)
	}
	if got := SeatLabel("", ""); got != "Unattributed" {
		t.Errorf("SeatLabel empty = %q", got)
	}
	if got := Cut("abcdef", 4); got != "abc…" {
		t.Errorf("Cut = %q", got)
	}
	if got := WindowLabel("five_hour"); got != "5-hour" {
		t.Errorf("WindowLabel = %q", got)
	}
}
