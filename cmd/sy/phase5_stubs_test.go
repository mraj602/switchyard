package main

import (
	"bytes"
	"strings"
	"testing"
)

// Every phase 5 stub has been replaced by its work package; `sy machine` without a
// subcommand keeps its old behaviour.

func TestMachineShowStillWorks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SY_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, args := range [][]string{{"machine"}, {"machine", "show"}, {"machine", "show", "--json"}} {
		var out, errb bytes.Buffer
		if code := dispatch(args, &out, &errb); code != 0 || !strings.Contains(out.String(), "m_") {
			t.Errorf("sy %s: exit %d out %q err %q", strings.Join(args, " "), code, out.String(), errb.String())
		}
	}
}
