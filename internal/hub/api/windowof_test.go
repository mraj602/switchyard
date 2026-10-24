package api

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
)

// The history endpoint keeps the real used_pct of a past window; only the current views
// (overview, stream) show a reset window as 0% used.
func TestWindowOfBlanksUsedPctOnlyForCurrentViews(t *testing.T) {
	final := 73.0
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	w := store.WindowRow{Key: "five_hour", ResetsAt: now.Add(-time.Hour).UnixMilli(), StartedAt: now.Add(-6 * time.Hour).UnixMilli(), Final: &final}

	hist := windowOf(w, now, false)
	if hist.UsedPct == nil || *hist.UsedPct != 73 {
		t.Errorf("history used_pct = %v, want 73", hist.UsedPct)
	}
	if hist.UnusedPct == nil || *hist.UnusedPct != 27 {
		t.Errorf("history unused_pct = %v, want 27", hist.UnusedPct)
	}
	cur := windowOf(w, now, true)
	if cur.UsedPct == nil || *cur.UsedPct != 0 {
		t.Errorf("current used_pct = %v, want 0", cur.UsedPct)
	}
}
