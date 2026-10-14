package termctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/termenv"
)

// With `-tags fakeopen` SY_TEST_OPENER records the argv and runs nothing; in a release
// build the variable has no effect (the seam is not compiled in).
func TestFakeOpenSeam(t *testing.T) {
	file := filepath.Join(t.TempDir(), "opened")
	env := termenv.Fake(t.TempDir(), "linux", map[string]string{"SY_TEST_OPENER": file})
	u := "warp://session/" + strings.Repeat("0", 32)
	o := NewOpener(env, nil)
	_, isExec := o.(ExecOpener)
	if fakeOpenBuilt {
		if isExec {
			t.Fatal("fakeopen build returned the real opener")
		}
		if err := o.Open(context.Background(), u); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(file)
		if string(b) != `["xdg-open","`+u+`"]`+"\n" {
			t.Errorf("recorded %q", b)
		}
		return
	}
	if !isExec {
		t.Fatal("release build has a test seam")
	}
}
