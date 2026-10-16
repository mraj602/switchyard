package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// PairCodeRow is one row of pair_code (pairing.md 2). CodeHash is nil for owner links.
type PairCodeRow struct {
	ID, Kind, Name, MachineID, URL, CreatedBy string
	CodeHash, SecretHash                      []byte
	CreatedAt, ExpiresAt                      int64
	ClaimedAt, CancelledAt                    int64 // 0 = no
	DeviceID, ClaimIP                         string
}

// Live reports whether the code can still be claimed at now (ms).
func (r PairCodeRow) Live(now int64) bool {
	return r.ClaimedAt == 0 && r.CancelledAt == 0 && r.ExpiresAt > now
}

const pairCols = `id, kind, code_hash, secret_hash, coalesce(name, ''), coalesce(machine_id, ''), url, created_at, created_by,
	expires_at, coalesce(claimed_at, 0), coalesce(cancelled_at, 0), coalesce(device_id, ''), coalesce(claim_ip, '')`

func scanPair(sc interface{ Scan(...any) error }) (PairCodeRow, error) {
	var r PairCodeRow
	err := sc.Scan(&r.ID, &r.Kind, &r.CodeHash, &r.SecretHash, &r.Name, &r.MachineID, &r.URL, &r.CreatedAt, &r.CreatedBy,
		&r.ExpiresAt, &r.ClaimedAt, &r.CancelledAt, &r.DeviceID, &r.ClaimIP)
	return r, err
}

// InsertPairCode adds a code or owner link.
func (tx *Tx) InsertPairCode(r PairCodeRow) error {
	var code any
	if len(r.CodeHash) > 0 {
		code = r.CodeHash
	}
	_, err := tx.exec(`INSERT INTO pair_code (id, kind, code_hash, secret_hash, name, machine_id, url, created_at, created_by, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Kind, code, r.SecretHash, nullStr(r.Name), nullStr(r.MachineID), r.URL, r.CreatedAt, r.CreatedBy, r.ExpiresAt)
	return err
}

// LiveCodeHashExists reports whether a live code already has these digits' hash.
func (tx *Tx) LiveCodeHashExists(hash []byte, now int64) (bool, error) {
	var n int
	err := tx.queryRow(`SELECT count(*) FROM pair_code WHERE code_hash = ? AND claimed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?`,
		hash, now).Scan(&n)
	return n > 0, err
}

// CancelOldestLiveCodes cancels the oldest live typed codes (owner links have no digits
// and do not count) until at most keep remain. It returns how many it cancelled.
func (tx *Tx) CancelOldestLiveCodes(keep int, now int64) (int, error) {
	res, err := tx.exec(`UPDATE pair_code SET cancelled_at = ? WHERE id IN (
		SELECT id FROM pair_code WHERE code_hash IS NOT NULL AND claimed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?
		ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?)`, now, now, keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CancelOldestLiveBoardOwnerLinks cancels the oldest live owner links made on the board
// (created_by "owner:<device id>") until at most keep remain (pairing.md 4.1). Links made by
// `sy hub owner-link` are not counted or touched. It returns how many it cancelled.
func (tx *Tx) CancelOldestLiveBoardOwnerLinks(keep int, now int64) (int, error) {
	res, err := tx.exec(`UPDATE pair_code SET cancelled_at = ? WHERE id IN (
		SELECT id FROM pair_code WHERE kind = 'owner' AND created_by GLOB 'owner:*' AND claimed_at IS NULL AND cancelled_at IS NULL
		AND expires_at > ? ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?)`, now, now, keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// LivePairCodeBySecretHash finds the live code with this secret hash (ErrNotFound if none).
func (tx *Tx) LivePairCodeBySecretHash(hash []byte, now int64) (PairCodeRow, error) {
	return tx.livePair(`secret_hash`, hash, now)
}

// PairCodeBySecretHash finds the code with this secret hash whatever its status (ErrNotFound
// if none): owner sign-in says why a known link no longer works (pairing.md 4).
func (tx *Tx) PairCodeBySecretHash(hash []byte) (PairCodeRow, error) {
	r, err := scanPair(tx.queryRow(`SELECT `+pairCols+` FROM pair_code WHERE secret_hash = ? ORDER BY created_at DESC LIMIT 1`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// LivePairCodeByCodeHash finds the live code with this digits hash (ErrNotFound if none).
func (tx *Tx) LivePairCodeByCodeHash(hash []byte, now int64) (PairCodeRow, error) {
	return tx.livePair(`code_hash`, hash, now)
}

func (tx *Tx) livePair(col string, hash []byte, now int64) (PairCodeRow, error) {
	r, err := scanPair(tx.queryRow(`SELECT `+pairCols+` FROM pair_code WHERE `+col+` = ? AND claimed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?`, hash, now))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ClaimPairCode marks a live code claimed by deviceID. False means a concurrent claim (or
// a cancel) got there first.
func (tx *Tx) ClaimPairCode(id, deviceID, ip string, now int64) (bool, error) {
	res, err := tx.exec(`UPDATE pair_code SET claimed_at = ?, device_id = ?, claim_ip = ? WHERE id = ? AND claimed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?`,
		now, deviceID, nullStr(ip), id, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CancelPairCode cancels a code that is still unclaimed and uncancelled. found is false
// when no such code exists at all.
func (tx *Tx) CancelPairCode(id string, now int64) (found bool, err error) {
	var n int
	if err := tx.queryRow(`SELECT count(*) FROM pair_code WHERE id = ?`, id).Scan(&n); err != nil || n == 0 {
		return false, err
	}
	_, err = tx.exec(`UPDATE pair_code SET cancelled_at = ? WHERE id = ? AND claimed_at IS NULL AND cancelled_at IS NULL`, now, id)
	return true, err
}

// PairCodeByID returns one code row (ErrNotFound if none).
func (s *Store) PairCodeByID(ctx context.Context, id string) (PairCodeRow, error) {
	r, err := scanPair(s.r.QueryRowContext(ctx, `SELECT `+pairCols+` FROM pair_code WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// PrunePairCodes deletes codes that ended (cancelled or expired, never claimed) before the
// given time (ms). Claimed rows stay: a device row points at them. The hub's daily
// maintenance calls it with now - 30 days (pairing.md 2.1).
func (s *Store) PrunePairCodes(ctx context.Context, before int64) (int64, error) {
	var n int64
	err := s.Write(ctx, func(tx *Tx) error {
		res, err := tx.exec(`DELETE FROM pair_code WHERE device_id IS NULL AND claimed_at IS NULL AND expires_at < ?
			AND coalesce(cancelled_at, expires_at) < ?`, before, before)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// PairSetting reads a string-valued row of the setting table (value is JSON), e.g.
// "hub.public_url". ok is false when it is missing or not a string.
func (s *Store) PairSetting(ctx context.Context, key string) (value string, ok bool, err error) {
	var raw string
	err = s.r.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if json.Unmarshal([]byte(raw), &value) != nil {
		return "", false, nil
	}
	return value, true, nil
}

// PairSettingInt reads an integer-valued setting, e.g. "pair.ttl_s".
func (s *Store) PairSettingInt(ctx context.Context, key string) (value int64, ok bool, err error) {
	var raw string
	err = s.r.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var f float64
	if json.Unmarshal([]byte(raw), &f) != nil {
		return 0, false, nil
	}
	return int64(f), true, nil
}
