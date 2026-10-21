package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

func lines(t *testing.T, path string, fn func([]byte)) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		fn(sc.Bytes())
	}
}

// WindowsOf finds the same windows the prototype stored for each snapshot, including keys
// it has never heard of (iguana_necktie).
func TestWindowsOfMatchesPrototype(t *testing.T) {
	type snap struct {
		TS      int64  `json:"ts"`
		Account string `json:"account_id"`
		Raw     string `json:"raw"`
	}
	samples := map[string]map[string]Sample{}
	lines(t, "../../../testdata/prototype/snapshots.jsonl", func(b []byte) {
		var s snap
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		m := map[string]Sample{}
		for _, w := range WindowsOf(json.RawMessage(s.Raw)) {
			m[w.Key] = w
		}
		samples[fmt.Sprint(s.TS, s.Account)] = m
	})
	checked := 0
	keys := map[string]bool{}
	lines(t, "../../../testdata/prototype/windows.jsonl", func(b []byte) {
		var w struct {
			TS           int64    `json:"ts"`
			Account      string   `json:"account_id"`
			Key          string   `json:"key"`
			Pct          *float64 `json:"pct"`
			ResetsAt     string   `json:"resets_at"`
			LimitDollars *float64 `json:"limit_dollars"`
		}
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatal(err)
		}
		m, ok := samples[fmt.Sprint(w.TS, w.Account)]
		if !ok {
			return // its snapshot is not in the fixture sample
		}
		checked++
		keys[w.Key] = true
		s, ok := m[w.Key]
		if !ok {
			t.Errorf("%d %s: window %s not found", w.TS, w.Account, w.Key)
			return
		}
		if w.Pct != nil && (s.UsedPct == nil || math.Abs(*s.UsedPct-*w.Pct) > 0.05) { // the prototype rounded to 0.1
			t.Errorf("%s: used %v, prototype %v", w.Key, s.UsedPct, *w.Pct)
		}
		if w.ResetsAt != "" {
			want, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
			if !s.ResetsAt.Equal(want) {
				t.Errorf("%s: resets %v, prototype %v", w.Key, s.ResetsAt, want)
			}
		}
		if w.LimitDollars != nil && (s.LimitDollars == nil || *s.LimitDollars != *w.LimitDollars) {
			t.Errorf("%s: limit dollars %v, prototype %v", w.Key, s.LimitDollars, *w.LimitDollars)
		}
	})
	if checked == 0 || !keys["iguana_necktie"] || !keys["five_hour"] {
		t.Fatalf("checked %d windows, keys %v", checked, keys)
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 10, 12, 16, 30, 0, 0, time.UTC)
	for _, raw := range []string{`"2026-10-12T16:30:00+00:00"`, `"2026-10-12T16:30:00Z"`, `1791822600`, `1791822600000`} {
		if got := ParseTime(json.RawMessage(raw)); !got.Equal(want) {
			t.Errorf("ParseTime(%s) = %v", raw, got)
		}
	}
	if !ParseTime(json.RawMessage(`null`)).IsZero() {
		t.Error("null")
	}
}

func TestPace(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	resets := start.Add(10 * time.Hour)
	half := start.Add(5 * time.Hour)
	for used, band := range map[float64]string{30: "comfortable", 50: "on_track", 60: "fast", 80: "runaway"} {
		p := PaceOf(used, start, resets, half)
		if p == nil || p.Band != band || p.ProjectedPct != used*2 {
			t.Errorf("used %v: %+v, want %s", used, p, band)
		}
	}
	if p := PaceOf(80, start, resets, half); p.FullAt == nil || !p.FullAt.Equal(start.Add(time.Duration(6.25*float64(time.Hour)))) {
		t.Errorf("full at %v", p.FullAt)
	}
	if PaceOf(10, start, resets, start.Add(time.Minute)) != nil {
		t.Error("too young window has a pace")
	}
	if PaceOf(10, start, resets, resets) != nil || PaceOf(10, time.Time{}, resets, half) != nil {
		t.Error("pace after reset or without start")
	}
}

// An idle account (claude.ai: the 5-hour window has not started) reads as five_hour 0 % with
// no reset, seven_day absent, and the model-scoped week from limits[].
func TestWindowsOfIdleAccount(t *testing.T) {
	raw := json.RawMessage(`{"five_hour":{"utilization":0,"resets_at":null},"seven_day":null,"seven_day_opus":null,
		"extra_usage":{"is_enabled":false},
		"limits":[{"kind":"session","group":"session","percent":0,"resets_at":null,"is_active":true,"scope":null},
		  {"kind":"weekly_scoped","group":"weekly","percent":0,"resets_at":"2026-10-27T04:00:00+00:00","is_active":false,
		   "scope":{"model":{"id":null,"display_name":"Fable"},"surface":null}}]}`)
	ws := WindowsOf(raw)
	if len(ws) != 2 || ws[0].Key != "five_hour" || !ws[0].Idle() || ws[1].Key != "weekly_fable" || ws[1].Idle() {
		t.Fatalf("%+v", ws)
	}
	if !ws[1].ResetsAt.Equal(time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)) || !ws[1].Inactive {
		t.Fatalf("%+v", ws[1])
	}
}

// Unscoped limits[] items fill five_hour / seven_day when the top-level field is null; when
// both carry the window the top-level one (unrounded) is kept; unknown kinds are left out.
func TestWindowsOfLimitsFallback(t *testing.T) {
	raw := json.RawMessage(`{"five_hour":{"utilization":8.4,"resets_at":"2026-10-21T22:09:59Z"},"seven_day":null,
		"limits":[{"kind":"session","group":"session","percent":8,"resets_at":"2026-10-21T22:09:59Z","is_active":true},
		  {"kind":"weekly_all","group":"weekly","percent":7,"resets_at":"2026-10-28T03:00:00Z","is_active":false,"scope":{"model":null,"surface":null}},
		  {"kind":"weekly_odd","group":"weekly","percent":50,"resets_at":"2026-10-28T03:00:00Z"}]}`)
	ws := WindowsOf(raw)
	if len(ws) != 2 || ws[0].Key != "five_hour" || *ws[0].UsedPct != 8.4 || ws[1].Key != "seven_day" || *ws[1].UsedPct != 7 {
		t.Fatalf("%+v", ws)
	}
	if !ws[1].ResetsAt.Equal(time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)) || ws[1].Idle() {
		t.Fatalf("%+v", ws[1])
	}
	// Only limits[] (no top-level windows): an idle session is an idle five_hour.
	ws = WindowsOf(json.RawMessage(`{"limits":[{"kind":"session","group":"session","percent":0,"resets_at":null}]}`))
	if len(ws) != 1 || ws[0].Key != "five_hour" || !ws[0].Idle() {
		t.Fatalf("%+v", ws)
	}
}
