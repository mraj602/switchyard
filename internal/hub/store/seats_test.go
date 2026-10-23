package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSeatLookSources(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var seat string
	if err := s.Write(ctx, func(tx *Tx) (err error) {
		seat, err = tx.UpsertSeat(SeatKey{Account: "a", Org: "o"}, 1_800_000_000_000)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	look := func() (label, colour, src string) {
		var l, c, k *string
		if err := s.r.QueryRowContext(ctx, `SELECT label, colour, look_src FROM seat WHERE id = ?`, seat).Scan(&l, &c, &k); err != nil {
			t.Fatal(err)
		}
		str := func(p *string) string {
			if p == nil {
				return "<nil>"
			}
			return *p
		}
		return str(l), str(c), str(k)
	}
	write := func(fn func(tx *Tx) error) error { return s.Write(ctx, fn) }
	if l, c, k := look(); l != "<nil>" || c != "<nil>" || k != "<nil>" {
		t.Fatalf("new seat: %s %s %s", l, c, k)
	}

	// An invalid slot label is ignored and leaves look_src NULL.
	_ = write(func(tx *Tx) error { _, err := tx.SeatSlotLook(seat, "Bad Label", ""); return err })
	if _, _, k := look(); k != "<nil>" {
		t.Fatalf("invalid label set look_src %s", k)
	}
	// A slot label sets 'slot'.
	_ = write(func(tx *Tx) error { _, err := tx.SeatSlotLook(seat, "slot-a", "#111111"); return err })
	if l, c, k := look(); l != "slot-a" || c != "#111111" || k != "slot" {
		t.Fatalf("after slot: %s %s %s", l, c, k)
	}
	// A UI patch sets 'ui'; slot_info then no longer changes it.
	lbl := "ui-name"
	if err := write(func(tx *Tx) error { return tx.PatchSeat(seat, SeatPatch{LabelSet: true, Label: &lbl}) }); err != nil {
		t.Fatal(err)
	}
	_ = write(func(tx *Tx) error { _, err := tx.SeatSlotLook(seat, "slot-b", "#333333"); return err })
	if l, c, k := look(); l != "ui-name" || c != "#111111" || k != "ui" {
		t.Fatalf("after ui patch: %s %s %s", l, c, k)
	}

	// Validation and unknown seat.
	bad := "No"
	if err := write(func(tx *Tx) error { return tx.PatchSeat(seat, SeatPatch{LabelSet: true, Label: &bad}) }); !errors.Is(err, ErrBadSeatPatch) {
		t.Fatalf("bad label: %v", err)
	}
	if err := write(func(tx *Tx) error { return tx.PatchSeat("seat_none", SeatPatch{LabelSet: true, Label: &lbl}) }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown seat: %v", err)
	}
}
