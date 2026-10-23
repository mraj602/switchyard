package store

import (
	"database/sql"
	"errors"
)

// LimitSampleRow is one limit_sample row.
type LimitSampleRow struct {
	SeatID, Key, Source       string
	TS                        int64
	UsedPct                   *float64
	ResetsAt                  int64 // 0 = unknown
	UsedDollars, LimitDollars *float64
	SessionID, RecordID       string
}

// ResetTolerance is how far apart two resets_at values can be and still name the same
// window: claude.ai reports a slightly different sub-second reset on every poll, and the
// status line rounds to seconds.
const ResetTolerance = 5 * 60 * 1000

// AddSample stores a limit sample (ignored if the same seat, key, ts and source is already
// stored) and folds it into its usage_window: peak, final (newest sample), sample count.
// windowMS is the window's length (0 = unknown, then started_at is the first sample).
// Windows of the same seat and key that reset before this sample get unused_pct.
func (tx *Tx) AddSample(s LimitSampleRow, windowMS int64) (inserted bool, err error) {
	res, err := tx.exec(`INSERT INTO limit_sample (seat_id, window_key, ts, source, used_pct, resets_at, used_dollars, limit_dollars, session_id, record_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		s.SeatID, s.Key, s.TS, s.Source, s.UsedPct, nullInt(s.ResetsAt), s.UsedDollars, s.LimitDollars, nullStr(s.SessionID), s.RecordID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if s.ResetsAt == 0 {
		return true, nil
	}
	resets := s.ResetsAt
	var found int64
	err = tx.queryRow(`SELECT resets_at FROM usage_window WHERE seat_id = ? AND window_key = ? AND resets_at BETWEEN ? AND ?
		ORDER BY abs(resets_at - ?) LIMIT 1`, s.SeatID, s.Key, resets-ResetTolerance, resets+ResetTolerance, resets).Scan(&found)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		resets = roundMinute(resets)
		started := s.TS
		if windowMS > 0 {
			started = resets - windowMS
		}
		if _, err := tx.exec(`INSERT INTO usage_window (seat_id, window_key, resets_at, started_at, peak_pct, final_pct, samples, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?) ON CONFLICT DO NOTHING`, s.SeatID, s.Key, resets, started, s.UsedPct, s.UsedPct, s.TS); err != nil {
			return true, err
		}
	case err != nil:
		return true, err
	default:
		resets = found
		if _, err := tx.exec(`UPDATE usage_window SET
			peak_pct = CASE WHEN ? IS NULL THEN peak_pct WHEN peak_pct IS NULL OR ? > peak_pct THEN ? ELSE peak_pct END,
			final_pct = CASE WHEN ? >= updated_at THEN coalesce(?, final_pct) ELSE final_pct END,
			started_at = CASE WHEN ? = 0 AND ? < started_at THEN ? ELSE started_at END,
			updated_at = max(updated_at, ?),
			samples = samples + 1
			WHERE seat_id = ? AND window_key = ? AND resets_at = ?`,
			s.UsedPct, s.UsedPct, s.UsedPct, s.TS, s.UsedPct, windowMS, s.TS, s.TS, s.TS, s.SeatID, s.Key, resets); err != nil {
			return true, err
		}
		// A late sample can change the final of a window that already has unused_pct.
		if _, err := tx.exec(`UPDATE usage_window SET unused_pct = max(0, 100 - final_pct)
			WHERE seat_id = ? AND window_key = ? AND resets_at = ? AND unused_pct IS NOT NULL AND final_pct IS NOT NULL`,
			s.SeatID, s.Key, resets); err != nil {
			return true, err
		}
	}
	// Earlier windows of this key that have reset by now: unused = 100 - final.
	_, err = tx.exec(`UPDATE usage_window SET unused_pct = max(0, 100 - final_pct)
		WHERE seat_id = ? AND window_key = ? AND resets_at < ? AND resets_at <= ? AND final_pct IS NOT NULL`,
		s.SeatID, s.Key, resets, s.TS)
	return true, err
}

func roundMinute(ms int64) int64 { return (ms + 30_000) / 60_000 * 60_000 }

// InsertWebUsage keeps a claude.ai usage response whole.
func (tx *Tx) InsertWebUsage(seat string, ts int64, tab, raw, recordID string) (bool, error) {
	res, err := tx.exec(`INSERT INTO web_usage (seat_id, ts, tab_id, raw, record_id) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		seat, ts, nullStr(tab), raw, recordID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TokenRowIn is one token row to merge.
type TokenRowIn struct {
	Key, SessionID, SeatID, Project, Model, RecordID string
	TS                                               int64
	Input, Output, Cache5m, Cache1h, CacheRead       int64
	Thinking                                         int64
	Sidechain                                        bool
}

// MergeTokenRow upserts a token row keeping the largest value of each counter (token
// upsert); record_id moves to the record that raised a count.
func (tx *Tx) MergeTokenRow(r TokenRowIn) error {
	side := 0
	if r.Sidechain {
		side = 1
	}
	_, err := tx.exec(`INSERT INTO token_row (key, session_id, seat_id, ts, project, model, input, output, cache_5m, cache_1h, cache_read, thinking, sidechain, record_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
		  record_id = CASE WHEN excluded.input > input OR excluded.output > output OR excluded.cache_5m > cache_5m
		    OR excluded.cache_1h > cache_1h OR excluded.cache_read > cache_read OR excluded.thinking > thinking
		    THEN excluded.record_id ELSE record_id END,
		  input = max(input, excluded.input), output = max(output, excluded.output),
		  cache_5m = max(cache_5m, excluded.cache_5m), cache_1h = max(cache_1h, excluded.cache_1h),
		  cache_read = max(cache_read, excluded.cache_read), thinking = max(thinking, excluded.thinking),
		  sidechain = max(sidechain, excluded.sidechain),
		  seat_id = coalesce(seat_id, excluded.seat_id), project = coalesce(project, excluded.project),
		  model = coalesce(model, excluded.model)`,
		r.Key, r.SessionID, nullStr(r.SeatID), r.TS, nullStr(r.Project), nullStr(r.Model), r.Input, r.Output, r.Cache5m,
		r.Cache1h, r.CacheRead, r.Thinking, side, r.RecordID)
	return err
}

// SessionSeat returns a session's seat ("" when unknown or no such session).
func (tx *Tx) SessionSeat(id string) (string, error) {
	var seat sql.NullString
	err := tx.queryRow(`SELECT seat_id FROM session WHERE id = ?`, id).Scan(&seat)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return seat.String, err
}

// LimitHitRow is one limit_hit row.
type LimitHitRow struct {
	SessionID, Type, Status, SeatID string
	TS, ResetsAt                    int64
}

// InsertLimitHit stores a "you've hit your limit" reply once.
func (tx *Tx) InsertLimitHit(h LimitHitRow) error {
	_, err := tx.exec(`INSERT INTO limit_hit (session_id, ts, type, status, resets_at, seat_id) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT DO UPDATE SET seat_id = coalesce(seat_id, excluded.seat_id)`,
		h.SessionID, h.TS, nullStr(h.Type), nullStr(h.Status), nullInt(h.ResetsAt), nullStr(h.SeatID))
	return err
}

// HistoryDayRow is one history_day row. Nil counters are left as they are.
type HistoryDayRow struct {
	SlotID                                int64
	Day, Model, SeatID, RecordID          string
	Tokens, Messages, Sessions, ToolCalls *int64
}

// UpsertHistoryDay stores Claude Code's own daily totals.
func (tx *Tx) UpsertHistoryDay(h HistoryDayRow) error {
	_, err := tx.exec(`INSERT INTO history_day (slot_id, day, model, seat_id, tokens, messages, sessions, tool_calls, record_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(slot_id, day, model) DO UPDATE SET
		seat_id = coalesce(excluded.seat_id, seat_id), tokens = coalesce(excluded.tokens, tokens),
		messages = coalesce(excluded.messages, messages), sessions = coalesce(excluded.sessions, sessions),
		tool_calls = coalesce(excluded.tool_calls, tool_calls), record_id = excluded.record_id`,
		h.SlotID, h.Day, h.Model, nullStr(h.SeatID), h.Tokens, h.Messages, h.Sessions, h.ToolCalls, h.RecordID)
	return err
}
