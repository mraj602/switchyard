package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func schemaOf(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT type || ' ' || name, coalesce(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	return out
}

// The first migration is version 1 of the spec, and running every migration gives exactly
// the sqlite_schema of the current docs/spec/schema.sql.
func TestMigrationsMatchSpec(t *testing.T) {
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 2 || !strings.Contains(ms[0].SQL, "Watchyard hub schema, version 1.") {
		t.Fatalf("migration 0001 must be version 1 of schema.sql")
	}
	spec, err := os.ReadFile(specSchema)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 1 && ms[0].SQL != string(spec) {
		t.Fatal("with one migration, 0001 must equal docs/spec/schema.sql byte for byte")
	}

	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := schemaOf(t, s.R())

	ref, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "spec.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()
	ref.SetMaxOpenConns(1)
	if _, err := ref.Exec(string(spec)); err != nil {
		t.Fatal(err)
	}
	want := schemaOf(t, ref)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s differs:\nmigrated: %s\nspec:     %s", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s only in the migrated database", k)
		}
	}
	if v, _ := s.SchemaVersion(ctx); v != LatestVersion() {
		t.Fatalf("version %d", v)
	}
}

// Reopening an up-to-date database changes nothing; opening a v1 database migrates it and
// keeps a copy of the old file first.
func TestOpenMigratesAndCopies(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	ms, _ := Migrations()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ms[0].SQL); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	baks, _ := filepath.Glob(path + ".pre-v*.bak")
	if len(baks) != 1 {
		t.Fatalf("backups %v", baks)
	}
	s, err = Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	baks, _ = filepath.Glob(path + ".pre-v*.bak")
	if len(baks) != 1 {
		t.Fatalf("second open made another copy: %v", baks)
	}
	if v, _ := s.SchemaVersion(ctx); v != LatestVersion() {
		t.Fatalf("version %d", v)
	}
}

func TestWritesSerializedReadsConcurrent(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			err := s.Write(ctx, func(tx *Tx) error {
				_, err := tx.EnsureMachine(NewID("m_", time.Now()), "x", int64(i))
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
		go func() {
			defer wg.Done()
			var n int
			if err := s.R().QueryRow(`SELECT count(*) FROM machine`).Scan(&n); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var n int
	_ = s.R().QueryRow(`SELECT count(*) FROM machine`).Scan(&n)
	if n != 20 {
		t.Fatalf("%d machines", n)
	}
	if _, err := s.R().Exec(`DELETE FROM machine`); err == nil {
		t.Fatal("read pool accepted a write")
	}
}

func TestBackupAndPrune(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "hub.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	recent := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	err = s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.EnsureMachine("m_1", "laptop", old); err != nil {
			return err
		}
		if err := tx.InsertDevice(Device{ID: "dev_1", Kind: "machine", Name: "laptop", MachineID: "m_1", TokenHint: "x",
			Scopes: []string{"write"}, CreatedBy: "test"}, make([]byte, 32)); err != nil {
			return err
		}
		for i, ts := range []int64{old, recent} {
			id := []string{"0000000000000000000A", "0000000000000000000B"}[i]
			if _, _, err := tx.InsertRecord(RawRecord{ID: id, DeviceID: "dev_1", MachineID: "m_1", Kind: "hook_event", V: 1, TS: ts, ReceivedAt: ts, Body: []byte(`{}`)}); err != nil {
				return err
			}
			if err := tx.InsertSession(SessionRow{ID: "s" + id, MachineID: "m_1", StartedAt: ts, LastEventAt: ts}); err != nil {
				return err
			}
			if err := tx.InsertHookEvent(HookEventRow{RecordID: id, SessionID: "s" + id, Event: "Stop", TS: ts}); err != nil {
				return err
			}
			if err := tx.MergeTokenRow(TokenRowIn{Key: "k" + id, SessionID: "s" + id, TS: ts, RecordID: id}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bak := filepath.Join(dir, "backups", BackupName(time.Now()))
	if err := s.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	cut := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	dry, err := s.Prune(ctx, cut, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Records != 1 || dry.HookEvents != 1 || dry.TokenRows != 1 {
		t.Fatalf("dry run %+v", dry)
	}
	if _, err := s.Prune(ctx, cut, false); err != nil {
		t.Fatal(err)
	}
	count := func(db *sql.DB, table string) int {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(s.R(), "record") != 1 || count(s.R(), "hook_event") != 1 || count(s.R(), "token_row") != 1 {
		t.Fatal("prune removed the wrong rows")
	}
	if count(s.R(), "session") != 2 {
		t.Fatal("prune must keep sessions")
	}
	b, err := sql.Open("sqlite", bak)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if count(b, "record") != 2 {
		t.Fatal("backup incomplete")
	}
}
