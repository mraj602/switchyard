package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

// NewID makes a hub id: prefix + ULID ("seat_01J…", "dev_01J…").
func NewID(prefix string, t time.Time) string { return prefix + record.NewID(t) }

// EnsureMachine creates the machine row if missing. name is used only on creation.
func (tx *Tx) EnsureMachine(id, name string, at int64) (created bool, err error) {
	if name == "" {
		name = id
	}
	res, err := tx.exec(`INSERT INTO machine (id, name, first_seen) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`, id, name, at)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Heartbeat is what one ingest request says about its machine.
type Heartbeat struct {
	Machine    string
	LastSeen   int64  // hub receive time
	LastRecord int64  // newest client ts in the batch, 0 = none
	Backlog    *int64 // X-Sy-Backlog, nil = not sent
	SyVersion  string // from the batch's client "wy/x.y.z", "" = unknown

	// machines.md 3.4, 6.1.
	Reach         string // "direct", "forward", "pull"; "" = not said, keeps the stored value
	BacklogOldest int64  // client ts (ms) of the oldest unanswered record; 0 = none/not sent
}

// MachineHeartbeat updates last_seen, last_record (never backwards), backlog, version and
// reach. A report of the backlog (Backlog or BacklogOldest set) also sets
// backlog_at to LastSeen and backlog_oldest to BacklogOldest (NULL when 0: the oldest
// record is only reported while more than one batch is queued).
func (tx *Tx) MachineHeartbeat(h Heartbeat) error {
	reported := h.Backlog != nil || h.BacklogOldest != 0
	_, err := tx.exec(`UPDATE machine SET
		last_seen = max(coalesce(last_seen, 0), ?),
		last_record = CASE WHEN ? > coalesce(last_record, 0) THEN ? ELSE last_record END,
		backlog = coalesce(?, backlog),
		sy_version = coalesce(?, sy_version),
		reach = coalesce(?, reach),
		backlog_oldest = CASE WHEN ? THEN ? ELSE backlog_oldest END,
		backlog_at = CASE WHEN ? THEN ? ELSE backlog_at END
		WHERE id = ?`,
		h.LastSeen, h.LastRecord, nullInt(h.LastRecord), h.Backlog, nullStr(h.SyVersion), nullStr(h.Reach),
		reported, nullInt(h.BacklogOldest), reported, h.LastSeen, h.Machine)
	return err
}

// SeatKey is an envelope seat.
type SeatKey struct {
	Account, Org, Email string
}

// UpsertSeat upserts account, org and seat for (account, org) seen at `at` and returns the
// seat id. Two different account uuids are never merged (identity.md 5.1).
func (tx *Tx) UpsertSeat(k SeatKey, at int64) (string, error) {
	if _, err := tx.exec(`INSERT INTO account (uuid, email, first_seen, last_seen) VALUES (?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET
		  email = CASE WHEN excluded.email IS NOT NULL AND excluded.last_seen >= account.last_seen THEN excluded.email ELSE coalesce(account.email, excluded.email) END,
		  first_seen = min(account.first_seen, excluded.first_seen),
		  last_seen = max(account.last_seen, excluded.last_seen)`,
		k.Account, nullStr(k.Email), at, at); err != nil {
		return "", err
	}
	if _, err := tx.exec(`INSERT INTO org (uuid, first_seen, last_seen) VALUES (?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET first_seen = min(org.first_seen, excluded.first_seen), last_seen = max(org.last_seen, excluded.last_seen)`,
		k.Org, at, at); err != nil {
		return "", err
	}
	var id string
	err := tx.queryRow(`SELECT id FROM seat WHERE account_uuid = ? AND org_uuid = ?`, k.Account, k.Org).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = NewID("seat_", time.UnixMilli(at))
	var order int64
	_ = tx.queryRow(`SELECT coalesce(max(sort_order) + 1, 0) FROM seat`).Scan(&order)
	_, err = tx.exec(`INSERT INTO seat (id, account_uuid, org_uuid, sort_order, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, k.Account, k.Org, order, tx.NowMS())
	return id, err
}

// SeatByEmailOrg finds the seat for a login known only by email and org (identity.md 4,
// src = auth_status). Exactly one match is required.
func (tx *Tx) SeatByEmailOrg(email, org string) (string, bool, error) {
	if email == "" || org == "" {
		return "", false, nil
	}
	rows, err := tx.QueryContext(tx.ctx, `SELECT s.id FROM seat s JOIN account a ON a.uuid = s.account_uuid
		WHERE a.email = ? AND s.org_uuid = ? LIMIT 2`, email, org)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", false, err
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		return "", false, rows.Err()
	}
	return ids[0], true, rows.Err()
}

// AccountInfo updates display fields of an account (from slot_info / web_usage).
func (tx *Tx) AccountInfo(uuid, email, displayName string) error {
	_, err := tx.exec(`UPDATE account SET email = coalesce(?, email), display_name = coalesce(?, display_name) WHERE uuid = ?`,
		nullStr(email), nullStr(displayName), uuid)
	return err
}

// OrgInfo updates an org's name, plan, rate tier and billing type when given.
func (tx *Tx) OrgInfo(uuid, name, plan, rateTier, billing string) error {
	_, err := tx.exec(`UPDATE org SET name = coalesce(?, name), plan = coalesce(?, plan),
		rate_tier = coalesce(?, rate_tier), billing_type = coalesce(?, billing_type) WHERE uuid = ?`,
		nullStr(name), nullStr(plan), nullStr(rateTier), nullStr(billing), uuid)
	return err
}

// SeatLook sets a seat's label, colour and order (used by tests and tools; the UI uses PatchSeat).
func (tx *Tx) SeatLook(seatID, label, colour string, order *int64) error {
	_, err := tx.exec(`UPDATE seat SET label = coalesce(?, label), colour = coalesce(?, colour), sort_order = coalesce(?, sort_order) WHERE id = ?`,
		nullStr(label), nullStr(colour), order, seatID)
	return err
}

// EnsureSlot creates or touches the slot (machine, folder) and returns its id.
func (tx *Tx) EnsureSlot(machine, folder string, at int64) (int64, error) {
	_, err := tx.exec(`INSERT INTO slot (machine_id, folder, first_seen, last_seen) VALUES (?, ?, ?, ?)
		ON CONFLICT(machine_id, folder) DO UPDATE SET first_seen = min(slot.first_seen, excluded.first_seen),
		last_seen = max(slot.last_seen, excluded.last_seen)`, machine, folder, at, at)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.queryRow(`SELECT id FROM slot WHERE machine_id = ? AND folder = ?`, machine, folder).Scan(&id)
	return id, err
}

// SlotFolder is a slot's folder (as records send it).
func (tx *Tx) SlotFolder(id int64) (string, error) {
	var f string
	err := tx.queryRow(`SELECT folder FROM slot WHERE id = ?`, id).Scan(&f)
	return f, err
}

// SlotDetails are the slot_info fields kept on the slot row.
type SlotDetails struct {
	RealPath, Launcher, ClaudeVersion string
	Default                           bool
	Sharing                           string // JSON or ""
}

// SlotInfo stores slot_info details.
func (tx *Tx) SlotInfo(id int64, d SlotDetails) error {
	def := 0
	if d.Default {
		def = 1
	}
	_, err := tx.exec(`UPDATE slot SET real_path = coalesce(?, real_path), is_default = ?, launcher = coalesce(?, launcher),
		claude_version = coalesce(?, claude_version), sharing = coalesce(?, sharing) WHERE id = ?`,
		nullStr(d.RealPath), def, nullStr(d.Launcher), nullStr(d.ClaudeVersion), nullStr(d.Sharing), id)
	return err
}

// SlotLogin applies identity.md 5.2 for a record at `at` that showed seat (nil = logged
// out) on a slot. Late records (older than the open period) change nothing.
func (tx *Tx) SlotLogin(slotID int64, seat *string, at int64, recordID string) error {
	var since int64
	var cur sql.NullString
	err := tx.queryRow(`SELECT since_at, seat_id FROM slot_login WHERE slot_id = ? AND until_at IS NULL`, slotID).Scan(&since, &cur)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A late record must not open a period that overlaps a closed one.
		var later sql.NullInt64
		if err := tx.queryRow(`SELECT max(until_at) FROM slot_login WHERE slot_id = ?`, slotID).Scan(&later); err != nil {
			return err
		}
		if later.Valid && at < later.Int64 {
			return nil
		}
		_, err := tx.exec(`INSERT INTO slot_login (slot_id, since_at, seat_id, source_record) VALUES (?, ?, ?, ?)`, slotID, at, seat, recordID)
		return err
	case err != nil:
		return err
	}
	if at < since {
		return nil
	}
	same := (seat == nil && !cur.Valid) || (seat != nil && cur.Valid && cur.String == *seat)
	if same {
		return nil
	}
	if at == since {
		// Same instant: the newer fact replaces the period's seat.
		_, err := tx.exec(`UPDATE slot_login SET seat_id = ?, source_record = ? WHERE slot_id = ? AND since_at = ?`, seat, recordID, slotID, since)
		return err
	}
	if _, err := tx.exec(`UPDATE slot_login SET until_at = ? WHERE slot_id = ? AND since_at = ?`, at, slotID, since); err != nil {
		return err
	}
	_, err = tx.exec(`INSERT INTO slot_login (slot_id, since_at, seat_id, source_record) VALUES (?, ?, ?, ?)`, slotID, at, seat, recordID)
	return err
}

// SeatAt is the seat a slot was logged in to at `at`, from slot_login periods.
func (tx *Tx) SeatAt(slotID, at int64) (string, bool, error) {
	var seat sql.NullString
	err := tx.queryRow(`SELECT seat_id FROM slot_login WHERE slot_id = ? AND since_at <= ? AND (until_at IS NULL OR until_at > ?)
		ORDER BY since_at DESC LIMIT 1`, slotID, at, at).Scan(&seat)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return seat.String, seat.Valid, nil
}

// WebSeat joins (machine, tab) to a seat (identity.md 5.5). stopped is the error status of
// a failed usage call ("" clears it).
func (tx *Tx) WebSeat(machine, tab, seat string, at int64, stopped string) error {
	_, err := tx.exec(`INSERT INTO web_seat (tab_id, machine_id, seat_id, first_seen, last_seen, stopped_reason) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine_id, tab_id) DO UPDATE SET
		  seat_id = CASE WHEN excluded.last_seen >= web_seat.last_seen THEN coalesce(excluded.seat_id, web_seat.seat_id) ELSE web_seat.seat_id END,
		  stopped_reason = CASE WHEN excluded.last_seen >= web_seat.last_seen THEN excluded.stopped_reason ELSE web_seat.stopped_reason END,
		  first_seen = min(web_seat.first_seen, excluded.first_seen),
		  last_seen = max(web_seat.last_seen, excluded.last_seen)`,
		tab, machine, nullStr(seat), at, at, nullStr(stopped))
	return err
}
