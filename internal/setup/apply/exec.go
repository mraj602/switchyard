package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Exec runs the external commands of `run` actions (claude plugin ...).
type Exec interface {
	// Run runs argv with env overrides applied to the process environment (a nil value
	// unsets the variable), stdin closed, and returns stdout.
	Run(ctx context.Context, argv []string, env map[string]*string) ([]byte, error)
}

// OSExec is the os/exec implementation: stdin closed, 60 s timeout by default.
type OSExec struct {
	Timeout time.Duration
	// Dir is the working directory of every command. Claude Code reads
	// <cwd>/.claude/settings.json as project settings, so plugin commands must run from a
	// neutral folder (setup.md 5.2: the state dir), never $HOME. Empty means os.TempDir().
	Dir string
}

// Run implements Exec.
func (e OSExec) Run(ctx context.Context, argv []string, env map[string]*string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("apply: empty argv")
	}
	t := e.Timeout
	if t == 0 {
		t = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = overlayEnv(os.Environ(), env)
	cmd.Dir = e.Dir
	if cmd.Dir == "" {
		cmd.Dir = os.TempDir()
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if goalHolds(out.Bytes()) {
			return out.Bytes(), nil // exit 1 with alreadyInGoalState: the goal already holds
		}
		return out.Bytes(), fmt.Errorf("%s: %w: %s", strings.Join(argv[:min(len(argv), 3)], " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func overlayEnv(base []string, env map[string]*string) []string {
	out := make([]string, 0, len(base)+len(env))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, over := env[k]; over {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range env {
		if v != nil {
			out = append(out, k+"="+*v)
		}
	}
	return out
}

// checkOutcome parses the last JSON object line of a `--json` command's output; an
// "outcome" other than "ok" is an error. Output without one is fine (exit status decided).
func checkOutcome(out []byte) error {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var o outcomeLine
		if json.Unmarshal([]byte(l), &o) != nil {
			continue
		}
		if o.done() {
			return nil
		}
		if o.Outcome != "" && o.Outcome != "ok" {
			return fmt.Errorf("command outcome %q %s", o.Outcome, o.Error)
		}
		return nil
	}
	return nil
}

// outcomeLine is the `--json` line of a claude plugin command (setup.md 5.2).
type outcomeLine struct {
	Outcome            string `json:"outcome"`
	Error              string `json:"error"`
	FailureCode        string `json:"failureCode"`
	AlreadyInGoalState bool   `json:"alreadyInGoalState"`
}

// done reports a "failed" outcome that means the goal already holds: alreadyInGoalState
// (enable/disable of something already so), not_installed (uninstall) and not_configured
// (marketplace remove), the same codes internal/claudecli treats as success.
func (o outcomeLine) done() bool {
	switch {
	case o.AlreadyInGoalState:
		return true
	case o.FailureCode == "already_in_goal_state", o.FailureCode == "not_installed", o.FailureCode == "not_configured":
		return true
	}
	return false
}

// goalHolds reports whether output's last JSON line says the goal already holds.
func goalHolds(out []byte) bool {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var o outcomeLine
		if json.Unmarshal([]byte(l), &o) != nil {
			continue
		}
		return o.done()
	}
	return false
}
