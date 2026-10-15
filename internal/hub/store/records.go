package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// RawRecord is one row of the record table.
type RawRecord struct {
	ID, DeviceID, MachineID, Kind string
	V                             int
	TS, ReceivedAt                int64
	Slot                          string
	Body                          []byte // the record exactly as received
	Typed                         bool
	TypeError                     string
}

// InsertRecord stores a record raw (INSERT … ON CONFLICT(id) DO NOTHING). When the id is
// already stored it returns inserted = false and the device that stored it first.
func (tx *Tx) InsertRecord(r RawRecord) (inserted bool, firstDevice string, err error) {
	inserted, firstDevice, _, err = tx.InsertRecordOwned(r)
	return inserted, firstDevice, err
}

// InsertRecordOwned is InsertRecord that also returns the machine the stored row belongs to
// when the id was already there, for the phase 5 same-machine duplicate rule (record.md 3.1).
func (tx *Tx) InsertRecordOwned(r RawRecord) (inserted bool, firstDevice, firstMachine string, err error) {
	res, err := tx.exec(`INSERT INTO record (id, device_id, machine_id, kind, v, ts, received_at, slot, body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		r.ID, r.DeviceID, r.MachineID, r.Kind, r.V, r.TS, r.ReceivedAt, nullStr(r.Slot), string(r.Body))
	if err != nil {
		return false, "", "", err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, r.DeviceID, r.MachineID, nil
	}
	var machine sql.NullString
	err = tx.queryRow(`SELECT device_id, machine_id FROM record WHERE id = ?`, r.ID).Scan(&firstDevice, &machine)
	return false, firstDevice, machine.String, err
}

// MarkTyped records the outcome of typing a record: typed = 1, or typed = 0 with why.
// typeErr == "" and !typed means "not typed, nothing wrong" (unknown kind, newer v).
func (tx *Tx) MarkTyped(id string, typed bool, typeErr string) error {
	t := 0
	if typed {
		t = 1
	}
	_, err := tx.exec(`UPDATE record SET typed = ?, type_error = ? WHERE id = ?`, t, nullStr(typeErr), id)
	return err
}

// RecordRef is the part of a stored record a refold needs.
type RecordRef struct {
	ID   string
	TS   int64
	Body []byte
}

func scanRefs(rows *sql.Rows) ([]RecordRef, error) {
	defer rows.Close()
	var out []RecordRef
	for rows.Next() {
		var r RecordRef
		var body string
		if err := rows.Scan(&r.ID, &r.TS, &body); err != nil {
			return nil, err
		}
		r.Body = []byte(body)
		out = append(out, r)
	}
	return out, rows.Err()
}

// HookRecordsOf returns the hook_event records of a session.
func (tx *Tx) HookRecordsOf(sessionID string) ([]RecordRef, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT r.id, r.ts, r.body FROM hook_event h JOIN record r ON r.id = h.record_id
		WHERE h.session_id = ? ORDER BY r.ts, r.id`, sessionID)
	if err != nil {
		return nil, err
	}
	return scanRefs(rows)
}

// SessionListRecords returns the session_list records of one slot (machine + folder as
// sent) with ts >= from.
func (tx *Tx) SessionListRecords(machine, slot string, from int64) ([]RecordRef, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT id, ts, body FROM record
		WHERE machine_id = ? AND kind = 'session_list' AND slot = ? AND ts >= ? ORDER BY ts, id`, machine, slot, from)
	if err != nil {
		return nil, err
	}
	return scanRefs(rows)
}

// RecordsForReplay streams every record in (ts, id) order to fn, for rebuilding typed
// tables in another database.
func (s *Store) RecordsForReplay(ctx context.Context, fn func(RawRecord) error) error {
	rows, err := s.r.QueryContext(ctx, `SELECT id, device_id, machine_id, kind, v, ts, received_at, coalesce(slot, ''), body FROM record ORDER BY ts, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r RawRecord
		var body string
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.MachineID, &r.Kind, &r.V, &r.TS, &r.ReceivedAt, &r.Slot, &body); err != nil {
			return err
		}
		r.Body = []byte(body)
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Device is one row of the device table (tokens.md).
type Device struct {
	ID, Kind, Name, MachineID string
	TokenHint                 string
	Scopes                    []string
	CreatedAt                 int64
	CreatedBy                 string
	LastUsedAt, RevokedAt     int64
	LastUsedIP                string
}

// HasScope reports whether the device holds scope.
func (d *Device) HasScope(scope string) bool {
	for _, s := range d.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

const deviceCols = `id, kind, name, coalesce(machine_id, ''), token_hint, scopes, created_at, created_by,
	coalesce(last_used_at, 0), coalesce(revoked_at, 0), coalesce(last_used_ip, '')`

func scanDevice(sc interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var scopes string
	err := sc.Scan(&d.ID, &d.Kind, &d.Name, &d.MachineID, &d.TokenHint, &scopes, &d.CreatedAt, &d.CreatedBy,
		&d.LastUsedAt, &d.RevokedAt, &d.LastUsedIP)
	if err != nil {
		return d, err
	}
	_ = json.Unmarshal([]byte(scopes), &d.Scopes)
	return d, nil
}

// InsertDevice adds a device with the SHA-256 of its token.
// While the hub is frozen for a move (machines.md 8.1) it answers ErrFrozen: a device made
// now would not be in the export. Pairing claims, token creation and pull devices all pass here.
func (tx *Tx) InsertDevice(d Device, tokenHash []byte) error {
	if err := tx.CheckNotFrozen(); err != nil {
		return err
	}
	scopes, _ := json.Marshal(d.Scopes)
	_, err := tx.exec(`INSERT INTO device (id, kind, name, machine_id, token_hash, token_hint, scopes, created_at, created_by, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Kind, d.Name, nullStr(d.MachineID), tokenHash, d.TokenHint, string(scopes), d.CreatedAt, d.CreatedBy, nullInt(d.RevokedAt))
	return err
}

// DeviceByHash looks a token up by its hash. Revoked devices are returned too; the
// caller checks RevokedAt.
func (s *Store) DeviceByHash(ctx context.Context, hash []byte) (Device, error) {
	d, err := scanDevice(s.r.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM device WHERE token_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DeviceByID returns one device.
func (s *Store) DeviceByID(ctx context.Context, id string) (Device, error) {
	d, err := scanDevice(s.r.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM device WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// Devices lists devices, oldest first; revoked ones only with all.
func (s *Store) Devices(ctx context.Context, all bool) ([]Device, error) {
	q := `SELECT ` + deviceCols + ` FROM device`
	if !all {
		q += ` WHERE revoked_at IS NULL`
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice sets revoked_at (once) on a device and on browser sessions made from it.
func (tx *Tx) RevokeDevice(id string) error {
	res, err := tx.exec(`UPDATE device SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, tx.NowMS(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = tx.exec(`UPDATE device SET revoked_at = ? WHERE created_by = ? AND revoked_at IS NULL`, tx.NowMS(), "login:"+id)
	return err
}

// DeviceRevoked reports whether a device is revoked or missing, inside the transaction.
func (tx *Tx) DeviceRevoked(id string) (bool, error) {
	var revoked sql.NullInt64
	err := tx.queryRow(`SELECT revoked_at FROM device WHERE id = ?`, id).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return revoked.Valid, err
}

// TouchDevice sets last_used_at and last_used_ip.
func (s *Store) TouchDevice(ctx context.Context, id string, at int64, ip string) error {
	return s.Write(ctx, func(tx *Tx) error {
		_, err := tx.exec(`UPDATE device SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, at, nullStr(ip), id)
		return err
	})
}

// EachRecordOfKind streams every stored record of one kind in (ts, id) order to fn (sy hub
// refold reads token_rows this way).
func (tx *Tx) EachRecordOfKind(kind string, fn func(RecordRef) error) error {
	rows, err := tx.QueryContext(tx.ctx, `SELECT id, ts, body FROM record WHERE kind = ? ORDER BY ts, id`, kind)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r RecordRef
		var body string
		if err := rows.Scan(&r.ID, &r.TS, &body); err != nil {
			return err
		}
		r.Body = []byte(body)
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// TokenRowTimes returns the ts of every token_row of a session, oldest first: the
// session's transcript activity, for the fold's list inputs (states.md 4).
func (tx *Tx) TokenRowTimes(sessionID string) ([]int64, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT ts FROM token_row WHERE session_id = ? ORDER BY ts`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var t int64
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LastTokenRowAt is the ts of a session's newest token_row at or before at (ms), 0 if none.
func (tx *Tx) LastTokenRowAt(sessionID string, at int64) (int64, error) {
	var t int64
	err := tx.queryRow(`SELECT coalesce(max(ts), 0) FROM token_row WHERE session_id = ? AND ts <= ?`, sessionID, at).Scan(&t)
	return t, err
}

// RecordTS is a stored record's ts (ms); ok is false when there is no such record.
func (tx *Tx) RecordTS(id string) (ts int64, ok bool, err error) {
	err = tx.queryRow(`SELECT ts FROM record WHERE id = ?`, id).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return ts, err == nil, err
}
