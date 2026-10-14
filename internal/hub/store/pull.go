package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Pull target states (schema v4 pull_target.state; machines.md 3.4).
const (
	PullStateNew      = "new"
	PullStateOK       = "ok"
	PullStateFailing  = "failing"
	PullStateHostKey  = "host_key"
	PullStateAuth     = "auth"
	PullStateRefused  = "refused"
	PullStateDisabled = "disabled"
)

// ErrPullExists means a pull target with that name (or for that machine) already exists.
var ErrPullExists = errors.New("a pull target with that name or machine already exists")

// PullTarget is one pull_target row. Times are ms since the epoch, 0 = never/none.
type PullTarget struct {
	ID        string
	Name      string
	MachineID string // "" until bound (first hello)
	DeviceID  string // the pull device; "" until bound
	Host      string
	Port      int
	User      string
	Jump      string
	RemoteSy  string
	HostKeys  []string
	IntervalS int
	Enabled   bool
	State     string
	CreatedAt int64
	LastTryAt int64
	LastOKAt  int64
	LastError string
	FailCount int
	NextAt    int64
}

const pullCols = `id, name, coalesce(machine_id, ''), coalesce(device_id, ''), host, port, user, coalesce(jump, ''), remote_sy,
	host_keys, interval_s, enabled, state, created_at, coalesce(last_try_at, 0), coalesce(last_ok_at, 0),
	coalesce(last_error, ''), fail_count, coalesce(next_at, 0)`

func scanPull(sc interface{ Scan(...any) error }) (PullTarget, error) {
	var t PullTarget
	var keys string
	var en int
	err := sc.Scan(&t.ID, &t.Name, &t.MachineID, &t.DeviceID, &t.Host, &t.Port, &t.User, &t.Jump, &t.RemoteSy,
		&keys, &t.IntervalS, &en, &t.State, &t.CreatedAt, &t.LastTryAt, &t.LastOKAt, &t.LastError, &t.FailCount, &t.NextAt)
	if err != nil {
		return t, err
	}
	t.Enabled = en == 1
	_ = json.Unmarshal([]byte(keys), &t.HostKeys)
	return t, nil
}

func (s *Store) queryPulls(ctx context.Context, q string, args ...any) ([]PullTarget, error) {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PullTarget
	for rows.Next() {
		t, err := scanPull(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PullTargets lists every target by name.
func (s *Store) PullTargets(ctx context.Context) ([]PullTarget, error) {
	return s.queryPulls(ctx, `SELECT `+pullCols+` FROM pull_target ORDER BY name`)
}

func (s *Store) onePull(ctx context.Context, where string, args ...any) (PullTarget, error) {
	t, err := scanPull(s.r.QueryRowContext(ctx, `SELECT `+pullCols+` FROM pull_target WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// PullTargetByID returns one target (ErrNotFound if missing).
func (s *Store) PullTargetByID(ctx context.Context, id string) (PullTarget, error) {
	return s.onePull(ctx, `id = ?`, id)
}

// PullTargetByName returns one target by its name or id.
func (s *Store) PullTargetByName(ctx context.Context, name string) (PullTarget, error) {
	return s.onePull(ctx, `name = ? OR id = ?`, name, name)
}

// PullTargetByMachine returns the target bound to a machine.
func (s *Store) PullTargetByMachine(ctx context.Context, machine string) (PullTarget, error) {
	return s.onePull(ctx, `machine_id = ?`, machine)
}

// DuePullTargets returns the enabled targets whose next_at is due (or never set), oldest
// first, excluding the states that wait for the owner (host_key, refused, disabled).
func (s *Store) DuePullTargets(ctx context.Context, now int64) ([]PullTarget, error) {
	return s.queryPulls(ctx, `SELECT `+pullCols+` FROM pull_target
		WHERE enabled = 1 AND state NOT IN ('host_key', 'refused', 'disabled') AND coalesce(next_at, 0) <= ?
		ORDER BY coalesce(next_at, 0), name`, now)
}

// NewPullDevice makes a pull device in tx (kind machine, scope write, created_by
// pull:<target>) whose token is generated, hashed and thrown away: only the hash is kept,
// so no token can ever be presented for it (tokens.md 3). The machine row is created if
// missing, named name.
func (tx *Tx) NewPullDevice(targetID, machineID, name string) (string, error) {
	if _, err := tx.EnsureMachine(machineID, name, tx.NowMS()); err != nil {
		return "", err
	}
	var hash [32]byte
	if _, err := rand.Read(hash[:]); err != nil {
		return "", err
	}
	d := Device{ID: NewID("dev_", tx.now), Kind: "machine", Name: name, MachineID: machineID, TokenHint: "pull",
		Scopes: []string{"write"}, CreatedAt: tx.NowMS(), CreatedBy: "pull:" + targetID}
	if err := tx.InsertDevice(d, hash[:]); err != nil {
		return "", err
	}
	return d.ID, nil
}

// AddPullTarget inserts a target. With t.MachineID set it also makes the pull device now;
// otherwise the target binds on its first hello (BindPullTarget). ID, CreatedAt and the
// defaults (port 22, interval 60, remote_sy .local/bin/sy) are filled in. ErrPullExists
// when the name or machine is already used.
func (s *Store) AddPullTarget(ctx context.Context, t PullTarget) (PullTarget, error) {
	var out PullTarget
	err := s.Write(ctx, func(tx *Tx) error {
		t.ID = NewID("pt_", tx.now)
		t.CreatedAt = tx.NowMS()
		if t.Port == 0 {
			t.Port = 22
		}
		if t.IntervalS == 0 {
			t.IntervalS = 60
		}
		if t.RemoteSy == "" {
			t.RemoteSy = ".local/bin/sy"
		}
		t.State, t.Enabled, t.NextAt = PullStateNew, true, tx.NowMS()
		if t.HostKeys == nil {
			t.HostKeys = []string{}
		}
		if t.MachineID != "" {
			var n int
			if err := tx.queryRow(`SELECT count(*) FROM pull_target WHERE machine_id = ?`, t.MachineID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrPullExists
			}
			dev, err := tx.NewPullDevice(t.ID, t.MachineID, t.Name)
			if err != nil {
				return err
			}
			t.DeviceID = dev
		}
		keys, _ := json.Marshal(t.HostKeys)
		_, err := tx.exec(`INSERT INTO pull_target (id, name, machine_id, device_id, host, port, user, jump, remote_sy, host_keys,
			interval_s, enabled, state, created_at, next_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'new', ?, ?)`,
			t.ID, t.Name, nullStr(t.MachineID), nullStr(t.DeviceID), t.Host, t.Port, t.User, nullStr(t.Jump), t.RemoteSy,
			string(keys), t.IntervalS, t.CreatedAt, t.NextAt)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrPullExists
			}
			return err
		}
		out = t
		return nil
	})
	return out, err
}

// BindPullTarget binds an unbound target to the machine that said hello (machines.md 3.5
// hub rule 1) and makes its pull device. A target already bound to that machine is a no-op;
// bound to another machine is an error. It returns the device id.
func (s *Store) BindPullTarget(ctx context.Context, id, machineID, machineName string) (string, error) {
	var dev string
	err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.CheckNotFrozen(); err != nil {
			return err
		}
		var bound, devID sql.NullString
		var name string
		if err := tx.queryRow(`SELECT machine_id, device_id, name FROM pull_target WHERE id = ?`, id).Scan(&bound, &devID, &name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if bound.Valid && bound.String != "" {
			if bound.String != machineID {
				return fmt.Errorf("pull target is bound to %s, not %s", bound.String, machineID)
			}
			if devID.Valid && devID.String != "" {
				dev = devID.String
				return nil
			}
		}
		if machineName == "" {
			machineName = name
		}
		d, err := tx.NewPullDevice(id, machineID, machineName)
		if err != nil {
			return err
		}
		dev = d
		_, err = tx.exec(`UPDATE pull_target SET machine_id = ?, device_id = ? WHERE id = ?`, machineID, d, id)
		if err != nil && strings.Contains(err.Error(), "UNIQUE") {
			return ErrPullExists
		}
		return err
	})
	return dev, err
}

// PullResult is what one run did to a target's row.
type PullResult struct {
	State     string // one of the PullState* values
	Error     string // one line, no secrets; "" clears it
	OK        bool   // the run spoke the protocol: sets last_ok_at and clears fail_count
	NextAt    int64  // ms; 0 leaves the stored value
	FailCount int    // new fail_count when !OK
	// GuardNextAt makes NextAt apply only while the stored next_at is still WasNextAt (what
	// the run started with): a "Pull now" during the run changed it and is kept.
	GuardNextAt bool
	WasNextAt   int64
}

// RecordPullRun stores the outcome of a run (last_try_at = now). A target the owner
// disabled meanwhile keeps state disabled.
func (s *Store) RecordPullRun(ctx context.Context, id string, r PullResult) error {
	return s.Write(ctx, func(tx *Tx) error {
		if err := tx.CheckNotFrozen(); err != nil {
			return err
		}
		now := tx.NowMS()
		fail := r.FailCount
		if r.OK {
			fail = 0
		}
		res, err := tx.exec(`UPDATE pull_target SET state = CASE WHEN enabled = 0 THEN 'disabled' ELSE ? END, last_try_at = ?,
			last_ok_at = CASE WHEN ? THEN ? ELSE last_ok_at END, last_error = ?, fail_count = ?,
			next_at = CASE WHEN ? > 0 AND (NOT ? OR next_at IS ?) THEN ? ELSE next_at END WHERE id = ?`,
			r.State, now, r.OK, now, nullStr(r.Error), fail, r.NextAt, r.GuardNextAt, nullInt(r.WasNextAt), r.NextAt, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SchedulePullNow sets a target's next run to now (`sy hub pull run`, POST .../pull).
func (s *Store) SchedulePullNow(ctx context.Context, id string) error {
	return s.Write(ctx, func(tx *Tx) error {
		res, err := tx.exec(`UPDATE pull_target SET next_at = ? WHERE id = ?`, tx.NowMS(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SetPullEnabled enables or disables a target. Enabling goes back to state new with a run
// due now; disabling sets state disabled.
func (s *Store) SetPullEnabled(ctx context.Context, id string, enabled bool) error {
	return s.Write(ctx, func(tx *Tx) error {
		var res sql.Result
		var err error
		if enabled {
			res, err = tx.exec(`UPDATE pull_target SET enabled = 1, state = CASE WHEN state = 'disabled' THEN 'new' ELSE state END,
				next_at = ? WHERE id = ?`, tx.NowMS(), id)
		} else {
			res, err = tx.exec(`UPDATE pull_target SET enabled = 0, state = 'disabled', next_at = NULL WHERE id = ?`, id)
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// TrustPullKeys re-pins a target's host keys (`sy hub pull trust`): state back to new, no
// failures, a run due now.
func (s *Store) TrustPullKeys(ctx context.Context, id string, keys []string) error {
	return s.Write(ctx, func(tx *Tx) error {
		b, _ := json.Marshal(keys)
		res, err := tx.exec(`UPDATE pull_target SET host_keys = ?, state = CASE WHEN enabled = 1 THEN 'new' ELSE 'disabled' END,
			fail_count = 0, last_error = NULL, next_at = ? WHERE id = ?`, string(b), tx.NowMS(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// RemovePullTarget deletes a target; with revoke also its pull device.
func (s *Store) RemovePullTarget(ctx context.Context, id string, revoke bool) error {
	return s.Write(ctx, func(tx *Tx) error {
		var dev sql.NullString
		if err := tx.queryRow(`SELECT device_id FROM pull_target WHERE id = ?`, id).Scan(&dev); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.exec(`DELETE FROM pull_target WHERE id = ?`, id); err != nil {
			return err
		}
		if revoke && dev.Valid && dev.String != "" {
			if err := tx.RevokeDeviceCascade(dev.String); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		return nil
	})
}

// PullHello is what a drain hello tells the hub (machines.md 3.5 hub rule 2).
type PullHello struct {
	Machine       string
	SyVersion     string
	Backlog       int64
	BacklogOldest int64 // ms, 0 = none
	RecordV       int
}

// PullHeartbeat counts a hello as contact: last_seen = now, backlog and its age (cleared
// when none), sy_version, reach = pull and the highest record v the machine writes. It
// never touches last_record.
func (s *Store) PullHeartbeat(ctx context.Context, h PullHello) error {
	return s.Write(ctx, func(tx *Tx) error {
		now := tx.NowMS()
		bk := h.Backlog
		if err := tx.MachineHeartbeat(Heartbeat{Machine: h.Machine, LastSeen: now, Backlog: &bk, SyVersion: h.SyVersion,
			Reach: "pull", BacklogOldest: h.BacklogOldest}); err != nil {
			return err
		}
		if h.RecordV > 0 {
			return tx.SetMachineRecordV(h.Machine, h.RecordV)
		}
		return nil
	})
}

// SetMachineRecordV stores the highest record v a machine writes (a drain hello's record_v).
func (tx *Tx) SetMachineRecordV(machine string, v int) error {
	_, err := tx.exec(`UPDATE machine SET record_v = ? WHERE id = ?`, v, machine)
	return err
}

// SetMachineHeld stores how many records a machine holds back for an older hub (drain done).
func (s *Store) SetMachineHeld(ctx context.Context, machine string, held int64) error {
	return s.Write(ctx, func(tx *Tx) error {
		if err := tx.CheckNotFrozen(); err != nil {
			return err
		}
		_, err := tx.exec(`UPDATE machine SET held = ? WHERE id = ?`, held, machine)
		return err
	})
}
