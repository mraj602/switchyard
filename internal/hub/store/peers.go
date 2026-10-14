package store

import (
	"context"
	"encoding/json"
)

// peer_audit (schema v9, peers.md 10): matrix changes, pauses, loop-guard stops, refusals.
// Metadata only: the detail is a small JSON object of ids, versions and reason words, never
// message text.

// PeerAuditRow is one peer_audit row.
type PeerAuditRow struct {
	ID     int64
	At     int64
	Actor  string
	Event  string
	Thread string
	Msg    string
	Detail json.RawMessage
}

// InsertPeerAudit adds a row inside a write transaction. detail nil is "{}".
func (tx *Tx) InsertPeerAudit(r PeerAuditRow) error {
	d := r.Detail
	if len(d) == 0 {
		d = json.RawMessage("{}")
	}
	_, err := tx.exec(`INSERT INTO peer_audit (at, actor, event, thread_id, msg_id, detail) VALUES (?, ?, ?, nullif(?, ''), nullif(?, ''), ?)`,
		r.At, r.Actor, r.Event, r.Thread, r.Msg, string(d))
	return err
}

// PeerAudit lists rows at or after sinceMS, newest first, at most limit.
func (s *Store) PeerAudit(ctx context.Context, sinceMS int64, limit int) ([]PeerAuditRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.r.QueryContext(ctx, `SELECT id, at, actor, event, coalesce(thread_id, ''), coalesce(msg_id, ''), detail
		FROM peer_audit WHERE at >= ? ORDER BY at DESC, id DESC LIMIT ?`, sinceMS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerAuditRow
	for rows.Next() {
		var r PeerAuditRow
		var d string
		if err := rows.Scan(&r.ID, &r.At, &r.Actor, &r.Event, &r.Thread, &r.Msg, &d); err != nil {
			return nil, err
		}
		r.Detail = json.RawMessage(d)
		out = append(out, r)
	}
	return out, rows.Err()
}
