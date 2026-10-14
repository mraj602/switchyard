package engine_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// Phase 8 (push.md 12.4): the engine stores Result.Ref in delivery.ticket in the same
// transaction as the result. A sender that returns no Ref (Web Push, ntfy) leaves it NULL.
func TestEngineStoresTicket(t *testing.T) {
	h := newHarness(t)
	other := h.addTarget(h.phone, model.KindWebPush, "")
	h.wp.script(h.target, model.Result{Outcome: model.OutcomeSent, HTTPStatus: 200, Ref: "t1"})
	h.askQ("s1", 0)
	h.advanceTo(61 * time.Second)
	if n := len(h.wp.sends()); n != 2 {
		t.Fatalf("%d sends, want 2%s", n, h.dump())
	}
	tickets := map[string]sql.NullString{}
	var receipts int
	h.write(func(tx *store.Tx) error {
		rows, err := tx.QueryContext(tx.Ctx(), `SELECT target_id, ticket FROM delivery WHERE status = 'sent'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var tk sql.NullString
			if err := rows.Scan(&id, &tk); err != nil {
				return err
			}
			tickets[id] = tk
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRowContext(tx.Ctx(), `SELECT count(*) FROM delivery WHERE receipt_at IS NOT NULL`).Scan(&receipts)
	})
	if tk := tickets[h.target]; !tk.Valid || tk.String != "t1" {
		t.Errorf("ticket of the Ref target: %+v, want t1", tk)
	}
	if tk, ok := tickets[other]; !ok || tk.Valid {
		t.Errorf("ticket of the target without Ref: %+v (present %v), want NULL", tk, ok)
	}
	if receipts != 0 {
		t.Errorf("receipt_at set on %d deliveries; only the expo receipts poller sets it", receipts)
	}
}
