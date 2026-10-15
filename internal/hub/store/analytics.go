package store

import (
	"context"
	"database/sql"
)

// Reads for GET /v1/analytics/{report} (docs/spec/analytics.md). Plain aggregates over
// token_row and friends; the handler prices and shapes them. rollup_daily is not used: a
// day depends on the hub's time zone, so the handler folds 15-minute buckets (every
// real-world zone offset is a multiple of 15 minutes) into days itself.

// SeatUnattributed is the seat filter value for rows with no seat.
const SeatUnattributed = "unattributed"

// BucketMS is the width of a TokenBucket.
const BucketMS = 15 * 60 * 1000

// AnalyticsFilter selects rows with FromMS <= ts < ToMS. Seat "" = every seat,
// SeatUnattributed = rows with no seat, otherwise one seat id.
type AnalyticsFilter struct {
	FromMS, ToMS int64
	Seat         string
}

// seatClause is the SQL for the seat filter on column col, plus its argument.
func (f AnalyticsFilter) seatClause(col string) (string, []any) {
	switch f.Seat {
	case "":
		return "", nil
	case SeatUnattributed:
		return " AND " + col + " IS NULL", nil
	}
	return " AND " + col + " = ?", []any{f.Seat}
}

// Counts are token counters (summed).
type Counts struct {
	Input, Output, Cache5m, Cache1h, CacheRead, Replies int64
}

// TokenBucket is the token_row sums of one 15-minute bucket, seat and model.
type TokenBucket struct {
	StartMS int64
	Seat    string // "" = Unattributed
	Model   string // "" = unknown
	Counts
}

// TokenBuckets sums token_row per 15-minute bucket, seat and model.
func (s *Store) TokenBuckets(ctx context.Context, f AnalyticsFilter) ([]TokenBucket, error) {
	sc, args := f.seatClause("seat_id")
	rows, err := s.r.QueryContext(ctx, `SELECT ts / ? * ?, coalesce(seat_id, ''), coalesce(model, ''),
		sum(input), sum(output), sum(cache_5m), sum(cache_1h), sum(cache_read), count(*)
		FROM token_row WHERE ts >= ? AND ts < ?`+sc+` GROUP BY 1, 2, 3`,
		append([]any{int64(BucketMS), int64(BucketMS), f.FromMS, f.ToMS}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenBucket
	for rows.Next() {
		var b TokenBucket
		c := &b.Counts
		if err := rows.Scan(&b.StartMS, &b.Seat, &b.Model, &c.Input, &c.Output, &c.Cache5m, &c.Cache1h, &c.CacheRead, &c.Replies); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// TokenSession is the token_row sums of one session, project and model.
type TokenSession struct {
	Session string
	Seat    string // "" = Unattributed
	Project string // "" = unknown
	Model   string // "" = unknown
	FirstMS int64
	LastMS  int64
	Counts
}

// TokenSessions sums token_row per session, seat, project and model.
func (s *Store) TokenSessions(ctx context.Context, f AnalyticsFilter) ([]TokenSession, error) {
	sc, args := f.seatClause("seat_id")
	rows, err := s.r.QueryContext(ctx, `SELECT session_id, coalesce(seat_id, ''), coalesce(project, ''), coalesce(model, ''),
		min(ts), max(ts), sum(input), sum(output), sum(cache_5m), sum(cache_1h), sum(cache_read), count(*)
		FROM token_row WHERE ts >= ? AND ts < ?`+sc+` GROUP BY 1, 2, 3, 4`,
		append([]any{f.FromMS, f.ToMS}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenSession
	for rows.Next() {
		var t TokenSession
		c := &t.Counts
		if err := rows.Scan(&t.Session, &t.Seat, &t.Project, &t.Model, &t.FirstMS, &t.LastMS,
			&c.Input, &c.Output, &c.Cache5m, &c.Cache1h, &c.CacheRead, &c.Replies); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FirstTokenMS is the oldest token_row ts (all seats), 0 when there are none.
func (s *Store) FirstTokenMS(ctx context.Context) (int64, error) {
	var ts sql.NullInt64
	if err := s.r.QueryRowContext(ctx, `SELECT min(ts) FROM token_row`).Scan(&ts); err != nil {
		return 0, err
	}
	return ts.Int64, nil
}

// HistoryDay is Claude Code's own total for one day (history_day), summed over models.
type HistoryDay struct {
	Day      string
	Tokens   int64
	Messages int64
}

// HistoryDays sums history_day per day for days in [from, to] ("YYYY-MM-DD").
func (s *Store) HistoryDays(ctx context.Context, from, to, seat string) ([]HistoryDay, error) {
	f := AnalyticsFilter{Seat: seat}
	sc, args := f.seatClause("seat_id")
	rows, err := s.r.QueryContext(ctx, `SELECT day, coalesce(sum(tokens), 0), coalesce(sum(messages), 0)
		FROM history_day WHERE day >= ? AND day <= ?`+sc+` GROUP BY day ORDER BY day`,
		append([]any{from, to}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryDay
	for rows.Next() {
		var d HistoryDay
		if err := rows.Scan(&d.Day, &d.Tokens, &d.Messages); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SeatNames maps every seat id to its label, else its account email, else its id.
func (s *Store) SeatNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT se.id, coalesce(se.label, a.email, se.id)
		FROM seat se LEFT JOIN account a ON a.uuid = se.account_uuid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// SeatExists says whether a seat id is known.
func (s *Store) SeatExists(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM seat WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// WindowSummary is one usage_window row.
type WindowSummary struct {
	Seat, Key           string
	ResetsAt, StartedAt int64
	Peak, Final, Unused *float64
	Samples             int64
}

// UsageWindowsBetween lists limit windows that reset in [FromMS, ToMS), oldest first.
func (s *Store) UsageWindowsBetween(ctx context.Context, f AnalyticsFilter) ([]WindowSummary, error) {
	sc, args := f.seatClause("seat_id")
	rows, err := s.r.QueryContext(ctx, `SELECT seat_id, window_key, resets_at, coalesce(started_at, 0), peak_pct, final_pct, unused_pct, samples
		FROM usage_window WHERE resets_at >= ? AND resets_at < ?`+sc+`
		ORDER BY resets_at, seat_id, window_key`, append([]any{f.FromMS, f.ToMS}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WindowSummary
	for rows.Next() {
		var w WindowSummary
		var peak, final, unused sql.NullFloat64
		if err := rows.Scan(&w.Seat, &w.Key, &w.ResetsAt, &w.StartedAt, &peak, &final, &unused, &w.Samples); err != nil {
			return nil, err
		}
		w.Peak, w.Final, w.Unused = nf(peak), nf(final), nf(unused)
		out = append(out, w)
	}
	return out, rows.Err()
}

// LimitHitsBetween lists "you've hit your limit" replies (SeatID "" = Unattributed) with FromMS <= ts < ToMS, oldest first.
func (s *Store) LimitHitsBetween(ctx context.Context, f AnalyticsFilter) ([]LimitHitRow, error) {
	sc, args := f.seatClause("seat_id")
	rows, err := s.r.QueryContext(ctx, `SELECT session_id, ts, coalesce(seat_id, ''), coalesce(type, ''), coalesce(status, ''), coalesce(resets_at, 0)
		FROM limit_hit WHERE ts >= ? AND ts < ?`+sc+` ORDER BY ts, session_id`, append([]any{f.FromMS, f.ToMS}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LimitHitRow
	for rows.Next() {
		var h LimitHitRow
		if err := rows.Scan(&h.SessionID, &h.TS, &h.SeatID, &h.Type, &h.Status, &h.ResetsAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
