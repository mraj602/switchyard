package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `wy hook --help` prints usage and exits 0 without touching state: it is not a payload, so
// nothing is logged to hook-errors.log.
func TestHookHelpTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WY_HOME", filepath.Join(home, "wy"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("WY_NO_SPAWN", "1")
	for _, a := range []string{"--help", "-h", "help"} {
		var out, errb bytes.Buffer
		if code := dispatch([]string{"hook", a}, &out, &errb); code != 0 {
			t.Fatalf("hook %s: exit %d", a, code)
		}
		if !strings.Contains(out.String(), "usage: wy hook") || errb.Len() != 0 {
			t.Errorf("hook %s: stdout %q stderr %q", a, out.String(), errb.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, "wy")); err == nil {
		t.Error("wy hook --help created state")
	}
}
