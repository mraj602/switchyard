package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// DeviceDetail is a device with the v3 columns Device does not carry: expires_at (0 =
// never) and prefs (the JSON text, "" = none).
type DeviceDetail struct {
	Device
	ExpiresAt int64
	Prefs     string
}

const detailCols = deviceCols + `, coalesce(expires_at, 0), coalesce(prefs, '')`

func scanDetail(sc interface{ Scan(...any) error }) (DeviceDetail, error) {
	var d DeviceDetail
	var scopes string
	err := sc.Scan(&d.ID, &d.Kind, &d.Name, &d.MachineID, &d.TokenHint, &scopes, &d.CreatedAt, &d.CreatedBy,
		&d.LastUsedAt, &d.RevokedAt, &d.LastUsedIP, &d.ExpiresAt, &d.Prefs)
	if err != nil {
		return d, err
	}
	_ = json.Unmarshal([]byte(scopes), &d.Scopes)
	return d, nil
}

// DeviceDetailByHash looks a token up by its hash. Revoked and expired devices are
// returned too; the caller checks RevokedAt and ExpiresAt.
func (s *Store) DeviceDetailByHash(ctx context.Context, hash []byte) (DeviceDetail, error) {
	d, err := scanDetail(s.r.QueryRowContext(ctx, `SELECT `+detailCols+` FROM device WHERE token_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DeviceDetailByID returns one device with its expiry and prefs.
func (s *Store) DeviceDetailByID(ctx context.Context, id string) (DeviceDetail, error) {
	d, err := scanDetail(s.r.QueryRowContext(ctx, `SELECT `+detailCols+` FROM device WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DeviceDetailByID is the same inside a write transaction.
func (tx *Tx) DeviceDetailByID(id string) (DeviceDetail, error) {
	d, err := scanDetail(tx.queryRow(`SELECT `+detailCols+` FROM device WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DeviceDetails lists devices newest first. Revoked and expired ones (expires_at <= now,
// ms) only with all.
func (s *Store) DeviceDetails(ctx context.Context, all bool, now int64) ([]DeviceDetail, error) {
	q := `SELECT ` + detailCols + ` FROM device`
	args := []any{}
	if !all {
		q += ` WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`
		args = append(args, now)
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceDetail
	for rows.Next() {
		d, err := scanDetail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDeviceExpiry sets device.expires_at (ms), in the transaction that made the device.
func (tx *Tx) SetDeviceExpiry(id string, at int64) error {
	_, err := tx.exec(`UPDATE device SET expires_at = ? WHERE id = ?`, nullInt(at), id)
	return err
}

// UpdateDevice changes a device's name and/or prefs (JSON text). Nil leaves a field alone.
// ErrNotFound when the device does not exist.
func (s *Store) UpdateDevice(ctx context.Context, id string, name, prefs *string) error {
	return s.Write(ctx, func(tx *Tx) error {
		var n int
		if err := tx.queryRow(`SELECT count(*) FROM device WHERE id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if name != nil {
			if _, err := tx.exec(`UPDATE device SET name = ? WHERE id = ?`, *name, id); err != nil {
				return err
			}
		}
		if prefs != nil {
			if _, err := tx.exec(`UPDATE device SET prefs = ? WHERE id = ?`, *prefs, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// RevokeDeviceCascade revokes a device and the board/owner sessions made from it
// (created_by = login:<id>), deletes their push targets and cancels their pending and held
// deliveries (pairing.md 6, push.md 5.6), also for a device that is already revoked.
// ErrNotFound when the device is missing, or already revoked with nothing left to clean. `sy hub token revoke` and DELETE /v1/devices/{id} both end here.
func (tx *Tx) RevokeDeviceCascade(id string) error {
	ids := []string{id}
	rows, err := tx.QueryContext(tx.ctx, `SELECT id FROM device WHERE created_by = ?`, "login:"+id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	revokeErr := tx.RevokeDevice(id)
	if revokeErr != nil && !errors.Is(revokeErr, ErrNotFound) {
		return revokeErr
	}
	// The clean-up also runs for a device that was already revoked (by an older code path, or
	// half way): its targets must not keep receiving pushes. When there was nothing left to
	// clean the call still answers ErrNotFound, as before.
	cleaned := int64(0)
	for _, d := range ids {
		res, err := tx.exec(`UPDATE delivery SET status = 'skipped', reason = 'revoked', next_at = NULL, updated_at = ?
			WHERE status IN ('pending', 'held') AND target_id IN (SELECT id FROM push_target WHERE device_id = ?)`, tx.NowMS(), d)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		cleaned += n
		if res, err = tx.exec(`DELETE FROM push_target WHERE device_id = ?`, d); err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		cleaned += n
	}
	if revokeErr != nil && cleaned == 0 {
		return revokeErr
	}
	return nil
}

// DeviceTargetBrief is what the Devices list shows of a push target.
type DeviceTargetBrief struct {
	ID, Kind, Name, Endpoint       string
	Enabled                        bool
	CreatedAt, LastOKAt, LastErrAt int64
	LastError                      string
	FailCount                      int
	GoneAt                         int64
}

// DeviceTargets returns the push targets of each device, by device id.
func (s *Store) DeviceTargets(ctx context.Context) (map[string][]DeviceTargetBrief, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT device_id, id, kind, coalesce(name, ''), endpoint, enabled, created_at,
		coalesce(last_ok_at, 0), coalesce(last_error_at, 0), coalesce(last_error, ''), fail_count, coalesce(gone_at, 0)
		FROM push_target WHERE device_id IS NOT NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]DeviceTargetBrief{}
	for rows.Next() {
		var dev string
		var t DeviceTargetBrief
		var en int
		if err := rows.Scan(&dev, &t.ID, &t.Kind, &t.Name, &t.Endpoint, &en, &t.CreatedAt, &t.LastOKAt, &t.LastErrAt,
			&t.LastError, &t.FailCount, &t.GoneAt); err != nil {
			return nil, err
		}
		t.Enabled = en == 1
		out[dev] = append(out[dev], t)
	}
	return out, rows.Err()
}
