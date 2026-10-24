package peers_test

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

func TestAwaitingReply(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	mk := func(mod func(*peers.Thread)) *peers.ThreadSet {
		s := &peers.ThreadSet{V: 1}
		th := s.Start("a", "b", "x", "y", now.Add(-time.Minute))
		th.RecordMessageFrom("a", now.Add(-time.Minute))
		if mod != nil {
			mod(th)
		}
		return s
	}
	paused := now
	closed := now
	cases := []struct {
		name  string
		set   *peers.ThreadSet
		who   string
		human time.Time
		want  bool
	}{
		{"sent the last message", mk(nil), "a", time.Time{}, true},
		{"the other side", mk(nil), "b", time.Time{}, false},
		{"replied", mk(func(t *peers.Thread) { t.RecordMessageFrom("b", now) }), "a", time.Time{}, false},
		{"a person spoke since", mk(nil), "a", now, false},
		{"sent after the person", mk(nil), "a", now.Add(-time.Hour), true},
		{"loop guard stopped it", mk(func(t *peers.Thread) { t.PausedAt = &paused }), "a", time.Time{}, false},
		{"closed", mk(func(t *peers.Thread) { t.ClosedAt = &closed }), "a", time.Time{}, false},
		{"idle too long", mk(func(t *peers.Thread) { t.LastAt = now.Add(-peers.ThreadIdle - time.Second) }), "a", time.Time{}, false},
		{"older file without last_from", mk(func(t *peers.Thread) { t.LastFrom = "" }), "a", time.Time{}, false},
	}
	for _, c := range cases {
		if got := c.set.AwaitingReply(c.who, c.human, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
