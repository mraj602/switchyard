package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/version"
)

// The hub's machines model (machines.md 6, 9; phase 5 WP-C): the read model with reach, info,
// backlog age, version and pull state; rename and remove; machine_info; the frozen flag that
// ingest re-checks inside its write transaction (machines.md 8.1).

// KeyHubFrozen is the setting that freezes the hub for a move: {"since": ms, "moved_to": url|null}.
const KeyHubFrozen = "hub.frozen"

// HubIsFrozen reports whether hub.frozen is set, inside the transaction (machines.md 8.1:
// ingest re-reads it in its write transaction, so every ingest either committed before the
// freeze or sees it).
func (tx *Tx) HubIsFrozen() (bool, error) {
	_, ok, err := tx.AlertSetting(KeyHubFrozen)
	return ok, err
}

// HubIsFrozen reports whether hub.frozen is set (read pool).
func (s *Store) HubIsFrozen(ctx context.Context) (bool, error) {
	_, ok, err := s.AlertSetting(ctx, KeyHubFrozen)
	return ok, err
}

// MachineRestore clears removed_at: a record from a (new) device of a removed machine brings
// it back (machines.md 6.2).
func (tx *Tx) MachineRestore(id string) (bool, error) {
	res, err := tx.exec(`UPDATE machine SET removed_at = NULL WHERE id = ? AND removed_at IS NOT NULL`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MachineInfoUpdate is what a machine_info record sets on the machine row (record.md 4.8).
type MachineInfoUpdate struct {
	OS, Arch      string
	Reach         string // direct, forward, pull; "" or "none" keeps the stored value
	RecordV       int
	Held          int64
	BacklogOldest int64  // ms, 0 = none
	Info          []byte // the payload as received
	At            int64  // the record's ts (ms): only a newer info_at wins
}

// MachineInfo applies a machine_info record when it is newer than the stored info_at.
func (tx *Tx) MachineInfo(machine string, u MachineInfoUpdate) (bool, error) {
	reach := u.Reach
	switch reach {
	case "direct", "forward", "pull":
	default:
		reach = ""
	}
	res, err := tx.exec(`UPDATE machine SET os = ?, arch = ?, reach = coalesce(?, reach), record_v = ?, held = ?,
		backlog_oldest = ?, info = ?, info_at = ?
		WHERE id = ? AND (info_at IS NULL OR info_at < ?)`,
		nullStr(u.OS), nullStr(u.Arch), nullStr(reach), nullInt(int64(u.RecordV)), max(u.Held, 0),
		nullInt(u.BacklogOldest), nullStr(string(u.Info)), u.At, machine, u.At)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MachineRename sets a machine's name (ErrNotFound when there is no such machine).
func (tx *Tx) MachineRename(id, name string) error {
	res, err := tx.exec(`UPDATE machine SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MachineRemoval is what MachineRemove did.
type MachineRemoval struct {
	Devices []string      // devices revoked now (the pull device included)
	Pull    []string      // pull targets disabled
	Alerts  []model.Alert // alerts ended (status and reason as stored)
}

// MachineRemove removes a machine from the board (machines.md 6.2): sets removed_at, revokes
// every device bound to it (and the browser sessions made from them), disables its pull
// target, and ends its open alerts with reason `ended`: pending ones become `cancelled`
// (they never pushed, push.md 4), fired, snoozed and seen ones `closed`. History stays.
// ErrNotFound when there is no such machine.
func (tx *Tx) MachineRemove(id string, now int64) (MachineRemoval, error) {
	var out MachineRemoval
	res, err := tx.exec(`UPDATE machine SET removed_at = ? WHERE id = ? AND removed_at IS NULL`, now, id)
	if err != nil {
		return out, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := tx.queryRow(`SELECT 1 FROM machine WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return out, ErrNotFound
		} else if err != nil {
			return out, err
		}
		// Already removed: finish anything a newer device added since (idempotent).
	}
	devs, err := tx.strings(`SELECT id FROM device WHERE revoked_at IS NULL AND (machine_id = ?
		OR id IN (SELECT device_id FROM pull_target WHERE machine_id = ? AND device_id IS NOT NULL)) ORDER BY id`, id, id)
	if err != nil {
		return out, err
	}
	for _, d := range devs {
		if err := tx.RevokeDeviceCascade(d); err != nil && !errors.Is(err, ErrNotFound) {
			return out, err
		}
	}
	out.Devices = devs
	if out.Pull, err = tx.strings(`SELECT id FROM pull_target WHERE machine_id = ? AND enabled = 1 ORDER BY id`, id); err != nil {
		return out, err
	}
	if _, err := tx.exec(`UPDATE pull_target SET enabled = 0, state = 'disabled', next_at = NULL WHERE machine_id = ?`, id); err != nil {
		return out, err
	}
	open, err := alertRows(tx.QueryContext(tx.ctx, `SELECT `+alertCols+` FROM alert WHERE machine_id = ? AND `+alertOpenSQL+
		` ORDER BY created_at, id`, id))
	if err != nil {
		return out, err
	}
	reason := model.ReasonEnded
	for _, a := range open {
		status := model.StatusClosed
		if a.Status == model.StatusPending {
			status = model.StatusCancelled
		}
		a.Status = status
		a.Reason, a.DueAt, a.EndedAt = &reason, 0, model.Time(now)
		if err := tx.AlertSave(a, now); err != nil {
			return out, err
		}
		if err := tx.DeliveriesSkipUnsent(a.ID, "resolved", now); err != nil {
			return out, err
		}
		out.Alerts = append(out.Alerts, a)
	}
	return out, nil
}

func (tx *Tx) strings(q string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(tx.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SeatPair is a seat's login (account uuid, org uuid): what a machine knows of a seat.
type SeatPair struct {
	Account, Org string
}

// SeatPairs maps every seat id to its (account, org) pair (machines.md 5.3: the direct-push
// policy names seats by login, because a machine does not know hub seat ids).
func (s *Store) SeatPairs(ctx context.Context) (map[string]SeatPair, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id, account_uuid, org_uuid FROM seat`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SeatPair{}
	for rows.Next() {
		var id string
		var p SeatPair
		if err := rows.Scan(&id, &p.Account, &p.Org); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- read model

// MachinePull is Machine.pull (api.yaml PullStatus): a pull target's health.
type MachinePull struct {
	Name      string  `json:"name"`
	Target    string  `json:"target"`
	IntervalS int     `json:"interval_s"`
	State     string  `json:"state"`
	LastTry   Time    `json:"last_try"`
	LastOK    Time    `json:"last_ok"`
	LastError *string `json:"last_error"`
	NextAt    Time    `json:"next_at"`
}

// MachineQuery selects machines for MachineList.
type MachineQuery struct {
	IDs []string // nil = all
	All bool     // include removed machines (?all=true)
}

// Version statuses of Machine.version_status (machines.md 6.1).
const (
	VersionSame    = "same"
	VersionOlder   = "older"
	VersionNewer   = "newer"
	VersionUnknown = "unknown"
)

// bareVersion is v without spaces, a leading "v" and build metadata ("+abc").
func bareVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	return v
}

// semver parses "v1.2.3" / "1.2.3" (build metadata ignored); ok is false for anything else,
// pre-releases ("0.5.0-rc.1") and "dev" included.
func semver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// VersionStatus compares a machine's sy version with the hub's by semver (machines.md 6.1):
// same, older, newer, or unknown when either is missing, a pre-release or a dev build.
func VersionStatus(machine, hub string) string {
	// The same build string is the same version, semver or not: builds from a commit
	// without a tag are named by the commit ("104cd7f", UI audit M1). "dev" (a build
	// without version stamping) and an empty string say nothing.
	if a, b := bareVersion(machine), bareVersion(hub); a == b && a != "" && a != "dev" {
		return VersionSame
	}
	m, ok1 := semver(machine)
	h, ok2 := semver(hub)
	if !ok1 || !ok2 {
		return VersionUnknown
	}
	for i := range m {
		switch {
		case m[i] < h[i]:
			return VersionOlder
		case m[i] > h[i]:
			return VersionNewer
		}
	}
	return VersionSame
}

// machineInfoFlags are the parts of the stored machine_info the read model shows.
type machineInfoFlags struct {
	DirectPush *struct {
		Enabled bool `json:"enabled"`
	} `json:"direct_push"`
	Timer *struct {
		Active *bool `json:"active"`
		Linger *bool `json:"linger"`
	} `json:"timer"`
}

// MachineList is the read model of /v1/machines (machines.md 6.1), by name.
func (s *Store) MachineList(ctx context.Context, q MachineQuery, now time.Time) ([]MachineView, error) {
	args := []any{now.Add(-LiveWithin).UnixMilli()}
	sqlq := `SELECT m.id, m.name, m.os, m.arch, m.sy_version, coalesce(m.last_seen, 0), coalesce(m.last_record, 0), m.backlog,
		(SELECT count(*)` + sessionFrom + ` WHERE s.machine_id = m.id AND ` + liveNowCondition + `),
		(SELECT count(*)` + sessionFrom + ` WHERE s.machine_id = m.id AND ` + sessionStateExpr + ` IN (` + activeStatesSQL + `)),
		m.reach, m.record_v, m.held, coalesce(m.backlog_oldest, 0), coalesce(m.backlog_at, 0), m.info, coalesce(m.removed_at, 0)
		FROM machine m WHERE 1 = 1`
	if !q.All {
		sqlq += ` AND m.removed_at IS NULL`
	}
	if q.IDs != nil {
		sqlq += " AND " + in("m.id", q.IDs, &args)
	}
	rows, err := s.r.QueryContext(ctx, sqlq+` ORDER BY m.name, m.id`, args...)
	if err != nil {
		return nil, err
	}
	var out []MachineView
	for rows.Next() {
		var m MachineView
		var osv, arch, ver, reach, info sql.NullString
		var backlog, recordV sql.NullInt64
		var lastSeen, lastRec, oldest, backlogAt, removed int64
		var active int
		if err := rows.Scan(&m.ID, &m.Name, &osv, &arch, &ver, &lastSeen, &lastRec, &backlog, &m.LiveSessions, &active,
			&reach, &recordV, &m.Held, &oldest, &backlogAt, &info, &removed); err != nil {
			rows.Close()
			return nil, err
		}
		m.OS, m.Arch, m.SyVersion, m.Reach = ns(osv), ns(arch), ns(ver), ns(reach)
		m.LastSeen, m.LastRecord = Time(lastSeen), Time(lastRec)
		m.BacklogOldest, m.BacklogAt, m.RemovedAt = Time(oldest), Time(backlogAt), Time(removed)
		if backlog.Valid {
			b := backlog.Int64
			m.Backlog = &b
		}
		if recordV.Valid {
			v := recordV.Int64
			m.RecordV = &v
		}
		m.VersionStatus = VersionStatus(ver.String, version.Version)
		if info.Valid {
			var f machineInfoFlags
			if json.Unmarshal([]byte(info.String), &f) == nil {
				m.DirectPush = f.DirectPush != nil && f.DirectPush.Enabled
				if f.Timer != nil {
					m.TimerOK, m.Linger = f.Timer.Active, f.Timer.Linger
				}
			}
		}
		m.Silent = active > 0 && (lastSeen == 0 || now.UnixMilli()-lastSeen > MachineSilent.Milliseconds())
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	frozen, err := s.HubIsFrozen(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.machineSlots(ctx, &out[i]); err != nil {
			return nil, err
		}
		if out[i].Pull, err = s.machinePull(ctx, out[i].ID, frozen); err != nil {
			return nil, err
		}
		if out[i].Alert, err = s.machineSilentAlert(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) machineSlots(ctx context.Context, m *MachineView) error {
	srows, err := s.r.QueryContext(ctx, `SELECT sl.folder, sl.launcher, l.seat_id, sl.claude_version FROM slot sl
		LEFT JOIN slot_login l ON l.slot_id = sl.id AND l.until_at IS NULL
		WHERE sl.machine_id = ? AND sl.removed_at IS NULL ORDER BY sl.folder`, m.ID)
	if err != nil {
		return err
	}
	defer srows.Close()
	m.Slots = []SlotView{}
	for srows.Next() {
		var v SlotView
		var launcher, seat, cv sql.NullString
		if err := srows.Scan(&v.Folder, &launcher, &seat, &cv); err != nil {
			return err
		}
		v.Launcher, v.Seat, v.ClaudeVersion, v.LoggedIn = ns(launcher), ns(seat), ns(cv), seat.Valid
		m.Slots = append(m.Slots, v)
	}
	return srows.Err()
}

// machinePull reads the machine's pull target (nil when it has none). A disabled target reads
// `disabled`, and every target reads `frozen` while the hub is frozen (its scheduler pauses).
func (s *Store) machinePull(ctx context.Context, machine string, frozen bool) (*MachinePull, error) {
	var p MachinePull
	var host, user string
	var port, enabled int
	var lastTry, lastOK, next sql.NullInt64
	var lastErr sql.NullString
	err := s.r.QueryRowContext(ctx, `SELECT name, host, port, user, interval_s, enabled, state, last_try_at, last_ok_at,
		last_error, next_at FROM pull_target WHERE machine_id = ?`, machine).
		Scan(&p.Name, &host, &port, &user, &p.IntervalS, &enabled, &p.State, &lastTry, &lastOK, &lastErr, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Target = host
	if user != "" {
		p.Target = user + "@" + host
	}
	if port != 22 {
		p.Target += ":" + strconv.Itoa(port)
	}
	p.LastTry, p.LastOK, p.NextAt, p.LastError = Time(lastTry.Int64), Time(lastOK.Int64), Time(next.Int64), ns(lastErr)
	switch {
	case enabled == 0:
		p.State = "disabled"
	case frozen:
		p.State = "frozen"
	}
	return &p, nil
}

// machineSilentAlert is the machine's open machine_silent alert, newest first (nil if none).
func (s *Store) machineSilentAlert(ctx context.Context, machine string) (*model.AlertBrief, error) {
	a, err := alertScan(s.r.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alert WHERE machine_id = ?
		AND trigger = 'machine_silent' AND `+alertOpenSQL+` ORDER BY created_at DESC, id DESC LIMIT 1`, machine))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a.AlertBrief, nil
}

// MachineByID is one machine's read model, removed or not (ErrNotFound).
func (s *Store) MachineByID(ctx context.Context, id string, now time.Time) (MachineView, error) {
	ms, err := s.MachineList(ctx, MachineQuery{IDs: []string{id}, All: true}, now)
	if err != nil {
		return MachineView{}, err
	}
	if len(ms) == 0 {
		return MachineView{}, ErrNotFound
	}
	return ms[0], nil
}

// MachineRecentDirect reports whether the machine was heard from within the window by a
// route other than the hub's own pull (its reach is not "pull"). A machine the hub has never
// heard of is not direct.
func (s *Store) MachineRecentDirect(ctx context.Context, id string, within time.Duration) (bool, error) {
	var last sql.NullInt64
	var reach sql.NullString
	err := s.r.QueryRowContext(ctx, `SELECT last_seen, reach FROM machine WHERE id = ? AND removed_at IS NULL`, id).Scan(&last, &reach)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !last.Valid || reach.String == "pull" {
		return false, nil
	}
	return s.Now().UnixMilli()-last.Int64 < within.Milliseconds(), nil
}
