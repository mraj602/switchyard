package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/mraj602/watchyard/internal/version"
)

// Migrations live in migrations/NNNN_name.sql. 0001 is version 1 of docs/spec/schema.sql
// (byte for byte); every later file is one step. docs/spec/schema.sql is always the full
// current schema, and a test checks that running every migration gives the same
// sqlite_schema as loading it.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one numbered schema step.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrations returns the embedded migrations in order.
func Migrations() ([]Migration, error) {
	ents, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("store: migration %s: name must be NNNN_name.sql", name)
		}
		b, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("store: migrations must be numbered 1..n, found %d at %d", m.Version, i+1)
		}
	}
	return out, nil
}

// LatestVersion is the schema version this binary migrates to.
func LatestVersion() int {
	m, err := Migrations()
	if err != nil || len(m) == 0 {
		return 0
	}
	return m[len(m)-1].Version
}

// currentVersion reads schema_version; 0 for an empty database.
func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_version'`).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

// SchemaVersion is the database's current schema version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return currentVersion(ctx, s.r)
}

func (s *Store) migrate(ctx context.Context, opt Options) error {
	ms, err := Migrations()
	if err != nil {
		return err
	}
	cur, err := currentVersion(ctx, s.w)
	if err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	latest := ms[len(ms)-1].Version
	if cur > latest {
		return fmt.Errorf("store: database schema is v%d, this wy knows up to v%d: upgrade wy", cur, latest)
	}
	if cur == latest {
		return nil
	}
	if cur > 0 && !opt.NoBackupBeforeMigrate {
		bak := fmt.Sprintf("%s.pre-v%d-%s.bak", s.path, latest, s.now().UTC().Format("20060102T150405Z"))
		if err := vacuumInto(ctx, s.w, bak); err != nil {
			return fmt.Errorf("store: copy before migrating: %w", err)
		}
		opt.Log("store: copied v%d database to %s before migrating", cur, bak)
	}
	for _, m := range ms[cur:] {
		err := s.Write(ctx, func(tx *Tx) error {
			if _, err := tx.exec(m.SQL); err != nil {
				return fmt.Errorf("store: migration %s: %w", m.Name, err)
			}
			_, err := tx.exec(`INSERT OR REPLACE INTO schema_version (version, applied_at, wy_version) VALUES (?, ?, ?)`,
				m.Version, tx.NowMS(), version.Version)
			return err
		})
		if err != nil {
			return err
		}
		opt.Log("store: migrated to v%d (%s)", m.Version, m.Name)
	}
	// An owner row exists from the start: every owner_id defaults to 'owner'.
	return s.Write(ctx, func(tx *Tx) error {
		_, err := tx.exec(`INSERT OR IGNORE INTO owner (id, created_at) VALUES ('owner', ?)`, tx.NowMS())
		return err
	})
}

func vacuumInto(ctx context.Context, db *sql.DB, to string) error {
	// An empty file made private first: SQLite accepts an existing empty target, and the copy
	// never exists with the default umask mode.
	f, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, to); err != nil {
		_ = os.Remove(to)
		return err
	}
	return nil
}
