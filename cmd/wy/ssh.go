package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/sshx"
	"github.com/mraj602/watchyard/internal/termctl"
	"github.com/mraj602/watchyard/internal/termenv"
)

// `wy ssh <ssh args…>` (docs/spec/terminal.md T9): the real ssh with
// `-o SendEnv=LC_SY_FOCUS -o SendEnv=LC_SY_HOST` first, so the server's sessions can be
// focused in this laptop's tab. Nothing else is added to the person's arguments.

const sshUsage = `usage: wy ssh <ssh arguments...>
       wy ssh --print-config

Runs ssh (or WY_SSH_COMMAND) with -o SendEnv=LC_SY_FOCUS -o SendEnv=LC_SY_HOST put before
your arguments, and passes this tab's Warp focus URL and this machine's id in those
variables, so Claude Code sessions on the server can be focused from the board here.
--print-config prints the SendEnv lines for your own ssh config (wy never writes it).
Exit code: ssh's own; 127 when there is no ssh.
`

const sshPrintConfig = `# wy ssh passes these with -o on every call. To use them from plain ssh instead, add this
# to ~/.ssh/config (wy never writes your ssh config):
Host *
    SendEnv LC_SY_FOCUS LC_SY_HOST
# The server's sshd must accept them; AcceptEnv LC_* is the default on most distributions.
`

// sshEnv is the environment wy ssh reads; tests replace it.
var sshEnv = termenv.FromOS

func runSSH(args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 {
		switch args[0] {
		case "--print-config":
			fmt.Fprint(stdout, sshPrintConfig)
			return nil
		case "-h", "--help":
			fmt.Fprint(stderr, sshUsage)
			return nil
		}
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, sshUsage)
		return errUsage
	}
	env := sshEnv()
	prog := sshx.SplitWords(env.Lookup(sshx.EnvCommand))
	if len(prog) == 0 {
		prog = []string{"ssh"}
	}
	if err := sshx.CheckForbidden(prog[1:]); err != nil {
		return exitWith(exitUsage, err)
	}
	path, err := exec.LookPath(prog[0])
	if err != nil {
		return exitWith(127, fmt.Errorf("%s: not found on PATH", prog[0]))
	}

	argv := append([]string{}, prog[1:]...)
	argv = append(argv, "-o", "SendEnv=LC_SY_FOCUS", "-o", "SendEnv=LC_SY_HOST")
	argv = append(argv, args...)

	cmd := exec.Command(path, argv...)
	cmd.Env = sshChildEnv(os.Environ(), env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr

	// A Ctrl-C at the terminal belongs to ssh (it ends the session itself); wy waits.
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code := ee.ExitCode()
			if code < 0 {
				code = 255 // killed by a signal
			}
			return exitWith(code, nil)
		}
		return exitWith(127, err)
	}
	return nil
}

// sshChildEnv is base with LC_SY_FOCUS and LC_SY_HOST replaced (T9): the focus URL is
// LC_SY_FOCUS if set (a nested hop), else WARP_FOCUS_URL, kept only when it passes the T7
// allow-list; the host is this machine's id. Without a usable focus URL neither is set (a
// host id alone focuses nothing), and any inherited value is removed so a stale one is
// never forwarded.
func sshChildEnv(base []string, env termenv.Env) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if isSYForwardVar(kv, env.GOOS) {
			continue
		}
		out = append(out, kv)
	}
	// A nested hop (LC_SY_FOCUS inherited from the first ssh): the tab and its machine are
	// the first hop's. Forward both unchanged, never this intermediate machine's id, or the
	// last server would believe the tab is on the middle one.
	if inherited := env.Lookup("LC_SY_FOCUS"); inherited != "" && termctl.ValidFocusURL(inherited) {
		if host := env.Lookup("LC_SY_HOST"); machineIDRe.MatchString(host) {
			return append(out, "LC_SY_FOCUS="+inherited, "LC_SY_HOST="+host)
		}
	}
	focus := env.Lookup("LC_SY_FOCUS")
	if focus == "" {
		focus = env.Lookup("WARP_FOCUS_URL")
	}
	if !termctl.ValidFocusURL(focus) {
		return out
	}
	out = append(out, "LC_SY_FOCUS="+focus)
	if id := thisMachineID(env); id != "" {
		out = append(out, "LC_SY_HOST="+id)
	}
	return out
}

var machineIDRe = regexp.MustCompile(`^m_[0-9A-HJKMNP-TV-Z]{26}$`)

// isSYForwardVar reports whether kv ("NAME=value") is LC_SY_FOCUS or LC_SY_HOST. Windows
// environment names are case-insensitive, so the match is too there.
func isSYForwardVar(kv, goos string) bool {
	name, _, _ := strings.Cut(kv, "=")
	if goos == "windows" {
		name = strings.ToUpper(name)
	}
	return name == "LC_SY_FOCUS" || name == "LC_SY_HOST"
}

// thisMachineID is this machine's id from <state>/identity ("" when it cannot be read or
// created).
func thisMachineID(env termenv.Env) string {
	dirs, err := paths.Resolve(paths.Env{GOOS: env.GOOS, Home: env.Home, Getenv: env.Lookup, Exists: paths.StatExists})
	if err != nil {
		return ""
	}
	m, err := identity.LoadOrCreateMachine(filepath.Clean(dirs.State.Path), identity.MachineOptions{})
	if err != nil {
		return ""
	}
	return m.ID
}
