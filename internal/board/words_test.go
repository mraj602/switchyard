package board

import (
	"strings"
	"testing"
	"time"
)

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second: "0s", 0: "0s", 40 * time.Second: "40s", 59 * time.Second: "59s", 2 * time.Minute: "2m",
		59*time.Minute + 59*time.Second: "59m", time.Hour: "1h", 3 * time.Hour: "3h", 47 * time.Hour: "47h",
		48 * time.Hour: "2d", 50 * time.Hour: "2d", 10 * 24 * time.Hour: "10d",
	} {
		if got := Age(d); got != want {
			t.Errorf("Age(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestClockText(t *testing.T) {
	now := time.Date(2026, 10, 20, 14, 6, 0, 0, time.UTC)
	ny := time.FixedZone("x", -5*3600)
	for _, c := range []struct {
		t    time.Time
		loc  *time.Location
		want string
	}{
		{time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC), time.UTC, "17:00"},
		{time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC), ny, "12:00"},          // local time of the viewer
		{time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC), time.UTC, "03:00"},     // under 18 h away
		{time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC), time.UTC, "Thu 03:00"}, // another day
		{time.Date(2026, 10, 20, 9, 5, 0, 0, time.UTC), time.UTC, "09:05"},     // today, past
	} {
		if got := ClockText(c.t, now, c.loc); got != c.want {
			t.Errorf("ClockText(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestContextLevel(t *testing.T) {
	for pct, want := range map[int]string{0: ContextOK, 79: ContextOK, 80: ContextWarn, 91: ContextWarn, 92: ContextHigh, 100: ContextHigh} {
		if got := ContextLevel(pct); got != want {
			t.Errorf("ContextLevel(%d) = %s, want %s", pct, got, want)
		}
	}
	for f, want := range map[float64]int{-3: 0, 0.4: 0, 0.5: 1, 79.6: 80, 150: 100} {
		if got := pctInt(f); got != want {
			t.Errorf("pctInt(%v) = %d, want %d", f, got, want)
		}
	}
}

func TestWindowText(t *testing.T) {
	now := time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
	r := time.Date(2026, 10, 20, 15, 10, 0, 0, time.UTC)
	if got := WindowText(41, &r, now, time.UTC, true); got != "41% · 15:10" {
		t.Error(got)
	}
	if got := WindowText(30, &r, now, time.UTC, false); got != "30%" {
		t.Error(got)
	}
	if got := WindowText(30, nil, now, time.UTC, true); got != "30%" {
		t.Error(got)
	}
}

func TestCleanAndLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                     "plain",
		"a\x1b]2;x\x07b":            "ab",
		"a\x1b]2;x\x1b\\b":          "ab",
		"a\x1b[31mred\x1b[0m":       "ared",
		"api\r\nweb":                "api  web",
		"x\u202eevil\u202c":         "xevil",
		"\u2066a\u2069":             "a",
		"a\u009d0;t\u0007b":         "a0;tb",
		"tab\there":                 "tab here",
		"\x00\x01\x7f":              "",
		"bad\xffutf8":               "badutf8",
		"zero\u200bwidth\ufeff":     "zerowidth",
		"line\u2028sep":             "line sep",
		"unterminated \x1b]2;title": "unterminated ",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Line("  a \n\n b\t c  "); got != "a b c" {
		t.Errorf("Line = %q", got)
	}
}

func TestWidthAndCut(t *testing.T) {
	for s, want := range map[string]int{"abc": 3, "": 0, "日本語": 6, "a日b": 4, "é": 1, "ｱ": 1, "ａ": 2, "😀": 2, "한글": 4, "—": 1} {
		if got := Width(s); got != want {
			t.Errorf("Width(%q) = %d, want %d", s, got, want)
		}
	}
	for _, c := range []struct {
		s    string
		w    int
		want string
	}{
		{"hello", 5, "hello"}, {"hello", 4, "hel…"}, {"hello", 1, "…"}, {"hello", 0, ""},
		{"日本語です", 6, "日本…"}, // 2+2+1 = 5 columns, a wide char never split
		{"日本語です", 5, "日本…"},
		{"日本語です", 4, "日…"},
		{"日本語", 6, "日本語"},
		{"ab cd", 3, "ab…"}, // no trailing space before the ellipsis
	} {
		got := Cut(c.s, c.w)
		if got != c.want || Width(got) > c.w {
			t.Errorf("Cut(%q, %d) = %q (%d cols), want %q", c.s, c.w, got, Width(got), c.want)
		}
	}
	if got := Cut(strings.Repeat("x", 500), 200); Width(got) != 200 {
		t.Errorf("cut width %d", Width(got))
	}
}

func TestRecap(t *testing.T) {
	if got := Recap("Done.\n```\ncode\n```\nTwo\n\nlines", 100); got != "Done. Two lines" {
		t.Errorf("%q", got)
	}
	if got := Recap("Before ```unterminated code", 100); got != "Before" {
		t.Errorf("%q", got)
	}
}
