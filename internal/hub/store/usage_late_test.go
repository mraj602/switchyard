package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestLateSampleRecomputesUnusedPct(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f := func(v float64) *float64 { return &v }
	h := int64(3600_000)
	r1 := int64(1_800_000_000_000) // window 1 resets
	r2 := r1 + 5*h
	var seat string
	err = s.Write(ctx, func(tx *Tx) error {
		seat, err = tx.UpsertSeat(SeatKey{Account: "a", Org: "o"}, r1-4*h)
		if err != nil {
			return err
		}
		// machine A: window 1 at 40% one hour before reset
		if _, err := tx.AddSample(LimitSampleRow{SeatID: seat, Key: "five_hour", Source: "statusline", TS: r1 - h, UsedPct: f(40), ResetsAt: r1, RecordID: "r1"}, 5*h); err != nil {
			return err
		}
		// machine A: new window sample after reset
		if _, err := tx.AddSample(LimitSampleRow{SeatID: seat, Key: "five_hour", Source: "statusline", TS: r1 + h, UsedPct: f(5), ResetsAt: r2, RecordID: "r2"}, 5*h); err != nil {
			return err
		}
		// machine B flushes late: window 1 at 90% ten minutes before reset
		_, err := tx.AddSample(LimitSampleRow{SeatID: seat, Key: "five_hour", Source: "web", TS: r1 - 10*60_000, UsedPct: f(90), ResetsAt: r1, RecordID: "r3"}, 5*h)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := s.Windows(ctx, seat, nil, time.Time{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		t.Logf("resets=%d final=%v unused=%v", w.ResetsAt, *w.Final, func() any {
			if w.Unused == nil {
				return nil
			}
			return *w.Unused
		}())
		if w.ResetsAt == r1 && w.Unused != nil && *w.Unused != 100-*w.Final {
			t.Errorf("stale unused_pct: final=%v unused=%v", *w.Final, *w.Unused)
		}
	}
}
