package store

import (
	"errors"
	"fmt"
)

// ErrBadSeatOrder is returned (wrapped) for an invalid seat order (PUT /v1/seats/order).
var ErrBadSeatOrder = errors.New("invalid seat order")

// MaxSeatOrder bounds one PUT /v1/seats/order list.
const MaxSeatOrder = 500

// ReorderSeats puts the listed seats first, in that order, and keeps every other seat after
// them in its current order; then it renumbers all seats 0..n-1 (seat.sort_order), so the
// positions are unique afterwards. The list may be partial (hidden seats, or a seat that
// appeared after the caller last looked), but every id must exist and none may repeat. It
// returns every seat id in the new order.
func (tx *Tx) ReorderSeats(ids []string) ([]string, error) {
	if len(ids) > MaxSeatOrder {
		return nil, fmt.Errorf("%w: at most %d ids", ErrBadSeatOrder, MaxSeatOrder)
	}
	rows, err := tx.QueryContext(tx.ctx, `SELECT id FROM seat ORDER BY sort_order, created_at, id`)
	if err != nil {
		return nil, err
	}
	var current []string
	known := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		current = append(current, id)
		known[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !known[id] {
			return nil, fmt.Errorf("%w: unknown seat %q", ErrBadSeatOrder, id)
		}
		if seen[id] {
			return nil, fmt.Errorf("%w: duplicate seat %q", ErrBadSeatOrder, id)
		}
		seen[id] = true
	}
	all := append([]string{}, ids...)
	for _, id := range current {
		if !seen[id] {
			all = append(all, id)
		}
	}
	for i, id := range all {
		if _, err := tx.exec(`UPDATE seat SET sort_order = ? WHERE id = ? AND sort_order <> ?`, i, id, i); err != nil {
			return nil, err
		}
	}
	return all, nil
}
