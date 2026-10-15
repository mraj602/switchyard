package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Rows of the F1 key tables (schema v7, keys.md 9). The store only keeps bytes: every
// signature is checked by internal/hub/keys when a row is loaded, so an edited row is
// ignored there, never trusted here.

// OwnerKeyRow is one row of owner_key.
type OwnerKeyRow struct {
	FP                string
	Pub, Body         []byte
	SelfSig, HubSig   []byte // HubSig nil while pending
	State, ProposedBy string
	CreatedAt         int64
	ConfirmedAt       int64
	ConfirmedBy       string
	RetiredAt         int64
}

const ownerKeyCols = `fp, pub, body, self_sig, hub_sig, state, proposed_by, created_at, coalesce(confirmed_at, 0),
	coalesce(confirmed_by, ''), coalesce(retired_at, 0)`

func scanOwnerKey(sc interface{ Scan(...any) error }) (OwnerKeyRow, error) {
	var r OwnerKeyRow
	err := sc.Scan(&r.FP, &r.Pub, &r.Body, &r.SelfSig, &r.HubSig, &r.State, &r.ProposedBy, &r.CreatedAt, &r.ConfirmedAt,
		&r.ConfirmedBy, &r.RetiredAt)
	return r, err
}

func ownerKeys(ctx context.Context, q querier, state string) ([]OwnerKeyRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+ownerKeyCols+` FROM owner_key WHERE ? = '' OR state = ? ORDER BY created_at DESC, fp`, state, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OwnerKeyRow
	for rows.Next() {
		r, err := scanOwnerKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OwnerKeys lists owner_key rows in one state ("" = all), newest first.
func (s *Store) OwnerKeys(ctx context.Context, state string) ([]OwnerKeyRow, error) {
	return ownerKeys(ctx, s.r, state)
}

// OwnerKeys is the same inside a write transaction.
func (tx *Tx) OwnerKeys(state string) ([]OwnerKeyRow, error) { return ownerKeys(tx.ctx, tx.Tx, state) }

// OwnerKeyByFP returns one row (ErrNotFound).
func (tx *Tx) OwnerKeyByFP(fp string) (OwnerKeyRow, error) {
	r, err := scanOwnerKey(tx.queryRow(`SELECT `+ownerKeyCols+` FROM owner_key WHERE fp = ?`, fp))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// InsertOwnerKey adds a proposal (state pending) or, with HubSig, an active key.
func (tx *Tx) InsertOwnerKey(r OwnerKeyRow) error {
	var hubSig any
	if r.HubSig != nil {
		hubSig = r.HubSig
	}
	_, err := tx.exec(`INSERT INTO owner_key (fp, pub, body, self_sig, hub_sig, state, proposed_by, created_at, confirmed_at, confirmed_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.FP, r.Pub, r.Body, r.SelfSig, hubSig, r.State, r.ProposedBy, r.CreatedAt,
		nullInt(r.ConfirmedAt), nullStr(r.ConfirmedBy))
	return err
}

// ActivateOwnerKey retires the active key, if any, and makes the pending key fp active with
// the hub's confirmation signature.
func (tx *Tx) ActivateOwnerKey(fp string, hubSig []byte, by string, now int64) error {
	if _, err := tx.exec(`UPDATE owner_key SET state = 'retired', retired_at = ? WHERE state = 'active'`, now); err != nil {
		return err
	}
	res, err := tx.exec(`UPDATE owner_key SET state = 'active', hub_sig = ?, confirmed_at = ?, confirmed_by = ? WHERE fp = ? AND state = 'pending'`,
		hubSig, now, by, fp)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePendingOwnerKeys deletes proposals made before cutoff (ms) and returns how many.
func (tx *Tx) DeletePendingOwnerKeys(cutoff int64) (int64, error) {
	res, err := tx.exec(`DELETE FROM owner_key WHERE state = 'pending' AND created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeviceKeyRow is one row of device_key.
type DeviceKeyRow struct {
	DeviceID             string
	SignAlg, BoxAlg      string
	SignPub, BoxPub      []byte // copies for queries; the certificate is what counts
	Reg                  string
	Cert, HubSig         []byte
	OwnerFP              string
	OwnerSig             []byte
	CreatedAt, Certified int64
}

const deviceKeyCols = `device_id, sign_alg, sign_pub, box_alg, box_pub, reg, cert, hub_sig, coalesce(owner_fp, ''), owner_sig,
	created_at, coalesce(certified_at, 0)`

func scanDeviceKey(sc interface{ Scan(...any) error }) (DeviceKeyRow, error) {
	var r DeviceKeyRow
	err := sc.Scan(&r.DeviceID, &r.SignAlg, &r.SignPub, &r.BoxAlg, &r.BoxPub, &r.Reg, &r.Cert, &r.HubSig, &r.OwnerFP, &r.OwnerSig,
		&r.CreatedAt, &r.Certified)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// DeviceKey returns a device's key row (ErrNotFound).
func (s *Store) DeviceKey(ctx context.Context, id string) (DeviceKeyRow, error) {
	return scanDeviceKey(s.r.QueryRowContext(ctx, `SELECT `+deviceKeyCols+` FROM device_key WHERE device_id = ?`, id))
}

// DeviceKey is the same inside a write transaction.
func (tx *Tx) DeviceKey(id string) (DeviceKeyRow, error) {
	return scanDeviceKey(tx.queryRow(`SELECT `+deviceKeyCols+` FROM device_key WHERE device_id = ?`, id))
}

// DeviceKeys lists every device_key row.
func (s *Store) DeviceKeys(ctx context.Context) ([]DeviceKeyRow, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+deviceKeyCols+` FROM device_key ORDER BY created_at, device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceKeyRow
	for rows.Next() {
		r, err := scanDeviceKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutDeviceKey stores a device's keys and hub-signed certificate, replacing an older row
// (without an owner signature: a new certificate has none).
func (tx *Tx) PutDeviceKey(r DeviceKeyRow) error {
	_, err := tx.exec(`INSERT OR REPLACE INTO device_key (device_id, sign_alg, sign_pub, box_alg, box_pub, reg, cert, hub_sig, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.DeviceID, r.SignAlg, r.SignPub, r.BoxAlg, r.BoxPub, r.Reg, r.Cert, r.HubSig, r.CreatedAt)
	return err
}

// SetDeviceOwnerSig adds the owner signature to a device's certificate.
func (tx *Tx) SetDeviceOwnerSig(id, fp string, sig []byte, now int64) error {
	res, err := tx.exec(`UPDATE device_key SET owner_fp = ?, owner_sig = ?, certified_at = ? WHERE device_id = ?`, fp, sig, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertLinkTarget makes or refreshes a phone's link push target (push.md 13.7) in the
// transaction that registers its keys.
func (tx *Tx) UpsertLinkTarget(id, deviceID, name string, keys json.RawMessage, now int64) error {
	_, err := tx.exec(`INSERT INTO push_target (id, kind, device_id, name, endpoint, keys, enabled, created_at, updated_at)
		VALUES (?, 'link', ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT (kind, endpoint) DO UPDATE SET device_id = excluded.device_id, keys = excluded.keys,
			updated_at = excluded.updated_at, gone_at = NULL`,
		id, deviceID, nullStr(name), "link:"+deviceID, string(keys), now, now)
	return err
}

// DropLink deletes a device's link queue and state (link.md 7), on revocation.
func (tx *Tx) DropLink(deviceID string) error {
	if _, err := tx.exec(`DELETE FROM link_queue WHERE device_id = ?`, deviceID); err != nil {
		return err
	}
	_, err := tx.exec(`DELETE FROM link_state WHERE device_id = ?`, deviceID)
	return err
}

// SignedDocRow is one row of revocation_doc or matrix_doc.
type SignedDocRow struct {
	Version       int
	Body          []byte
	Signer, KeyFP string
	Sig           []byte
	Widens        bool // matrix_doc only
	MadeBy        string
	CreatedAt     int64
}

func docTable(matrix bool) string {
	if matrix {
		return "matrix_doc"
	}
	return "revocation_doc"
}

// SignedDocs lists revocation_doc (matrix false) or matrix_doc rows, newest version first,
// at most limit (0 = all).
func (s *Store) SignedDocs(ctx context.Context, matrix bool, limit int) ([]SignedDocRow, error) {
	return signedDocs(ctx, s.r, matrix, limit)
}

// SignedDocs is the same inside a write transaction.
func (tx *Tx) SignedDocs(matrix bool, limit int) ([]SignedDocRow, error) {
	return signedDocs(tx.ctx, tx.Tx, matrix, limit)
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func signedDocs(ctx context.Context, q querier, matrix bool, limit int) ([]SignedDocRow, error) {
	widens := "0"
	if matrix {
		widens = "widens"
	}
	if limit <= 0 {
		limit = -1
	}
	rows, err := q.QueryContext(ctx, `SELECT version, body, signer, key_fp, sig, `+widens+`, made_by, created_at FROM `+docTable(matrix)+
		` ORDER BY version DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignedDocRow
	for rows.Next() {
		var r SignedDocRow
		var w int
		if err := rows.Scan(&r.Version, &r.Body, &r.Signer, &r.KeyFP, &r.Sig, &w, &r.MadeBy, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Widens = w == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertSignedDoc adds a version to revocation_doc (matrix false) or matrix_doc.
func (tx *Tx) InsertSignedDoc(matrix bool, r SignedDocRow) error {
	if matrix {
		w := 0
		if r.Widens {
			w = 1
		}
		_, err := tx.exec(`INSERT INTO matrix_doc (version, body, signer, key_fp, sig, widens, made_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Version, r.Body, r.Signer, r.KeyFP, r.Sig, w, r.MadeBy, r.CreatedAt)
		return err
	}
	_, err := tx.exec(`INSERT INTO revocation_doc (version, body, signer, key_fp, sig, made_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Version, r.Body, r.Signer, r.KeyFP, r.Sig, r.MadeBy, r.CreatedAt)
	return err
}

// MaxSignedDocVersion is the highest stored version of a table (0 = none), verified or not;
// a new version goes above it so the primary key never collides.
func (tx *Tx) MaxSignedDocVersion(matrix bool) (int, error) {
	var v int
	err := tx.queryRow(`SELECT coalesce(max(version), 0) FROM ` + docTable(matrix)).Scan(&v)
	return v, err
}
