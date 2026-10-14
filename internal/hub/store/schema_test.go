package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// specSchema is the canonical hub schema (the full current version).
const specSchema = "../../../docs/spec/schema.sql"

func TestSpecSchemaApplies(t *testing.T) {
	ddl, err := os.ReadFile(specSchema)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx) // one connection, so the PRAGMA applies to the DDL
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatalf("schema.sql does not apply: %v", err)
	}

	var version int
	if err := conn.QueryRowContext(ctx, "SELECT max(version) FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != LatestVersion() {
		t.Fatalf("schema_version = %d, want %d (the newest embedded migration)", version, LatestVersion())
	}

	want := []string{
		"schema_version", "setting", "owner", "account", "org", "seat", "machine", "slot",
		"slot_login", "web_seat", "device", "hub_secret", "pair_code", "channel", "push_target", "record",
		"session", "session_state", "hook_event", "token_row", "limit_hit", "history_day",
		"limit_sample", "web_usage", "usage_window", "rollup_daily", "rule", "alert", "delivery",
		"session_transition", "pull_target",
		"owner_key", "device_key", "revocation_doc", "link_state", "link_queue",
		"matrix_doc", "peer_thread", "peer_message", "peer_audit",
	}
	for _, name := range want {
		var strict int
		err := conn.QueryRowContext(ctx, "SELECT strict FROM pragma_table_list WHERE schema = 'main' AND name = ?", name).Scan(&strict)
		if err == sql.ErrNoRows {
			t.Errorf("table %s missing", name)
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		if strict != 1 {
			t.Errorf("table %s is not STRICT", name)
		}
	}

	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("foreign_key_check reports violations")
	}
	rows.Close()
}

// The raw record table must keep a web usage body whole, unknown keys included.
func TestRecordKeepsRawBody(t *testing.T) {
	ddl, err := os.ReadFile(specSchema)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT OR IGNORE INTO owner (id, created_at) VALUES ('owner', 0)`)
	mustExec(`INSERT INTO machine (id, name, first_seen) VALUES ('m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K', 'laptop', 0)`)
	mustExec(`INSERT INTO device (id, kind, name, machine_id, token_hash, token_hint, scopes, created_at, created_by)
	          VALUES ('dev_1', 'desktop', 'laptop', 'm_01JA2W5N0G8H3Q7S2V6X9Y1Z4K', randomblob(32), 'sy_d_…abcd', '["write","read"]', 0, 'test')`)
	body := `{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T9B4CD","kind":"web_usage","ts":"2026-10-11T14:05:00.120Z","payload":{"usage":{"iguana_necktie":{"utilization":3},"brand_new_field":[1,2]}}}`
	mustExec(`INSERT INTO record (id, device_id, machine_id, kind, v, ts, received_at, body)
	          VALUES ('01JA2XQ8R7Y5V0Z6M3K1T9B4CD', 'dev_1', 'm_01JA2W5N0G8H3Q7S2V6X9Y1Z4K', 'web_usage', 1, 0, 0, ?)`, body)
	var got string
	if err := db.QueryRow(`SELECT body ->> '$.payload.usage.iguana_necktie.utilization' FROM record`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "3" {
		t.Fatalf("iguana_necktie lost: %q", got)
	}
	// STRICT and CHECK constraints reject a non-JSON body.
	if _, err := db.Exec(`INSERT INTO record (id, device_id, machine_id, kind, v, ts, received_at, body)
	          VALUES ('01JA2XQ8R7Y5V0Z6M3K1T9B4CE', 'dev_1', 'm_x', 'hook_event', 1, 0, 0, 'not json')`); err == nil {
		t.Fatal("non-JSON body accepted")
	}
}

// Schema v3 seeds the two channels and the seven base rules (push.md 9), in the spec file and
// in the migrated database alike.
func TestSchemaV3Seeds(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	count := func(q string) int {
		t.Helper()
		var n int
		if err := s.R().QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM channel WHERE kind IN ('webpush', 'ntfy') AND enabled = 1`); n != 2 {
		t.Errorf("channels seeded: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM rule WHERE base = 1`); n != 7 {
		t.Errorf("base rules seeded: %d, want 7", n)
	}
	if n := count(`SELECT enabled FROM rule WHERE id = 'rule_reset'`); n != 0 {
		t.Errorf("rule_reset must be off by default")
	}
	if n := count(`SELECT delay_s FROM rule WHERE id = 'rule_asked'`); n != 60 {
		t.Errorf("rule_asked delay %d, want 60", n)
	}
	for _, col := range []string{"expires_at", "prefs"} {
		if n := count(`SELECT count(*) FROM pragma_table_info('device') WHERE name = '` + col + `'`); n != 1 {
			t.Errorf("device.%s missing", col)
		}
	}
	if n := count(`SELECT count(*) FROM hub_secret`); n != 0 {
		t.Errorf("hub_secret must start empty")
	}
}

// seedV4 builds a v4 database at path (migrations 0001..0004, as a phase 5 hub left it) and
// runs seed on it.
func seedV4(t *testing.T, path string, seed ...string) {
	t.Helper()
	seedAt(t, 4, path, seed...)
}

// seedAt builds a database at schema version v (migrations 0001..v) at path and runs seed on
// it.
func seedAt(t *testing.T, v int, path string, seed ...string) {
	t.Helper()
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) <= v {
		t.Fatalf("%d migrations, want more than %d", len(ms), v)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, m := range ms[:v] {
		if _, err := db.Exec(m.SQL); err != nil {
			t.Fatalf("%s: %v", m.Name, err)
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO schema_version (version, applied_at, sy_version) VALUES (?, 0, 'test')`, m.Version); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range seed {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

const v4Seats = `INSERT OR IGNORE INTO owner (id, created_at) VALUES ('owner', 0);
INSERT INTO account (uuid, first_seen, last_seen) VALUES ('a1', 0, 0), ('a2', 0, 0), ('a3', 0, 0);
INSERT INTO org (uuid, first_seen, last_seen) VALUES ('o1', 0, 0);
INSERT INTO seat (id, account_uuid, org_uuid, label, colour, created_at) VALUES
  ('seat_labelled', 'a1', 'o1', 'work', '#3b82f6', 0),
  ('seat_colour', 'a2', 'o1', NULL, '#22c55e', 0),
  ('seat_plain', 'a3', 'o1', NULL, NULL, 0);`

func lookSrcs(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.R().Query(`SELECT id, coalesce(look_src, 'NULL') FROM seat`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, src string
		if err := rows.Scan(&id, &src); err != nil {
			t.Fatal(err)
		}
		out[id] = src
	}
	return out
}

// Schema v5 (desktop.md C10): migrating a seeded v4 database backfills seat.look_src:
// 'import' for a labelled or coloured seat on a hub that ran the prototype importer
// (device dev_import), else 'slot' where a label is set, else NULL.
func TestSchemaV5BackfillsLookSrc(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		seed []string
		want map[string]string
	}{
		{"no importer", []string{v4Seats},
			map[string]string{"seat_labelled": "slot", "seat_colour": "NULL", "seat_plain": "NULL"}},
		{"importer ran", []string{v4Seats,
			`INSERT INTO machine (id, name, first_seen) VALUES ('m_proto', 'laptop', 0)`,
			`INSERT INTO device (id, kind, name, machine_id, token_hash, token_hint, scopes, created_at, created_by, revoked_at)
			 VALUES ('dev_import', 'machine', 'prototype import', 'm_proto', randomblob(32), 'import', '["write"]', 0, 'import', 0)`},
			map[string]string{"seat_labelled": "import", "seat_colour": "import", "seat_plain": "NULL"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hub.db")
			seedV4(t, path, c.seed...)
			s, err := Open(ctx, path, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if v, _ := s.SchemaVersion(ctx); v != LatestVersion() {
				t.Fatalf("schema version %d, want %d", v, LatestVersion())
			}
			got := lookSrcs(t, s)
			for id, want := range c.want {
				if got[id] != want {
					t.Errorf("%s: look_src %s, want %s", id, got[id], want)
				}
			}
			// The CHECK allows only ui, slot, import and NULL.
			if _, err := s.w.ExecContext(ctx, `UPDATE seat SET look_src = 'phone' WHERE id = 'seat_plain'`); err == nil {
				t.Error("look_src 'phone' accepted")
			}
			for _, ok := range []string{"ui", "slot", "import"} {
				if _, err := s.w.ExecContext(ctx, `UPDATE seat SET look_src = ? WHERE id = 'seat_plain'`, ok); err != nil {
					t.Errorf("look_src %s refused: %v", ok, err)
				}
			}
		})
	}
}

// A v5 hub (phase 7) with a Web Push target, an ntfy target and deliveries for both.
const v5Push = `INSERT OR IGNORE INTO owner (id, created_at) VALUES ('owner', 0);
INSERT INTO push_target (id, kind, device_id, name, endpoint, keys, created_at, updated_at) VALUES
  ('pt_web', 'webpush', NULL, 'iPhone · Safari', 'https://web.push.apple.com/x', '{"p256dh":"p","auth":"a"}', 1, 1),
  ('pt_ntfy', 'ntfy', NULL, 'ntfy.sh/sy-x', 'https://ntfy.sh/sy-x', NULL, 1, 1);
INSERT INTO alert (id, episode, trigger, status, created_at, updated_at) VALUES ('al_1', 'ep1', 'asked', 'fired', 1, 1);
INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, tries, created_at, updated_at) VALUES
  ('al_1', 'pt_web', 'webpush', 1, 'sent', 1, 1, 2),
  ('al_1', 'pt_ntfy', 'ntfy', 1, 'sent', 1, 1, 2);`

// Schema v6 (push.md 12): migrating a seeded v5 database seeds the expo channel, adds
// push_target.meta (JSON or NULL) and delivery.ticket / receipt_at, all NULL on old rows, and
// the open-ticket index. Migrating v1 to v6 equals the spec (TestMigrationsMatchSpec).
func TestSchemaV6Expo(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	seedAt(t, 5, path, v5Push)
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(ctx); v != LatestVersion() || v < 6 {
		t.Fatalf("schema version %d, want %d (>= 6)", v, LatestVersion())
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := s.R().QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM channel WHERE kind = 'expo' AND enabled = 1 AND settings = '{}' AND updated_at = 0`); n != 1 {
		t.Errorf("expo channel rows: %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM channel`); n != 4 { // v8 adds link (push.md 13)
		t.Errorf("channels: %d, want 4", n)
	}
	if n := count(`SELECT count(*) FROM push_target WHERE meta IS NULL`); n != 2 {
		t.Errorf("old targets with meta NULL: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM delivery WHERE ticket IS NULL AND receipt_at IS NULL`); n != 2 {
		t.Errorf("old deliveries without a ticket: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = 'delivery_ticket_open'`); n != 1 {
		t.Error("index delivery_ticket_open missing")
	}

	// An expo target with meta, and a sent delivery holding a ticket.
	if _, err := s.w.ExecContext(ctx, `INSERT INTO push_target (id, kind, endpoint, meta, created_at, updated_at)
		VALUES ('pt_expo', 'expo', 'ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]', '{"platform":"ios","app_version":"0.8.0","app_build":"12"}', 3, 3)`); err != nil {
		t.Fatalf("expo target refused: %v", err)
	}
	if _, err := s.w.ExecContext(ctx, `UPDATE push_target SET meta = 'not json' WHERE id = 'pt_expo'`); err == nil {
		t.Error("non-JSON meta accepted")
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, tries, created_at, updated_at, ticket)
		VALUES ('al_1', 'pt_expo', 'expo', 1, 'sent', 1, 3, 3, 't1')`); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM delivery INDEXED BY delivery_ticket_open WHERE ticket IS NOT NULL AND receipt_at IS NULL AND created_at > 0`); n != 1 {
		t.Errorf("open tickets: %d, want 1", n)
	}
	if _, err := s.w.ExecContext(ctx, `UPDATE delivery SET receipt_at = 4 WHERE ticket = 't1'`); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM delivery WHERE ticket IS NOT NULL AND receipt_at IS NULL`); n != 0 {
		t.Errorf("open tickets after the receipt: %d, want 0", n)
	}
}

// Schema v8 (push.md 13.8): migrating a seeded v6 database recreates channel and push_target
// with 'link' allowed, keeps every target and its columns, keeps each channel's switch and
// settings, seeds the link channel and adds delivery.delivered_at (NULL on old rows). Schema v7
// and v9 (keys.md 9, peers.md 10) add the key, link and peer tables with their CHECKs.
func TestSchemaV8LinkKeepsTargets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	seedAt(t, 6, path, v5Push,
		`INSERT INTO push_target (id, kind, endpoint, meta, created_at, updated_at, fail_count, last_error)
		 VALUES ('pt_expo', 'expo', 'ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]', '{"platform":"android"}', 3, 3, 2, 'HTTP 500')`,
		`UPDATE channel SET enabled = 0, settings = '{"base_url":"https://exp.host"}' WHERE kind = 'expo'`)
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(ctx); v != LatestVersion() || v < 9 {
		t.Fatalf("schema version %d, want %d (>= 9)", v, LatestVersion())
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := s.R().QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM push_target`); n != 3 {
		t.Errorf("targets after v8: %d, want 3", n)
	}
	if n := count(`SELECT count(*) FROM push_target WHERE id = 'pt_expo' AND meta = '{"platform":"android"}' AND fail_count = 2 AND last_error = 'HTTP 500'`); n != 1 {
		t.Error("expo target columns not kept")
	}
	if n := count(`SELECT count(*) FROM push_target WHERE id = 'pt_web' AND keys = '{"p256dh":"p","auth":"a"}'`); n != 1 {
		t.Error("webpush keys not kept")
	}
	if n := count(`SELECT count(*) FROM channel WHERE kind = 'expo' AND enabled = 0 AND settings = '{"base_url":"https://exp.host"}'`); n != 1 {
		t.Error("expo channel switch or settings not kept")
	}
	if n := count(`SELECT count(*) FROM channel WHERE kind = 'link' AND enabled = 1`); n != 1 {
		t.Error("link channel not seeded")
	}
	if n := count(`SELECT count(*) FROM delivery WHERE delivered_at IS NULL`); n != 2 {
		t.Errorf("old deliveries with delivered_at NULL: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = 'push_target_device'`); n != 1 {
		t.Error("index push_target_device missing")
	}
	mustExec := func(q string) {
		t.Helper()
		if _, err := s.w.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	refused := func(what, q string) {
		t.Helper()
		if _, err := s.w.ExecContext(ctx, q); err == nil {
			t.Errorf("%s accepted", what)
		}
	}
	mustExec(`INSERT INTO device (id, kind, name, token_hash, token_hint, scopes, created_at, created_by)
	          VALUES ('dev_p', 'phone', 'Pixel · app', randomblob(32), 'sy_p_…abcd', '["read","push"]', 0, 'test')`)
	mustExec(`INSERT INTO push_target (id, kind, device_id, endpoint, keys, created_at, updated_at)
	          VALUES ('pt_link', 'link', 'dev_p', 'link:dev_p', '{"p256dh":"p","auth":"a"}', 4, 4)`)
	refused("unknown channel kind", `INSERT INTO push_target (id, kind, endpoint, created_at, updated_at) VALUES ('pt_x', 'carrier', 'x', 5, 5)`)
	mustExec(`INSERT INTO device_key (device_id, sign_alg, sign_pub, box_alg, box_pub, reg, cert, hub_sig, created_at)
	          VALUES ('dev_p', 'p256', randomblob(65), 'p256', randomblob(65), 'qr', CAST('{}' AS BLOB), randomblob(64), 1)`)
	refused("owner_fp without owner_sig", `UPDATE device_key SET owner_fp = 'x' WHERE device_id = 'dev_p'`)
	mustExec(`INSERT INTO owner_key (fp, pub, body, self_sig, hub_sig, state, proposed_by, created_at) VALUES ('a', randomblob(32), x'7b7d', randomblob(64), randomblob(64), 'active', 'cli', 1)`)
	refused("two active owner keys", `INSERT INTO owner_key (fp, pub, body, self_sig, hub_sig, state, proposed_by, created_at) VALUES ('b', randomblob(32), x'7b7d', randomblob(64), randomblob(64), 'active', 'cli', 1)`)
	refused("active owner key without the hub's signature", `INSERT INTO owner_key (fp, pub, body, self_sig, state, proposed_by, created_at) VALUES ('c', randomblob(32), x'7b7d', randomblob(64), 'retired', 'cli', 1)`)
	refused("pending owner key with a hub signature", `INSERT INTO owner_key (fp, pub, body, self_sig, hub_sig, state, proposed_by, created_at) VALUES ('d', randomblob(32), x'7b7d', randomblob(64), randomblob(64), 'pending', 'cli', 1)`)
	mustExec(`INSERT INTO owner_key (fp, pub, body, self_sig, state, proposed_by, created_at) VALUES ('e', randomblob(32), x'7b7d', randomblob(64), 'pending', 'dev_p', 1)`)
	mustExec(`INSERT INTO link_state (device_id) VALUES ('dev_p')`)
	mustExec(`INSERT INTO link_queue (device_id, seq, type, tag, body, sig, exp, created_at)
	          VALUES ('dev_p', 1, 'alert', 'sy-s-1', x'00', randomblob(64), 10, 1)`)
	refused("unknown link frame type", `INSERT INTO link_queue (device_id, seq, type, body, sig, exp, created_at)
	          VALUES ('dev_p', 2, 'chat', x'00', randomblob(64), 10, 1)`)
	refused("link tag with a newline", `INSERT INTO link_queue (device_id, seq, type, tag, body, sig, exp, created_at)
	          VALUES ('dev_p', 3, 'alert', 'a'||char(10)||'b', x'00', randomblob(64), 10, 1)`)
	refused("link tag over 64 characters", `INSERT INTO link_queue (device_id, seq, type, tag, body, sig, exp, created_at)
	          VALUES ('dev_p', 4, 'alert', printf('%065d', 0), x'00', randomblob(64), 10, 1)`)
	mustExec(`INSERT INTO link_queue (device_id, seq, type, tag, body, sig, exp, created_at)
	          VALUES ('dev_p', 5, 'peer_status', 'ps:5f0c9a2e-1b2c.x_y', x'00', randomblob(64), 10, 1)`)
	mustExec(`INSERT INTO peer_thread (id, a_session, b_session, a_account, b_account, opened_at, last_at)
	          VALUES ('t_0123456789abcdef', 's1', 's2', 'a1', 'a2', 1, 1)`)
	refused("thread with itself", `INSERT INTO peer_thread (id, a_session, b_session, a_account, b_account, opened_at, last_at)
	          VALUES ('t_same', 's1', 's1', 'a1', 'a1', 1, 1)`)
	refused("unknown peer status", `INSERT INTO peer_message (id, thread_id, from_session, from_device, from_account, to_session,
	          to_device, to_account, kind, size, status, created_at, expires_at) VALUES ('m1', 't', 's1', 'd1', 'a1', 's2', 'd2', 'a2', 'start', 1, 'read', 1, 2)`)
}
