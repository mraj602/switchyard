package store

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A panic inside a write must roll the transaction back and release the one writer
// connection, or every later write would block forever.
func TestWritePanicRollsBack(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	func() {
		defer func() { _ = recover() }()
		_ = s.Write(ctx, func(tx *Tx) error {
			if _, err := tx.exec(`INSERT INTO setting (key, value, updated_at) VALUES ('k', '1', 1)`); err != nil {
				t.Error(err)
			}
			panic("boom")
		})
	}()
	done := make(chan error, 1)
	go func() {
		done <- s.Write(ctx, func(tx *Tx) error {
			var n int
			if err := tx.queryRow(`SELECT count(*) FROM setting WHERE key = 'k'`).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Errorf("the panicking write left its row behind")
			}
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the writer is still held after a panic")
	}
}

// The database file (and so its -wal and -shm) is private to the owner, and so is a backup.
func TestDatabaseFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	dir := t.TempDir()
	s := openTest(t, dir)
	bak := filepath.Join(dir, "backups", "copy.db")
	if err := s.Backup(context.Background(), bak); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.Path(), s.Path() + "-wal", bak} {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v", p, st.Mode().Perm())
		}
	}
	if err := s.Backup(context.Background(), bak); err == nil {
		t.Error("a backup overwrote an existing file")
	}
}

// An archive member that holds more than its manifest entry declares is refused before it
// can fill the disk.
func TestUnpackRefusesMemberLargerThanManifest(t *testing.T) {
	m := Manifest{Format: ManifestFormat, Kind: ManifestKind, Schema: 1,
		Files: []ManifestFile{{Path: memberDB, Size: 10, SHA256: "x"}}}
	mb, _ := json.Marshal(m)
	path := filepath.Join(t.TempDir(), "bomb.syx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	put := func(name string, b []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	put(memberManifest, mb)
	put(memberDB, []byte(strings.Repeat("z", 1<<20)))
	tw.Close()
	zw.Close()
	f.Close()

	dir := t.TempDir()
	if _, err := unpack(path, "", dir); err == nil {
		t.Fatal("an oversized member was accepted")
	}
	if st, err := os.Stat(filepath.Join(dir, memberDB)); err == nil && st.Size() > 11 {
		t.Errorf("wrote %d bytes for a member declared as 10", st.Size())
	}
}
