package directpush

import (
	"fmt"
	"runtime"
	"time"
)

// The quiet-hours check is push/rules' QuietWindow and Holds, copied because a client
// package may not import push/rules (ARCHITECTURE.md). TestQuietMatchesHub keeps them equal.

func isWindows() bool { return runtime.GOOS == "windows" }

func parseHHMM(s string) (int, error) {
	var h, m int
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("time %q is not HH:MM", s)
	}
	if _, err := fmt.Sscanf(s, "%02d:%02d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("time %q is not HH:MM", s)
	}
	return h*60 + m, nil
}

func location(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func localAt(d time.Time, mins int, loc *time.Location) time.Time {
	y, mo, dd := d.Date()
	h, m := mins/60, mins%60
	t := time.Date(y, mo, dd, h, m, 0, 0, loc)
	if t.Hour() != h || t.Minute() != m {
		_, offBefore := time.Date(y, mo, dd, 0, 0, 0, 0, loc).Zone()
		return time.Date(y, mo, dd, h, m, 0, 0, time.FixedZone("", offBefore)).In(loc)
	}
	for _, back := range []time.Duration{time.Hour, 30 * time.Minute} {
		if e := t.Add(-back); e.Hour() == h && e.Minute() == m && e.Day() == dd {
			return e
		}
	}
	return t
}

// quietActive reports whether quiet hours q are active at t (inclusive start, exclusive end,
// in q.TZ or UTC).
func quietActive(q QuietHours, t time.Time) bool {
	if !q.Enabled {
		return false
	}
	s, err1 := parseHHMM(q.Start)
	e, err2 := parseHHMM(q.End)
	if err1 != nil || err2 != nil || s == e {
		return false
	}
	loc := location(q.TZ)
	lt := t.In(loc)
	for _, back := range []int{1, 0} {
		day := time.Date(lt.Year(), lt.Month(), lt.Day()-back, 12, 0, 0, 0, loc)
		from := localAt(day, s, loc)
		endDay := day
		if s > e {
			endDay = time.Date(day.Year(), day.Month(), day.Day()+1, 12, 0, 0, 0, loc)
		}
		to := localAt(endDay, e, loc)
		if !t.Before(from) && t.Before(to) {
			return true
		}
	}
	return false
}

// quietHolds reports whether quiet hours hold a push of a rule with bypassQuiet at t
// (push.md 5.2: bypass_quiet passes unless critical is false).
func quietHolds(q QuietHours, bypassQuiet bool, t time.Time) bool {
	if !quietActive(q, t) {
		return false
	}
	return !(bypassQuiet && (q.Critical == nil || *q.Critical))
}
