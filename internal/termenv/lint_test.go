package termenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The terminal packages take their folders and environment from an injected Env
// (docs/spec/terminal.md T12). This test refuses, in every .go file under these trees
// (tests included, testdata skipped), any direct read of the person's home or of the
// process environment, outside termenv/env.go:
//
//   - os.UserHomeDir, os.UserConfigDir, os.UserCacheDir, os.ExpandEnv, os.Environ;
//   - os.Getenv / os.LookupEnv with a home-like name (HOME, USERPROFILE, APPDATA,
//     LOCALAPPDATA, XDG_*, SY_HOME, CLAUDE_CONFIG_DIR) or with a name that is not a
//     string literal (it could be any of them);
//   - importing os/user.
//
// Other variables (TERM_PROGRAM, NO_COLOR, ...) come through Env.Lookup too; only the
// names above are hard failures, because they decide which real files a test touches.
var lintedTrees = []string{"internal/board", "internal/termctl", "internal/warp", "internal/termenv"}

// allowedFile is the one file that may read the process environment.
const allowedFile = "internal/termenv/env.go"

var homeVars = map[string]bool{
	"HOME": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true, "SY_HOME": true,
	"CLAUDE_CONFIG_DIR": true, "HOMEDRIVE": true, "HOMEPATH": true,
}

var forbiddenOS = map[string]bool{
	"UserHomeDir": true, "UserConfigDir": true, "UserCacheDir": true, "ExpandEnv": true, "Environ": true,
}

// repoRoot walks up from this package to the folder holding go.mod.
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

// homeAccess returns one "file:line: what" per forbidden use in src.
func homeAccess(fset *token.FileSet, name string, src any) ([]string, error) {
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	osNames := map[string]bool{}
	var out []string
	report := func(n ast.Node, what string) {
		out = append(out, fset.Position(n.Pos()).String()+": "+what)
	}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		switch p {
		case "os":
			n := "os"
			if imp.Name != nil {
				n = imp.Name.Name
			}
			osNames[n] = true
		case "os/user":
			report(imp, "imports os/user")
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || !osNames[id.Name] {
			return true
		}
		if forbiddenOS[sel.Sel.Name] {
			report(sel, "os."+sel.Sel.Name)
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || !osNames[id.Name] || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") || len(call.Args) != 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			report(call, "os."+sel.Sel.Name+" with a non-literal name")
			return true
		}
		v, _ := strconv.Unquote(lit.Value)
		if homeVars[v] || strings.HasPrefix(v, "XDG_") {
			report(call, "os."+sel.Sel.Name+"("+lit.Value+")")
		}
		return true
	})
	return out, nil
}

func TestTerminalPackagesNeverReadTheRealHome(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var bad []string
	scanned := 0
	for _, tree := range lintedTrees {
		dir := filepath.Join(root, filepath.FromSlash(tree))
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue // a work package has not created it yet
		}
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if rel == allowedFile || rel == "internal/termenv/lint_test.go" {
				return nil
			}
			scanned++
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			found, err := homeAccess(fset, rel, src)
			if err != nil {
				return err
			}
			bad = append(bad, found...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned == 0 {
		t.Fatal("no files scanned: the tree list or the repo root is wrong")
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s (take it from termenv.Env instead)", b)
	}
}

// The checker itself: each forbidden form is caught, including through an import alias,
// and the allowed forms are not.
func TestHomeAccessChecker(t *testing.T) {
	src := `package x

import (
	stdos "os"
	"os/user"
)

var k = "HOME"

func f() {
	_, _ = stdos.UserHomeDir()
	_ = stdos.Getenv("HOME")
	_, _ = stdos.LookupEnv("XDG_DATA_HOME")
	_ = stdos.Getenv(k)
	_ = stdos.ExpandEnv("$HOME/x")
	_ = stdos.Getenv("USERPROFILE")
	_, _ = user.Current()
	_ = stdos.Getenv("TERM_PROGRAM") // allowed: not a home variable
	_ = stdos.TempDir()              // allowed
}
`
	got, err := homeAccess(token.NewFileSet(), "x.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"imports os/user", "os.UserHomeDir", `os.Getenv("HOME")`, `os.LookupEnv("XDG_DATA_HOME")`,
		"os.Getenv with a non-literal name", "os.ExpandEnv", `os.Getenv("USERPROFILE")`}
	if len(got) != len(want) {
		t.Fatalf("got %d findings, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if strings.HasSuffix(g, ": "+w) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in:\n%s", w, strings.Join(got, "\n"))
		}
	}
}
