package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `sy hook --help` prints usage and exits 0 without touching state: it is not a payload, so
// nothing is logged to hook-errors.log.
func TestHookHelpTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SY_HOME", filepath.Join(home, "sy"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("SY_NO_SPAWN", "1")
	for _, a := range []string{"--help", "-h", "help"} {
		var out, errb bytes.Buffer
		if code := dispatch([]string{"hook", a}, &out, &errb); code != 0 {
			t.Fatalf("hook %s: exit %d", a, code)
		}
		if !strings.Contains(out.String(), "usage: sy hook") || errb.Len() != 0 {
			t.Errorf("hook %s: stdout %q stderr %q", a, out.String(), errb.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, "sy")); err == nil {
		t.Error("sy hook --help created state")
	}
}
