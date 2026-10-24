package pull

import (
	"os/exec"
	"strings"
	"testing"
)

const mod = "github.com/mraj602/watchyard/"

// moduleDeps lists the module packages pkg depends on, directly or not (go list -deps),
// test files excluded.
func moduleDeps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", mod+pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", pkg, err, out)
	}
	var mine []string
	for _, p := range strings.Fields(string(out)) {
		if p != mod+pkg && strings.HasPrefix(p, mod) {
			mine = append(mine, strings.TrimPrefix(p, mod))
		}
	}
	return mine
}

// TestPullDependencyRules enforces ARCHITECTURE.md: drainproto is a leaf both sides import;
// directpush reaches the hub side only through the pure hub/state fold; hub/pull feeds records
// through the ingester, never the HTTP side (it imports hub/api for the Puller and
// PullStatus types only, never hub/cli); the client imports nothing of the hub.
func TestPullDependencyRules(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	if ds := moduleDeps(t, "internal/drainproto"); len(ds) > 0 {
		t.Errorf("drainproto must be a leaf, imports %v", ds)
	}
	for _, d := range moduleDeps(t, "internal/directpush") {
		if strings.HasPrefix(d, "internal/hub/") && d != "internal/hub/state" {
			t.Errorf("directpush depends on %s (only hub/state is allowed)", d)
		}
	}
	pd := moduleDeps(t, "internal/hub/pull")
	has := map[string]bool{}
	for _, d := range pd {
		has[d] = true
		if d == "internal/hub/cli" || strings.HasPrefix(d, "cmd/") {
			t.Errorf("hub/pull depends on %s", d)
		}
	}
	for _, need := range []string{"internal/drainproto", "internal/hub/ingest"} {
		if !has[need] {
			t.Errorf("hub/pull does not use %s", need)
		}
	}
	for _, d := range moduleDeps(t, "internal/client") {
		if strings.HasPrefix(d, "internal/hub/") {
			t.Errorf("client depends on %s", d)
		}
	}
}
