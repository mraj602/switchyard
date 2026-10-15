// Package localtime formats times for a person reading a terminal: in the local time zone,
// with the zone named ("2026-10-12 14:02 IST"). Text output of `sy` commands goes through
// it; `--json` output keeps RFC 3339 UTC and never does.
package localtime

import (
	"time"
)

// Layout is the one text layout: date, minutes, zone abbreviation (or offset).
const Layout = "2006-01-02 15:04 MST"

// Zone is the zone text output uses. Tests set it; everyone else leaves it.
var Zone = time.Local

// Format renders t in Zone; the zero time is "-".
func Format(t time.Time) string { return FormatIn(t, nil) }

// FormatIn renders t in loc (nil = Zone); the zero time is "-".
func FormatIn(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "-"
	}
	if loc == nil {
		loc = Zone
	}
	return t.In(loc).Format(Layout)
}

// MS renders unix milliseconds; 0 is "-".
func MS(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return Format(time.UnixMilli(ms))
}

// String renders an RFC 3339 time as it comes in JSON from the hub or the agent. "" is "-";
// a value that does not parse is returned as it is.
func String(s string) string { return StringIn(s, nil) }

// StringIn is String in loc (nil = Zone).
func StringIn(s string, loc *time.Location) string {
	if s == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return FormatIn(t, loc)
}

// Ptr is String for an optional value; nil is "-".
func Ptr(s *string) string {
	if s == nil {
		return "-"
	}
	return String(*s)
}
