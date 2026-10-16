package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestReorderSeats(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ids []string
	if err := s.Write(ctx, func(tx *Tx) error {
		for _, a := range []string{"a", "b", "c"} {
			id, err := tx.UpsertSeat(SeatKey{Account: a, Org: "o"}, 1_800_000_000_000)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	order := func() []string {
		rows, err := s.Seats(ctx, true, s.now())
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	if !slices.Equal(order(), ids) {
		t.Fatalf("default order %v, want %v", order(), ids)
	}
	var got []string
	if err := s.Write(ctx, func(tx *Tx) (err error) {
		got, err = tx.ReorderSeats([]string{ids[2], ids[0]})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{ids[2], ids[0], ids[1]}
	if !slices.Equal(got, want) || !slices.Equal(order(), want) {
		t.Fatalf("got %v, stored %v, want %v", got, order(), want)
	}
	for _, bad := range [][]string{{ids[0], ids[0]}, {"seat_nope"}} {
		err := s.Write(ctx, func(tx *Tx) error { _, err := tx.ReorderSeats(bad); return err })
		if !errors.Is(err, ErrBadSeatOrder) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	if !slices.Equal(order(), want) {
		t.Fatalf("a refused order changed the seats: %v", order())
	}
	// A seat created later goes last.
	var late string
	if err := s.Write(ctx, func(tx *Tx) (err error) {
		late, err = tx.UpsertSeat(SeatKey{Account: "d", Org: "o"}, 1_800_000_000_001)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if o := order(); o[len(o)-1] != late {
		t.Fatalf("new seat not last: %v", o)
	}
}
