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
