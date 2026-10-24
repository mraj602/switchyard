package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostileArchive exports an empty hub, lets mutate change the database inside, and packs it
// again with a manifest that matches (an attacker can do the same: the hashes authenticate
// nothing).
func hostileArchive(t *testing.T, mutate func(db *sql.DB)) string {
	t.Helper()
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	out := filepath.Join(t.TempDir(), "x.wyx")
	if _, err := src.Export(ctx, ExportOptions{Out: out, NoFreeze: true}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := unpack(out, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "hub.db")
	db, err := sql.Open("sqlite", FileURI(dbPath, "_pragma=busy_timeout(2000)"))
	if err != nil {
		t.Fatal(err)
	}
	mutate(db)
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	b, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	m.Files = []ManifestFile{{Path: memberDB, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])}}
	var buf bytes.Buffer
	if err := writeMembers(&buf, []member{{name: memberManifest, data: mustJSON(m)}, {name: memberDB, path: dbPath}}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "hostile.wyx")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func importHostile(t *testing.T, p string) (string, error) {
	t.Helper()
	dbp := filepath.Join(t.TempDir(), "hub.db")
	var logs []string
	_, err := Import(context.Background(), ImportOptions{File: p, DBPath: dbp, StateDir: t.TempDir(),
		Log: func(f string, a ...any) { logs = append(logs, f) }})
	return dbp, err
}

// An archive with a trigger, a view, a virtual table or an extra table is refused, and nothing
// is installed.
func TestImportRefusesUnexpectedSchemaObjects(t *testing.T) {
	for name, stmt := range map[string]string{
		"trigger": `CREATE TRIGGER evil AFTER INSERT ON setting BEGIN DELETE FROM device; END`,
		"view":    `CREATE VIEW v AS SELECT * FROM device`,
		"virtual": `CREATE VIRTUAL TABLE ft USING fts5(x)`,
		"table":   `CREATE TABLE extra (x)`,
		"index":   `CREATE INDEX extra_ix ON setting (updated_at)`,
	} {
		t.Run(name, func(t *testing.T) {
			p := hostileArchive(t, func(db *sql.DB) {
				if _, err := db.Exec(stmt); err != nil {
					t.Skipf("this sqlite build cannot make a %s: %v", name, err)
				}
			})
			dbp, err := importHostile(t, p)
			if err == nil || !strings.Contains(err.Error(), "refusing to import") {
				t.Fatalf("got %v", err)
			}
			if _, serr := os.Stat(dbp); serr == nil {
				t.Error("a refused import left a database behind")
			}
		})
	}
}

// Setting keys the hub never writes are dropped; its own keys are kept.
func TestImportDropsUnknownSettings(t *testing.T) {
	p := hostileArchive(t, func(db *sql.DB) {
		for _, k := range []string{"hub.public_url", "evil.key", "hub.admin_override"} {
			if _, err := db.Exec(`INSERT INTO setting (key, value, updated_at) VALUES (?, '"x"', 1)`, k); err != nil {
				t.Fatal(err)
			}
		}
	})
	dbp, err := importHostile(t, p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), dbp, Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for k, want := range map[string]bool{"hub.public_url": true, "evil.key": false, "hub.admin_override": false, "hub.imported": true} {
		_, ok, err := s.AlertSetting(context.Background(), k)
		if err != nil || ok != want {
			t.Errorf("setting %s: present=%v err=%v, want present=%v", k, ok, err, want)
		}
	}
}
