package store

import (
	"context"
	"database/sql"
	"errors"
)

// DeviceAccountsRow is one row of device_accounts (schema v14, keys.md 4.3): the body and
// owner signature of a device's machine_accounts document. Like every key row it is checked
// by internal/hub/keys when it is read, never trusted here.
type DeviceAccountsRow struct {
	DeviceID  string
	Body      []byte
	OwnerFP   string
	OwnerSig  []byte
	CreatedAt int64
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func deviceAccounts(ctx context.Context, q rowQuerier, id string) (DeviceAccountsRow, error) {
	var r DeviceAccountsRow
	err := q.QueryRowContext(ctx, `SELECT device_id, body, owner_fp, owner_sig, created_at FROM device_accounts WHERE device_id = ?`, id).
		Scan(&r.DeviceID, &r.Body, &r.OwnerFP, &r.OwnerSig, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// DeviceAccounts is a device's machine_accounts row (ErrNotFound when none).
func (s *Store) DeviceAccounts(ctx context.Context, id string) (DeviceAccountsRow, error) {
	return deviceAccounts(ctx, s.r, id)
}

// DeviceAccounts is DeviceAccounts inside a transaction.
func (tx *Tx) DeviceAccounts(id string) (DeviceAccountsRow, error) {
	return deviceAccounts(tx.ctx, tx.Tx, id)
}

// PutDeviceAccounts stores a device's machine_accounts document, replacing an older one.
func (tx *Tx) PutDeviceAccounts(r DeviceAccountsRow) error {
	_, err := tx.exec(`INSERT OR REPLACE INTO device_accounts (device_id, body, owner_fp, owner_sig, created_at) VALUES (?, ?, ?, ?, ?)`,
		r.DeviceID, r.Body, r.OwnerFP, r.OwnerSig, r.CreatedAt)
	return err
}

// MachineAccounts lists the accounts (uuids) the hub sees logged in now on the slots of a
// machine that are not removed, sorted: what `sy keys certify` offers the owner to bind
// (keys.md 4.3). These come from the machine's own records, so the owner checks them.
func (s *Store) MachineAccounts(ctx context.Context, machineID string) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT DISTINCT se.account_uuid FROM slot sl
		JOIN slot_login l ON l.slot_id = sl.id AND l.until_at IS NULL
		JOIN seat se ON se.id = l.seat_id
		WHERE sl.machine_id = ? AND sl.removed_at IS NULL ORDER BY se.account_uuid`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
