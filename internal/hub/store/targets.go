package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// This file is the store side of push channels and targets (push.md 7.1, 9): the channel
// switches, push_target rows, hub_secret and the few setting keys the channels need.

// ErrTargetKind is returned by UpsertTarget for an unknown kind.
var ErrTargetKind = errors.New("store: unknown push channel kind")

// Channel is one row of the channel table (api.yaml Channel).
type Channel struct {
	Kind      string
	Enabled   bool
	Settings  json.RawMessage
	UpdatedAt int64
}

// Channels lists the channel rows by kind.
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT kind, enabled, settings, updated_at FROM channel ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		var set string
		if err := rows.Scan(&c.Kind, &c.Enabled, &set, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Settings = json.RawMessage(set)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChannelByKind returns one channel (ErrNotFound when the kind has no row).
func (s *Store) ChannelByKind(ctx context.Context, kind string) (Channel, error) {
	var c Channel
	var set string
	err := s.r.QueryRowContext(ctx, `SELECT kind, enabled, settings, updated_at FROM channel WHERE kind = ?`, kind).
		Scan(&c.Kind, &c.Enabled, &set, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.Settings = json.RawMessage(set)
	return c, err
}

// SetChannel switches a channel on or off; settings nil keeps the stored object.
func (s *Store) SetChannel(ctx context.Context, kind string, enabled bool, settings json.RawMessage) (Channel, error) {
	err := s.Write(ctx, func(tx *Tx) error {
		var res sql.Result
		var err error
		if settings == nil {
			res, err = tx.exec(`UPDATE channel SET enabled = ?, updated_at = ? WHERE kind = ?`, enabled, tx.NowMS(), kind)
		} else {
			res, err = tx.exec(`UPDATE channel SET enabled = ?, settings = ?, updated_at = ? WHERE kind = ?`,
				enabled, string(settings), tx.NowMS(), kind)
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	return s.ChannelByKind(ctx, kind)
}

// targetCols are the columns scanTarget reads, from push_target aliased t.
const targetCols = `t.id, t.kind, coalesce(t.device_id, ''), coalesce(t.name, ''), t.endpoint, t.keys, coalesce(t.secret, ''),
	t.prefs, t.enabled, t.created_at, t.updated_at, coalesce(t.last_ok_at, 0), coalesce(t.last_error, ''),
	coalesce(t.last_error_at, 0), t.fail_count, coalesce(t.gone_at, 0), t.meta`

func scanTarget(sc interface{ Scan(...any) error }) (model.Target, error) { return scanTargetWith(sc) }

// scanTargetWith scans targetCols followed by extra columns.
func scanTargetWith(sc interface{ Scan(...any) error }, extra ...any) (model.Target, error) {
	var t model.Target
	var keys, prefs, meta sql.NullString
	var created, updated, ok, errAt, gone int64
	err := sc.Scan(append([]any{&t.ID, &t.Kind, &t.DeviceID, &t.Name, &t.Endpoint, &keys, &t.Secret, &prefs, &t.Enabled,
		&created, &updated, &ok, &t.LastError, &errAt, &t.FailCount, &gone, &meta}, extra...)...)
	if err != nil {
		return t, err
	}
	if meta.Valid && meta.String != "" {
		var m model.TargetMeta
		if json.Unmarshal([]byte(meta.String), &m) == nil {
			t.Meta = &m
		}
	}
	t.CreatedAt, t.UpdatedAt, t.LastOKAt, t.LastErrorAt, t.GoneAt =
		model.Time(created), model.Time(updated), model.Time(ok), model.Time(errAt), model.Time(gone)
	if keys.Valid && keys.String != "" {
		var k model.Keys
		if json.Unmarshal([]byte(keys.String), &k) == nil {
			t.Keys = &k
		}
	}
	if prefs.Valid && prefs.String != "" {
		var p model.Prefs
		if json.Unmarshal([]byte(prefs.String), &p) == nil {
			t.Prefs = &p
		}
	}
	return t, nil
}

// TargetByID returns one target including its secret (callers must not serialise it).
func (s *Store) TargetByID(ctx context.Context, id string) (model.Target, error) {
	t, err := scanTarget(s.r.QueryRowContext(ctx, `SELECT `+targetCols+` FROM push_target t WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// TargetByEndpoint returns the target of a kind at an endpoint (ErrNotFound when none).
func (s *Store) TargetByEndpoint(ctx context.Context, kind, endpoint string) (model.Target, error) {
	t, err := scanTarget(s.r.QueryRowContext(ctx, `SELECT `+targetCols+` FROM push_target t WHERE t.kind = ? AND t.endpoint = ?`, kind, endpoint))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// ListTargets lists targets oldest first. deviceID "" lists all; otherwise only that device's.
func (s *Store) ListTargets(ctx context.Context, deviceID string) ([]model.Target, error) {
	q, args := `SELECT `+targetCols+` FROM push_target t`, []any(nil)
	if deviceID != "" {
		q += ` WHERE t.device_id = ?`
		args = append(args, deviceID)
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY t.created_at, t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertTarget stores a target by (kind, endpoint). A new row gets an id and created_at; an
// existing one is re-bound to t.DeviceID, takes the new keys, secret, name and prefs, and is
// revived (enabled, not gone, fail_count 0), which is what a re-subscribing browser needs.
// It returns the stored row. An expo target (push.md 12.7) has the push token as endpoint,
// no keys and no secret, and its meta (platform, app version) is replaced on every upsert.
func (s *Store) UpsertTarget(ctx context.Context, t model.Target) (model.Target, error) {
	if t.Kind != model.KindWebPush && t.Kind != model.KindNtfy && t.Kind != model.KindExpo {
		return model.Target{}, ErrTargetKind
	}
	var keys, prefs, meta any
	if t.Keys != nil {
		b, _ := json.Marshal(t.Keys)
		keys = string(b)
	}
	if t.Prefs != nil {
		b, _ := json.Marshal(t.Prefs)
		prefs = string(b)
	}
	if t.Meta != nil && t.Kind == model.KindExpo {
		b, _ := json.Marshal(t.Meta)
		meta = string(b)
	}
	var id string
	err := s.Write(ctx, func(tx *Tx) error {
		now := tx.NowMS()
		err := tx.queryRow(`SELECT id FROM push_target WHERE kind = ? AND endpoint = ?`, t.Kind, t.Endpoint).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			id = NewID("pt_", time.UnixMilli(now))
			_, err = tx.exec(`INSERT INTO push_target (id, kind, device_id, name, endpoint, keys, secret, prefs, meta, enabled, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
				id, t.Kind, nullStr(t.DeviceID), nullStr(t.Name), t.Endpoint, keys, nullStr(t.Secret), prefs, meta, now, now)
			return err
		case err != nil:
			return err
		}
		_, err = tx.exec(`UPDATE push_target SET device_id = ?, name = coalesce(?, name), keys = ?, secret = ?, prefs = ?, meta = ?,
			enabled = 1, gone_at = NULL, fail_count = 0, last_error = NULL, last_error_at = NULL, updated_at = ? WHERE id = ?`,
			nullStr(t.DeviceID), nullStr(t.Name), keys, nullStr(t.Secret), prefs, meta, now, id)
		return err
	})
	if err != nil {
		return model.Target{}, err
	}
	return s.TargetByID(ctx, id)
}

// TargetPatch is a partial update of a target; nil fields are kept.
type TargetPatch struct {
	Enabled *bool
	Name    *string
	Prefs   *model.Prefs
}

// PatchTarget applies p and returns the stored row. Enabling a gone target does not revive
// it: only a new subscription (UpsertTarget) clears gone_at.
func (s *Store) PatchTarget(ctx context.Context, id string, p TargetPatch) (model.Target, error) {
	err := s.Write(ctx, func(tx *Tx) error {
		var exists int
		if err := tx.queryRow(`SELECT 1 FROM push_target WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		now := tx.NowMS()
		if p.Enabled != nil {
			if _, err := tx.exec(`UPDATE push_target SET enabled = ?, updated_at = ? WHERE id = ?`, *p.Enabled, now, id); err != nil {
				return err
			}
		}
		if p.Name != nil {
			if _, err := tx.exec(`UPDATE push_target SET name = ?, updated_at = ? WHERE id = ?`, nullStr(*p.Name), now, id); err != nil {
				return err
			}
		}
		if p.Prefs != nil {
			b, _ := json.Marshal(p.Prefs)
			if _, err := tx.exec(`UPDATE push_target SET prefs = ?, updated_at = ? WHERE id = ?`, string(b), now, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Target{}, err
	}
	return s.TargetByID(ctx, id)
}

// DeleteTarget removes a target and its deliveries (ErrNotFound when it does not exist).
func (s *Store) DeleteTarget(ctx context.Context, id string) error {
	return s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.exec(`DELETE FROM delivery WHERE target_id = ?`, id); err != nil {
			return err
		}
		res, err := tx.exec(`DELETE FROM push_target WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ApplyTargetResult records one send's result on the target (push.md 5.6): sent clears the
// error and fail_count; gone sets gone_at and disables; retry and failed record the error
// and count it. The error text is the push service's reply, never the message.
func (s *Store) ApplyTargetResult(ctx context.Context, id string, r model.Result) error {
	return s.Write(ctx, func(tx *Tx) error {
		now := tx.NowMS()
		var err error
		switch r.Outcome {
		case model.OutcomeSent:
			_, err = tx.exec(`UPDATE push_target SET last_ok_at = ?, last_error = NULL, last_error_at = NULL, fail_count = 0,
				updated_at = ? WHERE id = ?`, now, now, id)
		case model.OutcomeGone:
			_, err = tx.exec(`UPDATE push_target SET gone_at = ?, enabled = 0, last_error = ?, last_error_at = ?, updated_at = ?
				WHERE id = ?`, now, nullStr(r.Err), now, now, id)
		default:
			_, err = tx.exec(`UPDATE push_target SET last_error = ?, last_error_at = ?, fail_count = fail_count + 1, updated_at = ?
				WHERE id = ?`, nullStr(r.Err), now, now, id)
		}
		return err
	})
}

// HubSecret returns a hub_secret value (ErrNotFound when absent).
func (s *Store) HubSecret(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	err := s.r.QueryRowContext(ctx, `SELECT value FROM hub_secret WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

// EnsureHubSecret stores the value made by generate under key unless the key exists, and
// returns what is stored (so two racing callers agree). generate runs only when absent.
func (s *Store) EnsureHubSecret(ctx context.Context, key string, generate func() ([]byte, error)) ([]byte, error) {
	var out []byte
	err := s.Write(ctx, func(tx *Tx) error {
		err := tx.queryRow(`SELECT value FROM hub_secret WHERE key = ?`, key).Scan(&out)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		v, err := generate()
		if err != nil {
			return err
		}
		if _, err := tx.exec(`INSERT INTO hub_secret (key, value, created_at) VALUES (?, ?, ?)`, key, v, tx.NowMS()); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// PushSetting returns the JSON of a setting key (ErrNotFound when absent).
func (s *Store) PushSetting(ctx context.Context, key string) (json.RawMessage, error) {
	var v string
	err := s.r.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return json.RawMessage(v), err
}

// PutPushSetting stores a JSON value under a setting key.
func (s *Store) PutPushSetting(ctx context.Context, key string, value json.RawMessage) error {
	if !json.Valid(value) {
		return fmt.Errorf("store: setting %s: invalid JSON", key)
	}
	return s.Write(ctx, func(tx *Tx) error {
		_, err := tx.exec(`INSERT INTO setting (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(value), tx.NowMS())
		return err
	})
}
