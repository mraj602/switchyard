package main

import (
	"bytes"
	"strings"
	"testing"
)

// checkUsage wants exit 2 for each argv.
func checkUsage(t *testing.T, argvs ...[]string) {
	t.Helper()
	for _, args := range argvs {
		var out, errb bytes.Buffer
		if code := dispatch(args, &out, &errb); code != 2 {
			t.Errorf("sy %s: exit %d, want 2", strings.Join(args, " "), code)
		}
	}
}

// The four phase 7 commands are listed in the usage text with their phase.
func TestPhase7CommandsListed(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch([]string{"help"}, &out, &errb); code != 0 {
		t.Fatalf("help: exit %d", code)
	}
	for _, name := range []string{"board", "term", "open", "ssh"} {
		found := false
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), name+" ") && strings.HasSuffix(line, "(phase 7)") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s not listed as phase 7:\n%s", name, out.String())
		}
	}
}
