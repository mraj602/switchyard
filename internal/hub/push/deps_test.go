package push

import (
	"os/exec"
	"strings"
	"testing"
)

const mod = "github.com/mraj602/watchyard/"

// deps lists the module packages pkg depends on, directly or not (go list -deps), test
// files excluded.
func deps(t *testing.T, pkg string) []string {
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

// TestDependencyRule enforces ARCHITECTURE.md: the push packages reach the rest of the hub
// only through hub/store, hub/events, hub/ingest's change set and hub/push/model; they never
// import hub/api (which imports them), hub/cli or the web app. model is a leaf, rules is pure.
func TestDependencyRule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", mod+"internal/hub/push/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	pkgs := strings.Fields(string(out))
	if len(pkgs) < 8 {
		t.Fatalf("only %d push packages listed: %v", len(pkgs), pkgs)
	}
	forbidden := []string{"internal/hub/api", "internal/hub/cli", "internal/hub/pair", "webui", "cmd/"}
	for _, full := range pkgs {
		pkg := strings.TrimPrefix(full, mod)
		ds := deps(t, pkg)
		for _, d := range ds {
			for _, f := range forbidden {
				if d == f || strings.HasPrefix(d, f+"/") || (strings.HasSuffix(f, "/") && strings.HasPrefix(d, f)) {
					t.Errorf("%s depends on %s", pkg, d)
				}
			}
		}
		switch pkg {
		case "internal/hub/push/model", "internal/hub/push/clock":
			if len(ds) > 0 {
				t.Errorf("%s must be a leaf, imports %v", pkg, ds)
			}
		case "internal/hub/push/rules":
			for _, d := range ds {
				if d != "internal/hub/push/model" {
					t.Errorf("rules must stay pure (only push/model), imports %s", d)
				}
			}
		}
	}
}
