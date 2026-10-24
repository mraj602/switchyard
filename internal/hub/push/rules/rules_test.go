package rules

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

func TestEpisodeKeys(t *testing.T) {
	for got, want := range map[string]string{
		SessionEpisode("abc", "asked", 1700000000123): "s:abc:asked:1700000000123",
		LimitEpisode("seat1", "five_hour", 42, 80):    "l:seat1:five_hour:42:80",
		LimitEpisode("seat1", "seven_day", 42, 95):    "l:seat1:seven_day:42:95",
		ResetEpisode("seat1", "five_hour", 42):        "r:seat1:five_hour:42",
		SilentEpisode("m1", 99):                       "m:m1:99",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestSessionTrigger(t *testing.T) {
	for _, c := range []struct {
		state string
		needs bool
		want  model.Trigger
		ok    bool
	}{
		{"needs_permission", true, model.TriggerNeedsPermission, true},
		{"asked", true, model.TriggerAsked, true},
		{"failed", true, model.TriggerFailed, true},
		{"failed", false, "", false}, // overloaded / server_error: no push
		{"working", false, "", false},
		{"done", false, "", false},
	} {
		got, ok := SessionTrigger(c.state, c.needs)
		if got != c.want || ok != c.ok {
			t.Errorf("SessionTrigger(%q, %v) = %q, %v", c.state, c.needs, got, ok)
		}
	}
}

func TestDueAndStale(t *testing.T) {
	const s = int64(1000)
	min := int64(60_000)
	for _, c := range []struct {
		name               string
		since, delay, now  int64
		wantDue            int64
		maxAge             int64
		wantStale, overdue bool
	}{
		{"fresh: since+delay", 100 * s, 30 * s, 100 * s, 130 * s, 15 * min, false, false},
		{"seen late: due now", 100 * s, 30 * s, 200 * s, 200 * s, 15 * min, false, false},
		{"clock skew ahead: capped at now+delay", 900 * s, 30 * s, 100 * s, 130 * s, 15 * min, false, false},
		{"stale: past max age", 0, 30 * s, 30*s + 15*min + 1, 30*s + 15*min + 1, 15 * min, true, true},
		{"exactly max age: not stale", 0, 30 * s, 30*s + 15*min, 30*s + 15*min, 15 * min, false, false},
	} {
		if got := Due(c.since, c.delay, c.now); got != c.wantDue {
			t.Errorf("%s: Due = %d, want %d", c.name, got, c.wantDue)
		}
		if got := Stale(c.since, c.delay, c.maxAge, c.now); got != c.wantStale {
			t.Errorf("%s: Stale = %v", c.name, got)
		}
		if got := Overdue(c.since+c.delay, c.maxAge, c.now); got != c.overdue {
			t.Errorf("%s: Overdue = %v", c.name, got)
		}
	}
}

func TestMatchSpecificity(t *testing.T) {
	base := model.Rule{ID: "base_asked", Trigger: model.TriggerAsked, Base: true, Enabled: true, DelayS: 30}
	proj := model.Rule{ID: "rule_b", Trigger: model.TriggerAsked, Enabled: false, Scope: model.RuleScope{Projects: []string{"api"}}, UpdatedAt: 5}
	projOld := model.Rule{ID: "rule_a", Trigger: model.TriggerAsked, Enabled: true, Scope: model.RuleScope{Projects: []string{"api"}}, UpdatedAt: 1}
	seatProj := model.Rule{ID: "rule_c", Trigger: model.TriggerAsked, Enabled: true, DelayS: 0,
		Scope: model.RuleScope{Seats: []string{"work"}, Projects: []string{"api"}}, UpdatedAt: 1}
	// Windows do not count for a session trigger.
	sessWin := model.Rule{ID: "rule_d", Trigger: model.TriggerAsked, Enabled: true, Scope: model.RuleScope{Windows: []string{"five_hour"}}}
	rs := []model.Rule{base, proj, projOld, seatProj, sessWin}

	for _, c := range []struct {
		name string
		s    Subject
		want string
	}{
		{"seat+project beats project", Subject{Seat: "work", Project: "api"}, "rule_c"},
		{"newest of equal overrides", Subject{Seat: "home", Project: "api"}, "rule_b"},
		{"override beats base at equal specificity", Subject{Seat: "home", Project: "web"}, "rule_d"},
	} {
		got, ok := Match(rs, model.TriggerAsked, c.s)
		if !ok || got.ID != c.want {
			t.Errorf("%s: got %q (ok %v), want %q", c.name, got.ID, ok, c.want)
		}
	}
	if got, _ := Match([]model.Rule{base}, model.TriggerAsked, Subject{Project: "x"}); got.ID != "base_asked" {
		t.Errorf("base only: %q", got.ID)
	}
	if _, ok := Match([]model.Rule{base}, model.TriggerFailed, Subject{}); ok {
		t.Error("no rule for failed should not match")
	}
	// Ties on everything: lowest id.
	x := model.Rule{ID: "rule_x", Trigger: model.TriggerFailed, Scope: model.RuleScope{Seats: []string{"s"}}}
	y := x
	y.ID = "rule_y"
	if got, _ := Match([]model.Rule{y, x}, model.TriggerFailed, Subject{Seat: "s"}); got.ID != "rule_x" {
		t.Errorf("tie: %q", got.ID)
	}
}

func TestMatchWindows(t *testing.T) {
	base := model.Rule{ID: "base_limit_80", Trigger: model.TriggerLimit80, Base: true, Enabled: true}
	opus := model.Rule{ID: "rule_o", Trigger: model.TriggerLimit80, Enabled: true, Scope: model.RuleScope{Windows: []string{"seven_day_opus"}}}
	five := model.Rule{ID: "rule_f", Trigger: model.TriggerLimit80, Enabled: false, Scope: model.RuleScope{Windows: []string{"five_hour"}}}
	rs := []model.Rule{base, opus, five}
	if got, _ := Match(rs, model.TriggerLimit80, Subject{Seat: "s", Window: "five_hour"}); got.ID != "rule_f" {
		t.Errorf("five_hour: %q", got.ID)
	}
	if got, _ := Match(rs, model.TriggerLimit80, Subject{Seat: "s", Window: "seven_day"}); got.ID != "base_limit_80" {
		t.Errorf("seven_day (default windows): %q", got.ID)
	}
	if got, _ := Match(rs, model.TriggerLimit80, Subject{Seat: "s", Window: "seven_day_opus"}); got.ID != "rule_o" {
		t.Errorf("opus: %q", got.ID)
	}
	if _, ok := Match([]model.Rule{base}, model.TriggerLimit80, Subject{Window: "seven_day_sonnet"}); ok {
		t.Error("a window outside the default list must not match the base rule")
	}
}

func TestQuietHours(t *testing.T) {
	at := func(tz, s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, Location(tz))
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	q := func(tz, start, end string) model.QuietHours {
		return model.QuietHours{Enabled: true, Start: start, End: end, TZ: tz}
	}
	for _, c := range []struct {
		name    string
		q       model.QuietHours
		t       time.Time
		active  bool
		endWant time.Time
	}{
		{"before midnight", q("UTC", "23:00", "08:00"), at("UTC", "2026-10-12 23:30"), true, at("UTC", "2026-10-13 08:00")},
		{"after midnight", q("UTC", "23:00", "08:00"), at("UTC", "2026-10-13 07:59"), true, at("UTC", "2026-10-13 08:00")},
		{"end exclusive", q("UTC", "23:00", "08:00"), at("UTC", "2026-10-13 08:00"), false, time.Time{}},
		{"start inclusive", q("UTC", "23:00", "08:00"), at("UTC", "2026-10-12 23:00"), true, at("UTC", "2026-10-13 08:00")},
		{"daytime span", q("UTC", "12:00", "14:00"), at("UTC", "2026-10-12 13:00"), true, at("UTC", "2026-10-12 14:00")},
		{"outside daytime span", q("UTC", "12:00", "14:00"), at("UTC", "2026-10-12 15:00"), false, time.Time{}},
		{"start == end is off", q("UTC", "08:00", "08:00"), at("UTC", "2026-10-12 08:00"), false, time.Time{}},
		{"disabled", model.QuietHours{Start: "00:00", End: "23:59", TZ: "UTC"}, at("UTC", "2026-10-12 12:00"), false, time.Time{}},
		// London spring forward 2026-03-29 01:00 GMT -> 02:00 BST: a 01:30 end does not exist
		// and moves forward to 02:30 BST (01:30 UTC).
		{"London gap end moves forward", q("Europe/London", "23:00", "01:30"), at("UTC", "2026-03-29 01:10"), true,
			time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)},
		{"London spring night length", q("Europe/London", "23:00", "08:00"), at("Europe/London", "2026-03-29 07:30"), true,
			at("Europe/London", "2026-03-29 08:00")},
		// London fall back 2026-10-25 02:00 BST -> 01:00 GMT: 01:30 happens twice; an end of
		// 01:30 is its first occurrence (00:30 UTC).
		{"London repeat end first occurrence", q("Europe/London", "23:00", "01:30"), time.Date(2026, 10, 25, 0, 20, 0, 0, time.UTC), true,
			time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)},
		{"London repeat second pass is outside", q("Europe/London", "23:00", "01:30"), time.Date(2026, 10, 25, 1, 15, 0, 0, time.UTC), false,
			time.Time{}},
		{"New York evening", q("America/New_York", "22:00", "07:00"), time.Date(2026, 11, 1, 3, 30, 0, 0, time.UTC), true,
			at("America/New_York", "2026-11-01 07:00")},
		{"New York after DST end, local daytime", q("America/New_York", "22:00", "07:00"), time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC), false,
			time.Time{}},
		{"New York spring gap start", q("America/New_York", "02:30", "06:00"), time.Date(2026, 3, 8, 7, 45, 0, 0, time.UTC), true,
			at("America/New_York", "2026-03-08 06:00")},
	} {
		active, end := QuietWindow(c.q, c.t)
		if active != c.active || !end.Equal(c.endWant) {
			t.Errorf("%s: QuietWindow(%v) = %v, %v; want %v, %v", c.name, c.t.UTC(), active, end.UTC(), c.active, c.endWant.UTC())
		}
	}
}

func TestHoldsBypass(t *testing.T) {
	q := model.QuietHours{Enabled: true, Start: "23:00", End: "08:00", TZ: "UTC"}
	night := time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)
	if held, _ := Holds(q, false, night); !held {
		t.Error("normal rule should be held")
	}
	if held, _ := Holds(q, true, night); held {
		t.Error("bypass_quiet should pass with critical unset")
	}
	no := false
	q.Critical = &no
	if held, end := Holds(q, true, night); !held || !end.Equal(time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("critical false holds bypass rules too: %v %v", held, end)
	}
}

func TestValidateQuiet(t *testing.T) {
	for _, c := range []struct {
		q  model.QuietHours
		ok bool
	}{
		{model.QuietHours{Start: "23:00", End: "08:00"}, true},
		{model.QuietHours{Start: "24:00", End: "08:00"}, false},
		{model.QuietHours{Start: "7:00", End: "08:00"}, false},
		{model.QuietHours{Start: "23:00", End: "08:60"}, false},
		{model.QuietHours{Start: "23:00", End: "08:00", TZ: "Mars/Olympus"}, false},
		{model.QuietHours{Start: "23:00", End: "08:00", TZ: "Asia/Kolkata"}, true},
	} {
		if err := ValidateQuiet(c.q); (err == nil) != c.ok {
			t.Errorf("ValidateQuiet(%+v) = %v", c.q, err)
		}
	}
}
