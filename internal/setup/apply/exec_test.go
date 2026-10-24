package apply

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckOutcomeGoalHolds(t *testing.T) {
	ok := []string{
		`{"outcome":"ok"}`,
		`{"command":"enable","outcome":"failed","failureCode":"already_in_goal_state","alreadyInGoalState":true}`,
		`{"command":"uninstall","outcome":"failed","failureCode":"not_installed"}`,
		`{"command":"marketplace-remove","outcome":"failed","failureCode":"not_configured"}`,
		"",
	}
	for _, o := range ok {
		if err := checkOutcome([]byte(o)); err != nil {
			t.Errorf("%s: %v", o, err)
		}
	}
	if err := checkOutcome([]byte(`{"outcome":"failed","failureCode":"not_found"}`)); err == nil {
		t.Error("not_found must fail")
	}
}

// TestOSExecExit1GoalHoldsAndDir runs a fake command that exits 1 with alreadyInGoalState and
// records its working directory: success, and the cwd is the neutral Dir.
func TestOSExecExit1GoalHoldsAndDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script")
	}
	bin := t.TempDir()
	dir := t.TempDir()
	outf := filepath.Join(bin, "cwd")
	script := "#!/bin/sh\npwd > '" + outf + "'\necho '{\"command\":\"enable\",\"outcome\":\"failed\",\"failureCode\":\"already_in_goal_state\",\"alreadyInGoalState\":true}'\nexit 1\n"
	fake := filepath.Join(bin, "fake")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (OSExec{Dir: dir}).Run(context.Background(), []string{fake}, nil); err != nil {
		t.Fatalf("goal holds must be success: %v", err)
	}
	got, _ := os.ReadFile(outf)
	want, _ := filepath.EvalSymlinks(dir)
	if g, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got))); g != want {
		t.Errorf("cwd = %q, want %q", g, want)
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '{\"outcome\":\"failed\",\"failureCode\":\"not_found\"}'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (OSExec{Dir: dir}).Run(context.Background(), []string{fake}, nil); err == nil {
		t.Error("not_found exit 1 must fail")
	}
}

// TestOSExecClaudeUsesLocatedPath: a plan's argv[0] "claude" runs the located binary, so setup
// works over SSH where ~/.local/bin is not on PATH (wy machine add).
func TestOSExecClaudeUsesLocatedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script")
	}
	bin := t.TempDir()
	fake := filepath.Join(bin, "claude-located")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho located\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	out, err := (OSExec{Claude: fake}).Run(context.Background(), []string{"claude", "plugin", "list"}, nil)
	if err != nil || strings.TrimSpace(string(out)) != "located" {
		t.Fatalf("out %q err %v, want the located binary", out, err)
	}
	if _, err := (OSExec{}).Run(context.Background(), []string{"claude", "--version"}, nil); err == nil {
		t.Error("without Claude, argv[0] claude must be looked up on PATH (empty here)")
	}
}
