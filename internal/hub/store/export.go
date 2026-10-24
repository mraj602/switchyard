package store

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/brand"
	"github.com/mraj602/switchyard/internal/hub/hublock"
)

// Moving the hub (docs/spec/machines.md 8): freeze, snapshot, archive, import. The wire
// format of encrypted archives is in export_syxe.go.

// Setting keys of machines.md 8 (mirrored in internal/hub/api as SettingFrozen/SettingImported;
// the store cannot import api).
const (
	SettingFrozen   = "hub.frozen"
	SettingImported = "hub.imported"
)

// ErrFrozen is what ingest answers (503, "hub is frozen for a move") while hub.frozen is set.
var ErrFrozen = errors.New("store: hub is frozen for a move")

// FrozenInfo is the value of hub.frozen.
type FrozenInfo struct {
	Since   int64   `json:"since"`
	MovedTo *string `json:"moved_to"`
}

// FrozenIn reads hub.frozen inside a write transaction. Ingest calls it in its own write
// transaction (the store has one writer, so every ingest either committed before the freeze
// and is in the snapshot, or sees the freeze here and is refused, machines.md 8.1 step 1).
func (tx *Tx) FrozenIn() (*FrozenInfo, error) {
	raw, ok, err := tx.AlertSetting(SettingFrozen)
	if err != nil || !ok {
		return nil, err
	}
	var f FrozenInfo
	if err := json.Unmarshal(raw, &f); err != nil {
		// A frozen marker we cannot read still means frozen: refuse rather than guess.
		return &FrozenInfo{}, nil
	}
	return &f, nil
}

// CheckNotFrozen returns ErrFrozen when the hub is frozen. Ingest calls it first thing in
// its write transaction.
func (tx *Tx) CheckNotFrozen() error {
	f, err := tx.FrozenIn()
	if err != nil {
		return err
	}
	if f != nil {
		return ErrFrozen
	}
	return nil
}

// Frozen reads hub.frozen (nil when the hub is not frozen).
func (s *Store) Frozen(ctx context.Context) (*FrozenInfo, error) {
	raw, ok, err := s.AlertSetting(ctx, SettingFrozen)
	if err != nil || !ok {
		return nil, err
	}
	var f FrozenInfo
	if json.Unmarshal(raw, &f) != nil {
		return &FrozenInfo{}, nil
	}
	return &f, nil
}

// Freeze sets hub.frozen in one write transaction. An already frozen hub keeps its original
// `since`; movedTo (when not empty) replaces the recorded destination. fresh reports whether
// this call is the one that froze it.
func (s *Store) Freeze(ctx context.Context, movedTo string) (info FrozenInfo, fresh bool, err error) {
	err = s.Write(ctx, func(tx *Tx) error {
		cur, err := tx.FrozenIn()
		if err != nil {
			return err
		}
		if cur != nil {
			info = *cur
		} else {
			info = FrozenInfo{Since: tx.NowMS()}
			fresh = true
		}
		if movedTo != "" {
			info.MovedTo = &movedTo
		}
		raw, _ := json.Marshal(info)
		return tx.PutAlertSetting(SettingFrozen, raw, tx.NowMS())
	})
	return
}

// Unfreeze clears hub.frozen; was reports whether it was set.
func (s *Store) Unfreeze(ctx context.Context) (was bool, err error) {
	err = s.Write(ctx, func(tx *Tx) error {
		res, err := tx.exec(`DELETE FROM setting WHERE key = ?`, SettingFrozen)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		was = n > 0
		return nil
	})
	return
}

// ---- manifest ----

// ManifestFormat and ManifestKind identify a hub export (machines.md 8.1 step 4).
const (
	ManifestFormat = 1
	ManifestKind   = brand.Slug + "-hub-export"
)

// Archive member names.
const (
	memberManifest = "manifest.json"
	memberDB       = "hub.db"
	memberPepper   = "state/pair_pepper"
	memberHubKey   = "state/hub_sign.key"
	memberPullKey  = "state/pull/id_ed25519"
	memberPullPub  = "state/pull/id_ed25519.pub"
)

// ExportCounts is what the manifest says the snapshot holds.
type ExportCounts struct {
	Devices     int64 `json:"devices"`
	Machines    int64 `json:"machines"`
	Records     int64 `json:"records"`
	Sessions    int64 `json:"sessions"`
	PushTargets int64 `json:"push_targets"`
	PullTargets int64 `json:"pull_targets"`
}

// ManifestFile is one archive member's integrity entry.
type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is manifest.json.
type Manifest struct {
	Format     int            `json:"format"`
	Kind       string         `json:"kind"`
	Sy         string         `json:"sy"`
	Schema     int            `json:"schema"`
	ExportedAt string         `json:"exported_at"`
	Frozen     bool           `json:"frozen"`
	MovedTo    *string        `json:"moved_to"`
	PublicURL  string         `json:"public_url"`
	Counts     ExportCounts   `json:"counts"`
	Files      []ManifestFile `json:"files"`
}

// ExportOptions configures Export.
type ExportOptions struct {
	// Out is the archive path (created 0600, its folder 0700); it must not exist.
	Out string
	// StateDir holds pair_pepper; PullDir (default <StateDir>/pull) holds id_ed25519(.pub).
	// With StateDir set the pair_pepper must exist: an export without it cannot validate the
	// pairing codes it carries.
	StateDir, PullDir string
	MovedTo           string
	// NoFreeze exports a copy that is not a move.
	NoFreeze bool
	// Passphrase, when not empty, encrypts the archive (export_syxe.go).
	Passphrase string
	// SyVersion goes in the manifest.
	SyVersion string
}

// ExportResult says what was written.
type ExportResult struct {
	Path      string
	Encrypted bool
	Frozen    bool
	Manifest  Manifest
	Size      int64
}

func tableCount(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n)
	return n, err
}

func openRO(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", FileURI(path, "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// checkIntegrity runs PRAGMA integrity_check on the database file.
func checkIntegrity(ctx context.Context, path string) error {
	db, err := openRO(path)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	defer rows.Close()
	var msgs []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return err
		}
		if m != "ok" {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("integrity check failed: %s", strings.Join(msgs, "; "))
	}
	return rows.Err()
}

func countsOf(ctx context.Context, db *sql.DB) (c ExportCounts, err error) {
	for _, x := range []struct {
		t string
		p *int64
	}{{"device", &c.Devices}, {"machine", &c.Machines}, {"record", &c.Records}, {"session", &c.Sessions},
		{"push_target", &c.PushTargets}, {"pull_target", &c.PullTargets}} {
		if *x.p, err = tableCount(ctx, db, x.t); err != nil {
			return c, err
		}
	}
	return
}

func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func fileInfo(p string) (size int64, sum string, err error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

type member struct {
	name string
	path string // a file on disk, or
	data []byte // bytes in memory
}

func writeMembers(w io.Writer, ms []member) error {
	zw, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	now := time.Now()
	for _, m := range ms {
		var size int64
		var src io.Reader
		if m.path != "" {
			f, err := os.Open(m.path)
			if err != nil {
				return err
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return err
			}
			size, src = st.Size(), f
		} else {
			size, src = int64(len(m.data)), bytes.NewReader(m.data)
		}
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o600, Size: size, ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := io.Copy(tw, src); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// exportWorkHook is a test seam: it sees the snapshot folder while the snapshot exists.
var exportWorkHook func(work string)

// Export freezes the hub (unless NoFreeze), takes a VACUUM INTO snapshot, checks it, and
// writes the archive of machines.md 8.1. When it fails after freezing the hub itself, it
// unfreezes again so a failed export does not leave the hub refusing records.
func (s *Store) Export(ctx context.Context, o ExportOptions) (res ExportResult, err error) {
	if o.Out == "" {
		return res, errors.New("export: no output file")
	}
	if _, err := os.Stat(o.Out); err == nil {
		return res, fmt.Errorf("export: %s already exists", o.Out)
	}
	if err := os.MkdirAll(filepath.Dir(o.Out), 0o700); err != nil {
		return res, err
	}
	var frozen *FrozenInfo
	if !o.NoFreeze {
		info, fresh, ferr := s.Freeze(ctx, o.MovedTo)
		if ferr != nil {
			return res, ferr
		}
		frozen = &info
		defer func() {
			if err != nil && fresh {
				if _, uerr := s.Unfreeze(context.WithoutCancel(ctx)); uerr != nil {
					err = errors.Join(err, fmt.Errorf("the hub is still frozen and could not be unfrozen (%v): run `sy hub unfreeze`", uerr))
				}
			}
		}()
	}
	// The plaintext snapshot lives in a private folder in the hub's own data folder, never
	// next to --out (which may be a shared or synced place, and is meant to hold only the
	// possibly encrypted archive).
	dataDir := filepath.Dir(s.path)
	sweepStale(dataDir, ".export-")
	work, err := os.MkdirTemp(dataDir, ".export-")
	if err != nil {
		return res, err
	}
	if err := os.Chmod(work, 0o700); err != nil {
		os.RemoveAll(work)
		return res, err
	}
	defer func() {
		if rerr := os.RemoveAll(work); rerr != nil {
			err = errors.Join(err, fmt.Errorf("could not remove the unencrypted snapshot %s: delete it by hand", work))
		}
	}()
	snap := filepath.Join(work, "hub.db")
	if err := s.Backup(ctx, snap); err != nil {
		return res, err
	}
	if exportWorkHook != nil {
		exportWorkHook(work)
	}
	if err := checkIntegrity(ctx, snap); err != nil {
		return res, err
	}
	db, err := openRO(snap)
	if err != nil {
		return res, err
	}
	counts, err := countsOf(ctx, db)
	var schema sql.NullInt64
	if err == nil {
		err = db.QueryRowContext(ctx, `SELECT max(version) FROM schema_version`).Scan(&schema)
	}
	var pub string
	if err == nil {
		var raw string
		if e := db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, AlertKeyPublicURL).Scan(&raw); e == nil {
			_ = json.Unmarshal([]byte(raw), &pub)
		}
	}
	db.Close()
	if err != nil {
		return res, err
	}

	size, sum, err := fileInfo(snap)
	if err != nil {
		return res, err
	}
	m := Manifest{Format: ManifestFormat, Kind: ManifestKind, Sy: o.SyVersion, Schema: int(schema.Int64),
		ExportedAt: s.Now().UTC().Format(time.RFC3339), PublicURL: pub, Counts: counts,
		Files: []ManifestFile{{Path: memberDB, Size: size, SHA256: sum}}}
	if frozen != nil {
		m.Frozen, m.MovedTo = true, frozen.MovedTo
	}
	members := []member{{}, {name: memberDB, path: snap}}
	for _, st := range stateFiles(o.StateDir, o.PullDir) {
		b, rerr := os.ReadFile(st.path)
		if errors.Is(rerr, os.ErrNotExist) {
			if st.required {
				return res, fmt.Errorf("export: %s is missing; the export would not carry the pairing and signing keys (start the hub once, or pass the --db and --pull-dir the hub runs with)", st.path)
			}
			continue
		}
		if rerr != nil {
			return res, rerr
		}
		m.Files = append(m.Files, ManifestFile{Path: st.member, Size: int64(len(b)), SHA256: hashBytes(b)})
		members = append(members, member{name: st.member, data: b})
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	members[0] = member{name: memberManifest, data: mb}

	part := o.Out + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return res, err
	}
	defer os.Remove(part)
	var w io.Writer = f
	var enc *syxeWriter
	if o.Passphrase != "" {
		if enc, err = newSyxeWriter(f, o.Passphrase); err != nil {
			f.Close()
			return res, err
		}
		w = enc
	}
	werr := writeMembers(w, members)
	if werr == nil && enc != nil {
		werr = enc.Close()
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return res, werr
	}
	if err := os.Rename(part, o.Out); err != nil {
		return res, err
	}
	st, _ := os.Stat(o.Out)
	res = ExportResult{Path: o.Out, Encrypted: enc != nil, Frozen: frozen != nil, Manifest: m}
	if st != nil {
		res.Size = st.Size()
	}
	return res, nil
}

// stateFile is one state file an export carries and an import restores.
type stateFile struct {
	member   string // archive member name
	path     string // where it lives on this host
	required bool
}

// stateFiles lists the state files for a state folder and a pull folder (default
// <state>/pull); none when stateDir is empty.
func stateFiles(stateDir, pullDir string) []stateFile {
	if stateDir == "" {
		return nil
	}
	if pullDir == "" {
		pullDir = filepath.Join(stateDir, "pull")
	}
	return []stateFile{
		{memberPepper, filepath.Join(stateDir, "pair_pepper"), true},
		{memberHubKey, filepath.Join(stateDir, "hub_sign.key"), true}, // keys.md 3.4
		{memberPullKey, filepath.Join(pullDir, "id_ed25519"), false},
		{memberPullPub, filepath.Join(pullDir, "id_ed25519.pub"), false},
	}
}

// sweepStale removes leftover work folders (prefix) of a crashed run, older than an hour.
func sweepStale(dir, prefix string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// ---- reading an archive ----

// ErrNeedPassphrase means the file is encrypted and no passphrase was given.
var ErrNeedPassphrase = errors.New("the export is encrypted: a passphrase is needed")

// IsEncryptedExport reports whether the file starts with the SYXE magic.
func IsEncryptedExport(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var m [4]byte
	if _, err := io.ReadFull(f, m[:]); err != nil {
		return false, nil
	}
	return string(m[:]) == syxeMagic, nil
}

// maxStateFileBytes bounds the small state members (pairing pepper, signing and pull keys).
const maxStateFileBytes = 1 << 20

// unpack extracts the archive into dir (which must exist, mode 0700) and returns the
// verified manifest. Only the known member names are written; each file's size and SHA-256
// must match the manifest, and every file the manifest lists must be present.
func unpack(path, passphrase, dir string) (Manifest, error) {
	var m Manifest
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	var src io.Reader = f
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return m, errors.New("the export is empty or truncated")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return m, err
	}
	if string(magic[:]) == syxeMagic {
		if passphrase == "" {
			return m, ErrNeedPassphrase
		}
		r, err := newSyxeReader(f, passphrase)
		if err != nil {
			return m, err
		}
		src = r
	}
	zr, err := gzip.NewReader(src)
	if err != nil {
		return m, fmt.Errorf("not a hub export: %w", err)
	}
	tr := tar.NewReader(zr)
	got := map[string]ManifestFile{}
	haveManifest := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, fmt.Errorf("damaged export: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			return m, fmt.Errorf("damaged export: %s is not a regular file", h.Name)
		}
		switch h.Name {
		case memberManifest:
			if h.Size > 1<<20 {
				return m, errors.New("damaged export: manifest too large")
			}
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return m, fmt.Errorf("damaged export: %w", err)
			}
			if err := json.Unmarshal(b, &m); err != nil {
				return m, fmt.Errorf("damaged export: manifest: %w", err)
			}
			haveManifest = true
		case memberDB, memberPepper, memberHubKey, memberPullKey, memberPullPub:
			if !haveManifest {
				return m, errors.New("damaged export: manifest.json must come first")
			}
			if _, dup := got[h.Name]; dup {
				return m, fmt.Errorf("damaged export: %s twice", h.Name)
			}
			// Write no more than the manifest declares for this member, so a hostile archive
			// cannot fill the disk through a compression bomb; the key files are small.
			want := int64(-1)
			for _, mf := range m.Files {
				if mf.Path == h.Name {
					want = mf.Size
				}
			}
			if want < 0 {
				return m, fmt.Errorf("damaged export: %s is not in the manifest", h.Name)
			}
			if h.Name != memberDB && want > maxStateFileBytes {
				return m, fmt.Errorf("damaged export: %s is too large", h.Name)
			}
			dst := filepath.Join(dir, filepath.FromSlash(h.Name))
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return m, err
			}
			out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return m, err
			}
			hh := sha256.New()
			n, cerr := io.Copy(io.MultiWriter(out, hh), io.LimitReader(tr, want+1))
			if e := out.Close(); cerr == nil {
				cerr = e
			}
			if cerr != nil {
				return m, fmt.Errorf("damaged export: %s: %w", h.Name, cerr)
			}
			got[h.Name] = ManifestFile{Path: h.Name, Size: n, SHA256: hex.EncodeToString(hh.Sum(nil))}
		default:
			return m, fmt.Errorf("damaged export: unexpected member %q", h.Name)
		}
	}
	// reading to EOF of the tar is not enough: the gzip trailer (and the last encrypted
	// chunk) must be read too, or a truncated file could pass.
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return m, fmt.Errorf("damaged export: %w", err)
	}
	if _, err := io.Copy(io.Discard, src); err != nil {
		return m, fmt.Errorf("damaged export: %w", err)
	}
	if !haveManifest {
		return m, errors.New("not a hub export: no manifest.json")
	}
	// An export made before a rename carries the old name (rename.md 2.6).
	if m.Format != ManifestFormat || (m.Kind != ManifestKind && m.Kind != brand.Legacy(ManifestKind)) || m.Kind == "" {
		return m, fmt.Errorf("not a hub export this sy reads (format %d, kind %q)", m.Format, m.Kind)
	}
	listed := map[string]bool{}
	for _, mf := range m.Files {
		listed[mf.Path] = true
		g, ok := got[mf.Path]
		if !ok {
			return m, fmt.Errorf("damaged export: %s is listed but missing", mf.Path)
		}
		if g.Size != mf.Size || g.SHA256 != mf.SHA256 {
			return m, fmt.Errorf("damaged export: %s does not match the manifest", mf.Path)
		}
	}
	for name := range got {
		if !listed[name] {
			return m, fmt.Errorf("damaged export: %s is not in the manifest", name)
		}
	}
	if !listed[memberDB] {
		return m, errors.New("damaged export: no hub.db")
	}
	return m, nil
}

// ---- import ----

// ErrHubRunning means the database is in use.
var ErrHubRunning = errors.New("a hub is running on this data folder (the database is locked): stop the hub first")

// NonEmptyError is the refusal of machines.md 8.2 step 3.
type NonEmptyError struct {
	Path   string
	Counts map[string]int64
}

func (e *NonEmptyError) Error() string {
	var parts []string
	for _, t := range importGuardTables {
		if n := e.Counts[t]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, t))
		}
	}
	return fmt.Sprintf("this hub already has data (%s); move %s away first", strings.Join(parts, ", "), e.Path)
}

var importGuardTables = []string{"device", "record", "machine", "push_target", "pull_target"}

// ReadSchemaVersion reads schema_version from a database file without opening it through the
// store (so nothing is migrated or created). 0 when the file does not exist or is empty.
func ReadSchemaVersion(ctx context.Context, path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, nil
	}
	db, err := openRO(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return currentVersion(ctx, db)
}

// existingCounts counts the guarded tables of an existing database; a missing table counts 0.
func existingCounts(ctx context.Context, path string) (map[string]int64, error) {
	db, err := openRO(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]int64{}
	for _, t := range importGuardTables {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, t).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		if out[t], err = tableCount(ctx, db, t); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// lockedCheck fails with ErrHubRunning when another process holds the database.
func lockedCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", FileURI(path, "_pragma=busy_timeout(500)&_txlock=exclusive"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "busy") {
			return ErrHubRunning
		}
		return err
	}
	return tx.Rollback()
}

// ImportOptions configures Import.
type ImportOptions struct {
	File       string
	Passphrase string
	// DBPath is <data>/hub.db; StateDir is where pair_pepper lives and PullDir (default
	// <StateDir>/pull) where the pull key lives.
	DBPath, StateDir, PullDir string
	// PublicURL replaces the exported hub.public_url when not empty.
	PublicURL string
	SyVersion string
	Log       func(format string, args ...any)
	// Now is the clock (tests).
	Now func() time.Time
}

// ImportResult says what was done.
type ImportResult struct {
	Manifest  Manifest
	PublicURL string
	// URLChanged is true when PublicURL replaced the exported one.
	URLChanged bool
	// Backups lists the *.pre-import-<time> files made of existing state files.
	Backups []string
}

// Import unpacks an export into an empty hub (machines.md 8.2). There is no force.
func Import(ctx context.Context, o ImportOptions) (res ImportResult, err error) {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	dataDir := filepath.Dir(o.DBPath)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return res, err
	}
	// 1. nothing may be using the data folder: a running `sy hub serve` holds hub.lock, and
	// an existing database must not be locked by any other process.
	if held, herr := hublock.Held(dataDir); herr != nil {
		return res, herr
	} else if held {
		return res, ErrHubRunning
	}
	existing := false
	if _, serr := os.Stat(o.DBPath); serr == nil {
		existing = true
		if err := lockedCheck(ctx, o.DBPath); err != nil {
			return res, err
		}
	}
	// 2. read, decrypt, verify into a private folder next to the database (same filesystem).
	sweepStale(dataDir, ".import-")
	work, err := os.MkdirTemp(dataDir, ".import-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(work)
	m, err := unpack(o.File, o.Passphrase, work)
	if err != nil {
		return res, err
	}
	res.Manifest = m
	if max := LatestVersion(); m.Schema > max {
		return res, fmt.Errorf("the export has schema v%d, this sy opens up to v%d: update sy here first", m.Schema, max)
	}
	newDB := filepath.Join(work, memberDB)
	if err := checkIntegrity(ctx, newDB); err != nil {
		return res, fmt.Errorf("the exported database is damaged: %w", err)
	}
	if v, err := ReadSchemaVersion(ctx, newDB); err != nil || v != m.Schema {
		return res, fmt.Errorf("the exported database is schema v%d, its manifest says v%d", v, m.Schema)
	}
	// The archive's database is untrusted input: before anything opens it for writing (a
	// migration would fire a trigger an attacker put there), refuse anything a Switchyard
	// database never contains.
	if err := refuseCodeInSchema(ctx, newDB); err != nil {
		return res, err
	}
	// 3. refuse a non-empty hub.
	if existing {
		counts, err := existingCounts(ctx, o.DBPath)
		if err != nil {
			return res, fmt.Errorf("cannot read the existing hub database %s: %w", o.DBPath, err)
		}
		for _, n := range counts {
			if n > 0 {
				return res, &NonEmptyError{Path: o.DBPath, Counts: counts}
			}
		}
	}
	// 4. migrate and unfreeze the copy in the work folder, so everything that can fail on
	// the data has failed before any file of the hub is touched.
	if err := prepareImported(ctx, newDB, o, m, now, logf, &res); err != nil {
		return res, err
	}
	if err := os.Chmod(newDB, 0o600); err != nil {
		return res, err
	}
	// 5. swap: the database first, the state files last; any failure puts everything back.
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		res.Backups = nil
	}
	defer func() {
		if err != nil {
			rollback()
		}
	}()
	stamp := now().UTC().Format("20060102T150405Z")
	// The existing (empty) database and its sidecars move aside until the swap is done.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		cur := o.DBPath + suffix
		if _, serr := os.Stat(cur); serr != nil {
			continue
		}
		bak := cur + ".pre-import-" + stamp
		if err = os.Rename(cur, bak); err != nil {
			return res, err
		}
		undo = append(undo, func() { _ = os.Rename(bak, cur) })
	}
	if err = os.Rename(newDB, o.DBPath); err != nil {
		return res, err
	}
	undo = append(undo, func() { _ = os.Rename(o.DBPath, newDB) })
	var emptyBackups []string
	for _, suffix := range []string{"", "-wal", "-shm"} {
		emptyBackups = append(emptyBackups, o.DBPath+suffix+".pre-import-"+stamp)
	}
	for _, st := range stateFiles(o.StateDir, o.PullDir) {
		src := filepath.Join(work, filepath.FromSlash(st.member))
		b, rerr := os.ReadFile(src)
		if errors.Is(rerr, os.ErrNotExist) {
			continue
		}
		if rerr != nil {
			err = rerr
			return res, err
		}
		dst := st.path
		if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return res, err
		}
		if _, serr := os.Stat(dst); serr == nil {
			bak := dst + ".pre-import-" + stamp
			if err = os.Rename(dst, bak); err != nil {
				return res, err
			}
			res.Backups = append(res.Backups, bak)
			undo = append(undo, func() { _ = os.Rename(bak, dst) })
		}
		tmp := dst + ".tmp-import"
		if err = os.WriteFile(tmp, b, 0o600); err != nil {
			return res, err
		}
		if err = os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return res, err
		}
		undo = append(undo, func() { _ = os.Remove(dst) })
	}
	// Done: the empty database's own backups carry nothing worth keeping.
	for _, f := range emptyBackups {
		_ = os.Remove(f)
	}
	return res, nil
}

// prepareImported opens the unpacked database (which migrates it to this sy's schema), clears
// hub.frozen, applies the new public URL and records the import, then closes it so the file
// is complete without a -wal.
func prepareImported(ctx context.Context, path string, o ImportOptions, m Manifest, now func() time.Time, logf func(string, ...any), res *ImportResult) (err error) {
	st, err := Open(ctx, path, Options{Log: logf, Now: now})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); err == nil {
			err = cerr
		}
		_ = os.Remove(path + "-wal")
		_ = os.Remove(path + "-shm")
	}()
	res.PublicURL = m.PublicURL
	// Migrated: the archive's tables and indexes must now be exactly this version's.
	if err := checkSchemaObjects(ctx, path, filepath.Dir(path)); err != nil {
		return err
	}
	return st.Write(ctx, func(tx *Tx) error {
		if _, err := tx.exec(`DELETE FROM setting WHERE key = ?`, SettingFrozen); err != nil {
			return err
		}
		if err := dropUnknownSettings(tx, logf); err != nil {
			return err
		}
		if o.PublicURL != "" {
			v, _ := json.Marshal(o.PublicURL)
			if err := tx.PutAlertSetting(AlertKeyPublicURL, v, tx.NowMS()); err != nil {
				return err
			}
			res.PublicURL, res.URLChanged = o.PublicURL, o.PublicURL != m.PublicURL
		}
		imp, _ := json.Marshal(map[string]string{"exported_at": m.ExportedAt,
			"imported_at": now().UTC().Format(time.RFC3339), "sy": o.SyVersion})
		return tx.PutAlertSetting(SettingImported, imp, tx.NowMS())
	})
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// refuseCodeInSchema rejects an archive database that holds a trigger, a view or a virtual
// table. No Switchyard schema version has any, so one can only have been added to run SQL (or
// load a module) when the database is opened or written. It reads sqlite_schema only.
func refuseCodeInSchema(ctx context.Context, path string) error {
	db, err := openRO(path)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT type, name, coalesce(sql, '') FROM sqlite_schema
		WHERE type IN ('trigger', 'view') OR upper(sql) LIKE 'CREATE VIRTUAL TABLE%'`)
	if err != nil {
		return fmt.Errorf("reading the exported database's schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name, sqlText string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			return err
		}
		if typ == "table" {
			typ = "virtual table"
		}
		return fmt.Errorf("the exported database holds a %s %q, which a Switchyard database never has; refusing to import it", typ, name)
	}
	return rows.Err()
}

// schemaObjects lists the (type, name) of every schema object except SQLite's own.
func schemaObjects(ctx context.Context, path string) (map[string]bool, error) {
	db, err := openRO(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT type, name FROM sqlite_schema WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			return nil, err
		}
		out[typ+" "+name] = true
	}
	return out, rows.Err()
}

// checkSchemaObjects compares a migrated import with a freshly made database of this sy: an
// object the fresh one does not have (a table, an index, anything) is refused. The reference
// is made in dir and removed again.
func checkSchemaObjects(ctx context.Context, path, dir string) error {
	ref := filepath.Join(dir, ".schema-ref.db")
	for _, sfx := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(ref + sfx)
	}
	defer func() {
		for _, sfx := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(ref + sfx)
		}
	}()
	rs, err := Open(ctx, ref, Options{NoBackupBeforeMigrate: true})
	if err != nil {
		return err
	}
	if err := rs.Close(); err != nil {
		return err
	}
	want, err := schemaObjects(ctx, ref)
	if err != nil {
		return err
	}
	got, err := schemaObjects(ctx, path)
	if err != nil {
		return err
	}
	var extra []string
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("the exported database has objects a Switchyard database does not (%s); refusing to import it", strings.Join(extra, ", "))
	}
	return nil
}

// importableSettings are the setting keys this hub writes; an archive's other keys are dropped.
var importableSettings = map[string]bool{
	SettingImported: true, AlertKeyPublicURL: true, AlertKeyHubTZ: true, AlertKeyPushSettings: true,
	AlertKeyBaselineDone: true, "push.vapid_public": true, "pair.ttl_s": true,
	"app.android_mirror": true, "app.android_repo": true,
}

// dropUnknownSettings deletes setting rows whose key this hub never writes (and logs them),
// so an archive cannot plant a setting some later code reads.
func dropUnknownSettings(tx *Tx, logf func(string, ...any)) error {
	rows, err := tx.QueryContext(tx.ctx, `SELECT key FROM setting`)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		if !importableSettings[k] {
			drop = append(drop, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range drop {
		logf("import: ignoring setting %q, which this hub does not use", k)
		if _, err := tx.exec(`DELETE FROM setting WHERE key = ?`, k); err != nil {
			return err
		}
	}
	return nil
}
