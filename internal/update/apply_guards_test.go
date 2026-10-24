package update

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackRefusedWhenOldBinaryCannotOpenHub(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, true)
	if _, err := Apply(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.Current = "0.0.1-e2e"
	o.HubSchema = 5 // a migration since the update; wy.old opens up to v4
	before := read(t, exe)
	_, err := Rollback(context.Background(), o)
	if code(err) != ExitRefused || !strings.Contains(err.Error(), "cannot open the hub database (v5)") {
		t.Fatalf("got %v", err)
	}
	if !bytes.Equal(read(t, exe), before) {
		t.Fatal("rollback changed the binary")
	}
	o.HubSchema = 4
	if _, err := Rollback(context.Background(), o); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadableHubDBRefuses(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, true)
	o.HubDBErr = "/srv/wy/hub.db: file is not a database"
	before := read(t, exe)
	if _, err := Apply(context.Background(), o); code(err) != ExitRefused {
		t.Fatalf("apply: %v", err)
	}
	os.WriteFile(exe+".old", fakeBin("0.0.0-e2e", 4), 0o755)
	if _, err := Rollback(context.Background(), o); code(err) != ExitRefused {
		t.Fatalf("rollback: %v", err)
	}
	if !bytes.Equal(read(t, exe), before) {
		t.Fatal("changed")
	}
}

func TestUpdateLockBlocksSecondRun(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, true)
	unlock, err := lockUpdate(o.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	before := read(t, exe)
	if _, err := Apply(context.Background(), o); code(err) != ExitFailed || !strings.Contains(err.Error(), "another wy update") {
		t.Fatalf("got %v", err)
	}
	if _, err := Rollback(context.Background(), o); code(err) != ExitFailed {
		t.Fatalf("rollback: %v", err)
	}
	unlock()
	if _, err := Apply(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(read(t, exe), before) {
		t.Fatal("not updated")
	}
	if _, err := os.Stat(filepath.Join(o.StateDir, "update.lock")); err == nil {
		t.Error("lock left behind")
	}
}

func TestFromFileArchiveInUpdateDirSurvives(t *testing.T) {
	o, _, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, false)
	name := "wy_0.0.1-e2e_linux_amd64.tar.gz"
	dir := filepath.Join(t.TempDir(), "update") // the user's own folder, named update
	os.MkdirAll(dir, 0o755)
	arch := r["/download/v0.0.1-e2e/"+name]
	f := filepath.Join(dir, name)
	os.WriteFile(f, arch, 0o644)
	sums := filepath.Join(dir, "SHA256SUMS")
	os.WriteFile(sums, r["/download/v0.0.1-e2e/SHA256SUMS"], 0o644)
	o.FromFile, o.SumsFile = f, sums
	if _, err := Apply(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("the user's archive was deleted: %v", err)
	}
}

func TestLatestDownloadsByTagURL(t *testing.T) {
	o, _, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, true)
	rel, err := Resolve(context.Background(), o.Source, Latest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rel.SumsURL, "/download/v0.0.1-e2e/") {
		t.Errorf("SumsURL %s is the moving alias", rel.SumsURL)
	}
	// The alias moves between the two requests: refuse rather than mix releases.
	r["/latest/download/SHA256SUMS"] = append([]byte("# moved\n"), r["/latest/download/SHA256SUMS"]...)
	if _, err := Resolve(context.Background(), o.Source, Latest); code(err) != ExitDownload {
		t.Fatalf("got %v", err)
	}
}

func TestScoopIsPackageManaged(t *testing.T) {
	for _, p := range []string{`C:\Users\a\scoop\apps\wy\current\wy.exe`, `C:\Users\a\scoop\shims\wy.exe`} {
		if _, owned := PackageManager(p); !owned {
			t.Errorf("%s should be package-managed", p)
		}
	}
}

func TestPostUpdateStepPanicDoesNotStopOthers(t *testing.T) {
	postSteps = nil
	defer func() { postSteps = nil }()
	RegisterPostUpdate(PostStep{Name: "a", Run: func(context.Context, string) (string, error) { panic("boom") }})
	RegisterPostUpdate(PostStep{Name: "b", Run: func(context.Context, string) (string, error) { return "ok", nil }})
	rs := RunPostUpdate(context.Background(), "0")
	if len(rs) != 2 || rs[0].Error == "" || rs[1].Detail != "ok" {
		t.Fatalf("%+v", rs)
	}
}
