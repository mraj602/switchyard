package drainproto

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// drainproto is a leaf both sides import (ARCHITECTURE.md): no import of
// anything under internal/ or the module, test files included.
func TestPackageIsALeaf(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no source files", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); strings.Contains(p, "switchyard") || strings.Contains(p, "/internal/") {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}
