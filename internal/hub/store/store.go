package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver, registered as "sqlite"
)

// Store is the hub database: one writer connection (all writes are serialized through
// Write) and a pool of read-only connections for concurrent reads. WAL mode lets readers
// run while a write is in progress.
type Store struct {
	path string
	w    *sql.DB // exactly one connection
	r    *sql.DB // read pool, query_only
	mu   sync.Mutex
	now  func() time.Time
}

// Options tune Open. The zero value is fine.
type Options struct {
	// BusyTimeout is how long a connection waits on a lock. Default 10 s.
	BusyTimeout time.Duration
	// ReadConns is the size of the read pool. Default max(4, GOMAXPROCS).
	ReadConns int
	// Now is the clock (tests). Default time.Now.
	Now func() time.Time
	// NoBackupBeforeMigrate skips the copy of an existing database before migrating (tests).
	NoBackupBeforeMigrate bool
	// Log receives one line per migration step and backup. Nil discards.
	Log func(format string, args ...any)
}

func dsn(path string, busy time.Duration, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file:" + p + "?" + q.Encode()
}

// Open opens (creating if needed) the database at path and migrates it to the newest
// schema. Before migrating an existing database it writes a copy next to it
// (<path>.pre-v<N>-<UTC time>.bak).
func Open(ctx context.Context, path string, opt Options) (*Store, error) {
	if opt.BusyTimeout <= 0 {
		opt.BusyTimeout = 10 * time.Second
	}
	if opt.ReadConns <= 0 {
		opt.ReadConns = max(4, runtime.GOMAXPROCS(0))
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = func(string, ...any) {}
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	// Create the file private before SQLite does (it would use the umask, usually 0644, and
	// copy that mode to the -wal and -shm files).
	if f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600); err == nil {
		f.Close()
	}
	w, err := sql.Open("sqlite", dsn(path, opt.BusyTimeout, false))
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	s := &Store{path: path, w: w, now: opt.Now}
	if err := s.migrate(ctx, opt); err != nil {
		w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, opt.BusyTimeout, true))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("store: open readers: %w", err)
	}
	r.SetMaxOpenConns(opt.ReadConns)
	r.SetMaxIdleConns(opt.ReadConns)
	s.r = r
	return s, nil
}

// Path is the database file.
func (s *Store) Path() string { return s.path }

// Close closes both pools. It checkpoints the WAL so the file is complete on its own.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	_, _ = s.w.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	errs = append(errs, s.w.Close())
	return errors.Join(errs...)
}

// R is the read pool. Statements on it cannot write (query_only).
func (s *Store) R() *sql.DB { return s.r }

// Now is the store's clock.
func (s *Store) Now() time.Time { return s.now() }

// Tx is one write transaction. Repository methods hang off it.
type Tx struct {
	*sql.Tx
	ctx   context.Context
	now   time.Time
	after []func()
}

// AfterCommit runs fn once the transaction has committed (never when it rolls back), on
// the goroutine that called Write. Wake-ups of waiting readers belong here: before the
// commit they would read the old state.
func (tx *Tx) AfterCommit(fn func()) { tx.after = append(tx.after, fn) }

// Ctx is the context of the transaction.
func (tx *Tx) Ctx() context.Context { return tx.ctx }

// NowMS is the hub time at the start of the transaction, in ms.
func (tx *Tx) NowMS() int64 { return tx.now.UnixMilli() }

func (tx *Tx) exec(q string, args ...any) (sql.Result, error) {
	return tx.ExecContext(tx.ctx, q, args...)
}

func (tx *Tx) queryRow(q string, args ...any) *sql.Row {
	return tx.QueryRowContext(tx.ctx, q, args...)
}

// Savepoint runs fn inside a savepoint: if fn fails, only its changes are rolled back and
// the transaction continues.
func (tx *Tx) Savepoint(name string, fn func() error) error {
	if _, err := tx.exec("SAVEPOINT " + name); err != nil {
		return err
	}
	if err := fn(); err != nil {
		if _, rerr := tx.exec("ROLLBACK TO " + name); rerr != nil {
			return errors.Join(err, rerr)
		}
		_, _ = tx.exec("RELEASE " + name)
		return err
	}
	_, err := tx.exec("RELEASE " + name)
	return err
}

// Write runs fn in one write transaction (BEGIN IMMEDIATE). Writes are serialized: only
// one Write runs at a time, so no write ever waits on SQLITE_BUSY from another hub write.
func (s *Store) Write(ctx context.Context, fn func(tx *Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	tx := &Tx{Tx: t, ctx: ctx, now: s.now()}
	// A panic in fn must not leave the only writer connection inside an open transaction.
	defer func() {
		if p := recover(); p != nil {
			_ = t.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = t.Rollback()
		return err
	}
	if err := t.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	for _, fn := range tx.after {
		fn()
	}
	return nil
}

// Ping checks the database answers (for /health).
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.r.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// nullInt turns 0 into NULL.
func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// nullStr turns "" into NULL.
func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}
