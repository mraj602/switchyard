package board

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The board only reads (docs/spec/terminal.md T1): no code under
// internal/board/** calls /v1/ingest. Two checks: no non-test source mentions the ingest
// endpoint or calls an Ingest method, and the packages do not depend on the code that can
// post records (internal/client has Client.Ingest; internal/hub/ingest is the hub's side).

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestNoIngestInBoardSources(t *testing.T) {
	root := repoRoot(t)
	var files int
	err := filepath.WalkDir(filepath.Join(root, "internal", "board"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0) // comments are not parsed
		if err != nil {
			return err
		}
		files++
		rel, _ := filepath.Rel(root, p)
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind == token.STRING && (strings.Contains(x.Value, "/v1/ingest") || strings.Contains(x.Value, "X-Wy-Backlog")) {
					t.Errorf("%s uses %s: the board never posts records", rel, x.Value)
				}
			case *ast.SelectorExpr:
				if x.Sel.Name == "Ingest" {
					t.Errorf("%s calls .Ingest: the board never posts records", rel)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no board sources found")
	}
}

func TestBoardDoesNotDependOnIngestCode(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool")
	}
	cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", "./internal/board/...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list failed: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range []string{"/internal/client", "/internal/hub/ingest", "/internal/directpush", "/internal/hub/api"} {
			if strings.HasSuffix(dep, bad) {
				t.Errorf("internal/board/** depends on %s", dep)
			}
		}
	}
}
