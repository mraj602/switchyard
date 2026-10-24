package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/brand"
)

// rename.md 2.7: after a rename, an archive attested by the repository under its old name
// (a release from before the rename) still verifies; the current name is tried first.
func TestAttestationAcceptsLegacyRepo(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	var tried []string
	run := func(_ context.Context, _ string, args ...string) error {
		repo := args[4]
		tried = append(tried, repo)
		if repo == "mraj602/oldname" {
			return nil
		}
		return errors.New("wrong repository")
	}
	ok, err := VerifyAttestation(context.Background(), Attestation{Required: true, GH: "gh", Run: run}, "a")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got := strings.Join(tried, ","); got != "mraj602/"+brand.Slug+",mraj602/oldname" {
		t.Errorf("tried %s", got)
	}
	never := func(context.Context, string, ...string) error { return errors.New("no attestation") }
	if _, err := VerifyAttestation(context.Background(), Attestation{Required: true, GH: "gh", Run: never}, "a"); code(err) != ExitVerify {
		t.Fatalf("%v", err)
	}
}

func TestMoveLegacyBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	old := filepath.Join(dir, "oldcli")
	if err := os.WriteFile(old, []byte("new build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if did, err := MoveLegacyBinary(old, "linux"); did != "" || err != nil {
		t.Fatalf("not renamed: %q %v", did, err)
	}
	defer brand.SetLegacyCLIForTest("oldcli")()
	did, err := MoveLegacyBinary(old, "linux")
	if err != nil || !strings.Contains(did, "renamed oldcli to "+brand.CLI) {
		t.Fatalf("%q %v", did, err)
	}
	if l, err := os.Readlink(old); err != nil || l != brand.CLI {
		t.Fatalf("link %q %v", l, err)
	}
	if b, _ := os.ReadFile(old); string(b) != "new build" {
		t.Fatal("old name does not run the new build")
	}
	// Running under the current name: an old regular binary becomes a link; a link stays.
	cur := filepath.Join(dir, brand.CLI)
	_ = os.Remove(old)
	_ = os.WriteFile(old, []byte("stale"), 0o755)
	if did, err := MoveLegacyBinary(cur, "linux"); err != nil || !strings.Contains(did, "now links") {
		t.Fatalf("%q %v", did, err)
	}
	if did, err := MoveLegacyBinary(cur, "linux"); did != "" || err != nil {
		t.Fatalf("again: %q %v", did, err)
	}
	// Windows: a copy.
	_ = os.Remove(old)
	_ = os.WriteFile(filepath.Join(dir, "oldcli.exe"), []byte("stale"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, brand.CLI+".exe"), []byte("new build"), 0o755)
	if did, err := MoveLegacyBinary(filepath.Join(dir, brand.CLI+".exe"), "windows"); err != nil || !strings.Contains(did, "copy") {
		t.Fatalf("windows: %q %v", did, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "oldcli.exe")); string(b) != "new build" {
		t.Fatal("windows copy")
	}
}

type oldOnlyRemote struct{ runs [][]string }

func (r *oldOnlyRemote) Run(_ context.Context, _ FleetMachine, argv []string) (string, int, error) {
	r.runs = append(r.runs, append([]string(nil), argv...))
	if argv[0] != ".local/bin/oldcli" {
		return "", 127, nil
	}
	return `{"status":"updated","from":"1.0.0","to":"1.1.0"}`, 0, nil
}
func (r *oldOnlyRemote) Platform(context.Context, FleetMachine) (string, string, error) {
	return "linux", "amd64", nil
}
func (r *oldOnlyRemote) Push(context.Context, FleetMachine, string, string) (string, error) {
	return "", errors.New("no push")
}

// rename.md 2.13: a machine that only has the old command updates itself through it.
func TestFleetFallsBackToLegacyBinary(t *testing.T) {
	defer brand.SetLegacyCLIForTest("oldcli")()
	r := &oldOnlyRemote{}
	res, failed := UpdateFleet(context.Background(), FleetOptions{Machines: []FleetMachine{{Name: "w"}}, Version: "1.1.0", Remote: r})
	if failed || res[0].Outcome != "updated" || len(r.runs) != 2 || r.runs[0][0] != ".local/bin/"+brand.CLI {
		t.Fatalf("%+v %v", res, r.runs)
	}
}
