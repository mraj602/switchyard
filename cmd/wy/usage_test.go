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
			t.Errorf("wy %s: exit %d, want 2", strings.Join(args, " "), code)
		}
	}
}

// The terminal commands are listed in the usage text, one per line, without build notes.
func TestTerminalCommandsListed(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch([]string{"help"}, &out, &errb); code != 0 {
		t.Fatalf("help: exit %d", code)
	}
	if strings.Contains(out.String(), "phase") {
		t.Errorf("usage mentions a build phase:\n%s", out.String())
	}
	for _, name := range []string{"board", "term", "open", "ssh"} {
		found := false
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), name+" ") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s not listed:\n%s", name, out.String())
		}
	}
}

func TestHelpUnknownCommandIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch([]string{"help", "nonesuch"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

// -h on a subcommand with flags prints its usage and exits 0; a bad flag exits 2.
func TestFlagHelpExitsZero(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch([]string{"update", "-h"}, &out, &errb); code != 0 {
		t.Errorf("wy update -h: exit %d, want 0", code)
	}
	if !strings.Contains(errb.String(), "-rollback") {
		t.Errorf("no usage printed: %q", errb.String())
	}
	checkUsage(t, []string{"update", "--no-such-flag"})
}
