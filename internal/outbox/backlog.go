package outbox

import "time"

// Backlog counts the complete, parsable records past the cursor (day files, then spill
// files) and returns the ts of the oldest one that has a parsable ts ("" if none). It reads
// without logging corrupt lines or changing anything; the drain's hello uses it.
func (o *Outbox) Backlog(cur Cursor) (n int64, oldest string, err error) {
	days, spills, err := o.list()
	if err != nil {
		return 0, "", err
	}
	var best time.Time
	see := func(it Item) {
		n++
		if t, perr := time.Parse(time.RFC3339Nano, it.TS); perr == nil && (oldest == "" || t.Before(best)) {
			best, oldest = t, it.TS
		}
	}
	for _, name := range days {
		if name < cur.File {
			continue
		}
		var from int64
		if name == cur.File {
			from = cur.Offset
		}
		if err := o.readLines(name, from, func(ln line) bool {
			if it, ok := o.parse(name, ln, false, false); ok {
				see(it)
			}
			return true
		}); err != nil {
			return 0, "", err
		}
	}
	for _, name := range spills {
		if err := o.readLines(name, 0, func(ln line) bool {
			if it, ok := o.parse(name, ln, true, false); ok {
				see(it)
			}
			return true
		}); err != nil {
			return 0, "", err
		}
	}
	return n, oldest, nil
}
