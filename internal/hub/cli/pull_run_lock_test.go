package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/hublock"
	"github.com/mraj602/watchyard/internal/hub/pull"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// `wy hub pull run` with no name leaves the runs to a live hub (it only
// schedules them) and runs in-process when no hub is alive.
func TestPullRunWithoutNameAsksALiveHub(t *testing.T) {
	f := newPullFixture(t)
	if _, errs, err := f.run(cliKey(1)+"\n", "add", "gpu-box", "--ssh", "me@gpu-box.ts.net", "--machine", cliMachine, "--host-keys-stdin"); err != nil {
		t.Fatal(err, errs)
	}
	started := 0
	pullStarter = func(ctx context.Context, argv []string) (pull.Proc, error) {
		started++
		return nil, errors.New("no ssh in tests")
	}
	// A live scheduler touches scheduler.alive: the CLI must not run the target itself.
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, pull.AliveFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errs, err := f.run("", "run"); err != nil {
		t.Fatal(err, errs)
	}
	if started != 0 {
		t.Fatalf("ran %d session(s) in-process although a hub is alive", started)
	}
	// No hub: it runs the due target itself.
	if err := os.Remove(filepath.Join(f.dir, pull.AliveFile)); err != nil {
		t.Fatal(err)
	}
	if _, errs, err := f.run("", "run"); err != nil {
		t.Fatal(err, errs)
	}
	if started != 1 {
		t.Fatalf("in-process runs: %d, want 1", started)
	}
}

// A `wy hub serve` on the data folder (hub.lock) stops a second serve and
// an import, whatever address it listens on.
func TestHubLockStopsSecondServeAndImport(t *testing.T) {
	t.Setenv("WY_STATE_DIR", t.TempDir())
	data := t.TempDir()
	db := filepath.Join(data, "hub.db")
	l, err := hublock.Acquire(data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err := run(t, "serve", "--db", db, "--listen", "127.0.0.1:0"); !errors.Is(err, hublock.ErrHeld) {
		t.Fatalf("second serve: %v", err)
	}
	f := filepath.Join(t.TempDir(), "x.wyx")
	os.WriteFile(f, []byte("x"), 0o600)
	_, err = run(t, "import", f, "--db", db, "--yes", "--listen", "127.0.0.1:1") // not the real hub's 8787
	var re *RefusedError
	if !errors.As(err, &re) || !errors.Is(err, store.ErrHubRunning) {
		t.Fatalf("import with the lock held: %v", err)
	}
}
