package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

type machineRow struct {
	reach          sql.NullString
	backlog        sql.NullInt64
	backlogOldest  sql.NullInt64
	backlogAt      sql.NullInt64
	held           int
	lastSeen, ver  sql.NullString
	lastSeenMillis sql.NullInt64
}

func readMachine(t *testing.T, s *Store, id string) machineRow {
	t.Helper()
	var r machineRow
	err := s.R().QueryRow(`SELECT reach, backlog, backlog_oldest, backlog_at, held, last_seen, sy_version FROM machine WHERE id = ?`, id).
		Scan(&r.reach, &r.backlog, &r.backlogOldest, &r.backlogAt, &r.held, &r.lastSeenMillis, &r.ver)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Heartbeat writes reach, backlog_oldest and backlog_at only when it has something to say
// (machines.md 3.4, 6.1) and leaves what a previous heartbeat stored otherwise.
func TestHeartbeatPhase5Fields(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const m = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"
	if err := s.Write(ctx, func(tx *Tx) error {
		_, err := tx.EnsureMachine(m, "gpu-box", 1000)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	beat := func(h Heartbeat) {
		t.Helper()
		h.Machine = m
		if err := s.Write(ctx, func(tx *Tx) error { return tx.MachineHeartbeat(h) }); err != nil {
			t.Fatal(err)
		}
	}
	n := func(v int64) *int64 { return &v }

	// An old-style heartbeat leaves the new columns NULL and held 0.
	beat(Heartbeat{LastSeen: 2000, Backlog: n(0), SyVersion: "0.4.2"})
	r := readMachine(t, s, m)
	if r.reach.Valid || r.held != 0 || r.backlogOldest.Valid {
		t.Fatalf("after a phase 4 heartbeat: %+v", r)
	}
	if !r.backlogAt.Valid || r.backlogAt.Int64 != 2000 {
		t.Fatalf("a backlog report sets backlog_at: %+v", r)
	}

	// A pull hello: reach pull, backlog and the oldest unanswered record.
	beat(Heartbeat{LastSeen: 3000, Backlog: n(1240), Reach: "pull", BacklogOldest: 1700000000000})
	r = readMachine(t, s, m)
	if r.reach.String != "pull" || r.backlog.Int64 != 1240 || r.backlogOldest.Int64 != 1700000000000 || r.backlogAt.Int64 != 3000 {
		t.Fatalf("after hello: %+v", r)
	}

	// A heartbeat that says nothing about backlog or reach keeps all three.
	beat(Heartbeat{LastSeen: 4000, LastRecord: 3500})
	r = readMachine(t, s, m)
	if r.reach.String != "pull" || r.backlogOldest.Int64 != 1700000000000 || r.backlogAt.Int64 != 3000 {
		t.Fatalf("silent heartbeat changed them: %+v", r)
	}

	// A drained backlog (reported, oldest 0) clears backlog_oldest.
	beat(Heartbeat{LastSeen: 5000, Backlog: n(0)})
	r = readMachine(t, s, m)
	if r.backlogOldest.Valid || r.backlogAt.Int64 != 5000 || r.reach.String != "pull" {
		t.Fatalf("after drain: %+v", r)
	}
}

// Schema v4 constraints of machines.md 9.
func TestSchemaV4Constraints(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(ctx); v < 4 {
		t.Fatalf("schema version %d, want at least 4", v)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		_, e := tx.EnsureMachine("m_x", "x", 1)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"bad reach":    `UPDATE machine SET reach = 'ssh' WHERE id = 'm_x'`,
		"bad info":     `UPDATE machine SET info = 'not json' WHERE id = 'm_x'`,
		"bad state":    `INSERT INTO pull_target (id, name, host, user, host_keys, created_at, state) VALUES ('pt_1', 'a', 'h', 'u', '[]', 0, 'frozen')`,
		"bad interval": `INSERT INTO pull_target (id, name, host, user, host_keys, created_at, interval_s) VALUES ('pt_1', 'a', 'h', 'u', '[]', 0, 5)`,
		"bad port":     `INSERT INTO pull_target (id, name, host, user, host_keys, created_at, port) VALUES ('pt_1', 'a', 'h', 'u', '[]', 0, 0)`,
		"bad keys":     `INSERT INTO pull_target (id, name, host, user, host_keys, created_at) VALUES ('pt_1', 'a', 'h', 'u', 'nope', 0)`,
	} {
		if _, e := s.w.ExecContext(ctx, q); e == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, e := s.w.ExecContext(ctx, `INSERT INTO pull_target (id, name, machine_id, host, user, host_keys, created_at) VALUES ('pt_1', 'a', 'm_x', 'h', 'u', '["ssh-ed25519 AAAA"]', 0)`); e != nil {
		t.Fatalf("valid pull_target: %v", e)
	}
	var state string
	var interval, port, enabled int
	if e := s.R().QueryRow(`SELECT state, interval_s, port, enabled FROM pull_target WHERE id = 'pt_1'`).Scan(&state, &interval, &port, &enabled); e != nil {
		t.Fatal(e)
	}
	if state != "new" || interval != 60 || port != 22 || enabled != 1 {
		t.Fatalf("defaults: %s %d %d %d", state, interval, port, enabled)
	}
	// machine_id is UNIQUE: one target per machine.
	if _, e := s.w.ExecContext(ctx, `INSERT INTO pull_target (id, name, machine_id, host, user, host_keys, created_at) VALUES ('pt_2', 'b', 'm_x', 'h', 'u', '[]', 0)`); e == nil {
		t.Error("two pull targets for one machine accepted")
	}
}
