package hubsrc

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/board"
)

// Ended sessions older than EndedWithin leave the folded state, and the slice is capped.
func TestApplySessionPrunesAndCaps(t *testing.T) {
	now := time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
	st := &state{ov: &wireOverview{}}
	old := now.Add(-board.EndedWithin - time.Hour)
	st.sessions = []wireSession{
		{ID: "old-ended", State: "ended", StateSince: old},
		{ID: "old-working", State: "working", StateSince: old},
		{ID: "fresh-ended", State: "gone", StateSince: now.Add(-time.Hour)},
	}
	st.applySession(wireSession{ID: "new", State: "working", StateSince: now}, true, now)
	var ids []string
	for _, x := range st.sessions {
		ids = append(ids, x.ID)
	}
	if got := strings.Join(ids, ","); strings.Contains(got, "old-ended") || !strings.Contains(got, "old-working") || !strings.Contains(got, "fresh-ended") {
		t.Errorf("after prune: %s", got)
	}
	for i := 0; i < MaxSessions+50; i++ {
		st.applySession(wireSession{ID: "s" + strconv.Itoa(i), State: "working", StateSince: now.Add(time.Duration(i) * time.Second)}, true, now)
	}
	if len(st.sessions) > MaxSessions {
		t.Errorf("%d sessions kept, cap %d", len(st.sessions), MaxSessions)
	}
}

// fetchAll follows `next` past the first page.
func TestFetchFollowsNextPage(t *testing.T) {
	h := newFakeHub(t)
	base := time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)
	h.page2 = []map[string]any{
		sess("p2a", "working", "", false, "s_home", "home", "#3b82f6", "m_me", "laptop", "docs", base, ""),
	}
	_, ch, _ := startSource(t, h, nil)
	v := waitView(t, ch, 3*time.Second, isLive)
	if _, ok := v.Rows["p2a"]; !ok {
		t.Errorf("the second page's session is missing: %v", v.Rows)
	}
}

// A control byte in an event id is ignored: it would otherwise be sent back as a header.
func TestSSEIgnoresControlByteIDs(t *testing.T) {
	evs := collect(t, strings.NewReader("id: 5\ndata: a\n\nid: 6\x01x\ndata: b\n\nid: 7\x7f\ndata: c\n\n"))
	if len(evs) != 3 {
		t.Fatalf("events %+v", evs)
	}
	for _, e := range evs[1:] {
		if e.ID != "5" {
			t.Errorf("id %q, want the last good id 5", e.ID)
		}
	}
}
