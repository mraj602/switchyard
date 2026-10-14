package rules

import (
	"fmt"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// ParseHHMM parses "HH:MM" (00:00-23:59) into minutes after midnight.
func ParseHHMM(s string) (int, error) {
	var h, m int
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("time %q is not HH:MM", s)
	}
	if _, err := fmt.Sscanf(s, "%02d:%02d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("time %q is not HH:MM", s)
	}
	return h*60 + m, nil
}

// Location loads an IANA zone; an empty or unknown name is UTC.
func Location(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ValidateQuiet checks a quiet-hours object (api.yaml QuietHours).
func ValidateQuiet(q model.QuietHours) error {
	if _, err := ParseHHMM(q.Start); err != nil {
		return err
	}
	if _, err := ParseHHMM(q.End); err != nil {
		return err
	}
	if q.TZ != "" {
		if _, err := time.LoadLocation(q.TZ); err != nil {
			return fmt.Errorf("unknown time zone %q", q.TZ)
		}
	}
	return nil
}

// localAt is the instant of minute-of-day mins on the calendar day of d in loc. A local
// time that does not exist (the spring-forward gap) moves forward by the gap, as push.md
// 5.2 asks; an ambiguous one (the autumn repeat) is its first occurrence.
func localAt(d time.Time, mins int, loc *time.Location) time.Time {
	y, mo, dd := d.Date()
	h, m := mins/60, mins%60
	t := time.Date(y, mo, dd, h, m, 0, 0, loc)
	if t.Hour() != h || t.Minute() != m {
		// Go normalised a time in a gap; the wall clock reads differently from what was
		// asked. Moving forward means: the same instant measured from the earlier offset
		// plus the gap, which is what wall-clock arithmetic in the new offset gives.
		_, offBefore := time.Date(y, mo, dd, 0, 0, 0, 0, loc).Zone()
		t = time.Date(y, mo, dd, h, m, 0, 0, time.FixedZone("", offBefore)).In(loc)
		return t
	}
	// time.Date does not promise which occurrence of a repeated time it returns: take the
	// first one (shifts are 1 h, or 30 min on Lord Howe).
	for _, back := range []time.Duration{time.Hour, 30 * time.Minute} {
		if e := t.Add(-back); e.Hour() == h && e.Minute() == m && e.Day() == dd {
			return e
		}
	}
	return t
}

// QuietWindow reports whether quiet hours q are active at t and, if so, when they end.
// start > end spans midnight; start == end (or disabled) is never active. Inclusive start,
// exclusive end, evaluated in q.TZ (UTC when empty).
func QuietWindow(q model.QuietHours, t time.Time) (active bool, end time.Time) {
	if !q.Enabled {
		return false, time.Time{}
	}
	s, err1 := ParseHHMM(q.Start)
	e, err2 := ParseHHMM(q.End)
	if err1 != nil || err2 != nil || s == e {
		return false, time.Time{}
	}
	loc := Location(q.TZ)
	lt := t.In(loc)
	// The period that started on the previous day or today.
	for _, back := range []int{1, 0} {
		day := time.Date(lt.Year(), lt.Month(), lt.Day()-back, 12, 0, 0, 0, loc)
		from := localAt(day, s, loc)
		endDay := day
		if s > e {
			endDay = time.Date(day.Year(), day.Month(), day.Day()+1, 12, 0, 0, 0, loc)
		}
		to := localAt(endDay, e, loc)
		if !t.Before(from) && t.Before(to) {
			return true, to
		}
	}
	return false, time.Time{}
}

// Holds reports whether quiet hours hold a push of a rule with bypassQuiet at t, and when
// they end (push.md 5.2: bypass_quiet rules pass unless the quiet hours say critical: false).
func Holds(q model.QuietHours, bypassQuiet bool, t time.Time) (bool, time.Time) {
	active, end := QuietWindow(q, t)
	if !active {
		return false, time.Time{}
	}
	if bypassQuiet && q.LetCriticalThrough() {
		return false, time.Time{}
	}
	return true, end
}
