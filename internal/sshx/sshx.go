// Package sshx runs the person's own `ssh` safely (machines.md 4.1, 4.3, 10): the program
// comes from WY_SSH_COMMAND (words, no shell; default `ssh`) plus `-o` options, never one
// that turns host key checking off; remote commands are fixed text plus plain words, each
// single-quoted for the remote login shell; secrets travel on stdin only, never in argv.
//
// Three run modes: captured (stdout and stderr kept), stdin-fed (the same, with a reader on
// stdin) and TTY-inherited (`-t`, the person's terminal passed through, for logins). A
// failure of ssh itself (exit 255) is classified from stderr into an *Error.
//
// Depends on nothing in internal/.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// waitDelay bounds how long a cancelled call waits for ssh's pipes to close: a surviving
// ProxyJump child can hold them open after ssh itself is killed.
const waitDelay = 5 * time.Second

// EnvCommand is the environment variable that replaces the ssh program (like GIT_SSH_COMMAND).
const EnvCommand = "WY_SSH_COMMAND"

// Runner builds and runs ssh calls. The zero value is not usable; use New or FromEnv.
type Runner struct {
	// Program is the ssh program and its fixed words (WY_SSH_COMMAND, else ["ssh"]).
	Program []string
	// Options are extra `-o K=V` options (--ssh-option), already checked.
	Options []string
	// Batch adds `-o BatchMode=yes` (no terminal: an unknown host key or a password prompt
	// fails instead of waiting).
	Batch bool
	// Env, when set, is the environment of the ssh process (default os.Environ()).
	Env []string
	// Stdin, Stdout, Stderr are the terminal for TTY calls (default os.Stdin/out/err).
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Defaults holds options added because the person's config does not set them
	// (ConnectTimeout, ServerAliveInterval; see ApplyDefaults).
	Defaults []string

	// AutoDefaults makes Run and RunTTY look the destination up with `ssh -G` once and add
	// ConnectTimeout and ServerAliveInterval when the person's config sets none (the remove,
	// repoint and fleet commands turn it on). It is ignored after ApplyDefaults, which fixes Defaults by hand.
	AutoDefaults bool

	mu      sync.Mutex
	applied bool
	perHost map[string][]string
}

// New returns a Runner for program words (empty = ["ssh"]) after checking them.
func New(program []string, options ...string) (*Runner, error) {
	if len(program) == 0 {
		program = []string{"ssh"}
	}
	if err := CheckForbidden(program[1:]); err != nil {
		return nil, err
	}
	r := &Runner{Program: append([]string(nil), program...)}
	for _, o := range options {
		kv, err := ParseOption(o)
		if err != nil {
			return nil, err
		}
		r.Options = append(r.Options, kv)
	}
	return r, nil
}

// FromEnv is New with the program from WY_SSH_COMMAND (split into words; no shell, no
// quotes) and the --ssh-option values.
func FromEnv(getenv func(string) string, options ...string) (*Runner, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return New(SplitWords(getenv(EnvCommand)), options...)
}

// SplitWords splits a command such as WY_SSH_COMMAND or a --hub-admin prefix into words at
// white space. There is no shell: quotes are not interpreted.
func SplitWords(s string) []string { return strings.Fields(s) }

// ErrForbidden is returned for an option that would turn host key checking off. The command
// layer exits 2 with its message.
var ErrForbidden = errors.New("wy never turns host key checking off; check the key by hand")

var optKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// cleanVal unquotes and normalises an option value: lower case, no surrounding quotes.
func cleanVal(val string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(val), `"'`))
}

// forbidden reports whether the option key/value turns host key checking off (10.1).
func forbidden(key, val string) bool {
	k := strings.ToLower(key)
	v := cleanVal(val)
	switch k {
	case "stricthostkeychecking":
		return v == "no" || v == "off" || v == "accept-new" || v == "false"
	case "userknownhostsfile", "globalknownhostsfile":
		for _, f := range strings.Fields(v) {
			f = strings.Trim(f, `"'`)
			if f == "none" || strings.EqualFold(f, "nul") {
				return true
			}
			// /dev//null, /dev/./null, //dev/null and friends are the same file.
			if c := path.Clean(strings.ReplaceAll(f, `\`, "/")); c == "/dev/null" || strings.EqualFold(c, "nul") {
				return true
			}
		}
	case "checkhostip":
		return v == "no" || v == "false"
	}
	return false
}

// splitKV splits "K=V" or "K V".
func splitKV(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "= \t"); i > 0 {
		return s[:i], strings.TrimLeft(s[i:], "= \t"), true
	}
	return s, "", false
}

// ParseOption checks one --ssh-option value (K=V) and returns it as K=V.
func ParseOption(o string) (string, error) {
	k, v, ok := splitKV(o)
	if !ok || !optKeyRe.MatchString(k) || strings.ContainsAny(v, "\r\n\x00") {
		return "", fmt.Errorf("--ssh-option: want Key=Value, got %q", o)
	}
	if forbidden(k, v) {
		return "", fmt.Errorf("--ssh-option %s=%s: %w", k, v, ErrForbidden)
	}
	return k + "=" + v, nil
}

// shortArgFlags are the ssh flags that take an argument: in a combined group such as
// `-vo Key=V` the rest of the group (or the next word) is that argument.
const shortArgFlags = "BbcDEeFIiJLlmOopQRSWw"

// CheckForbidden checks the words of WY_SSH_COMMAND after the program: any `-o` option
// (`-o K=V`, `-oK=V`, `-o 'K V'`, and inside a combined group such as `-vo K=V`) that turns
// host key checking off is refused.
func CheckForbidden(words []string) error {
	for i := 0; i < len(words); i++ {
		w := words[i]
		if len(w) < 2 || w[0] != '-' || w[1] == '-' {
			continue
		}
		for j := 1; j < len(w); j++ {
			c := w[j]
			if !strings.ContainsRune(shortArgFlags, rune(c)) {
				continue // a flag without argument: keep scanning the group
			}
			if c != 'o' {
				break // the rest of the group is this flag's argument
			}
			var opt string
			if j+1 < len(w) {
				opt = w[j+1:]
			} else if i+1 < len(words) {
				opt = words[i+1]
				if !strings.ContainsAny(opt, "= ") && i+2 < len(words) {
					opt += " " + words[i+2] // -o Key Value as separate words
				}
				i++
			}
			k, v, _ := splitKV(opt)
			if forbidden(k, v) {
				return fmt.Errorf("%s has %s=%s: %w", EnvCommand, k, v, ErrForbidden)
			}
			break
		}
	}
	return nil
}

// Call is one ssh invocation.
type Call struct {
	// Host is the destination as the person typed it ([user@]host or a config alias).
	Host string
	// Remote are the remote command words: plain words (CheckWord) or the fixed script of a
	// Script call. Empty means no command (only with Extra such as -N, or -G).
	Remote []string
	// Stdin is fed to the remote command (secrets travel here). Nil means no input.
	Stdin io.Reader
	// Extra are ssh arguments placed before the host (-R specs, -o ExitOnForwardFailure=yes).
	Extra []string
	// TTY requests a terminal (-t) and inherits the person's terminal (logins).
	TTY bool
	// Interactive leaves stdin and stderr attached to the person's terminal while stdout is
	// captured (the first connection, so OpenSSH can ask about an unknown host key).
	Interactive bool
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@%+-]*$`)

// CheckHost refuses destinations that ssh could read as an option or that carry a port
// (machines.md 4.1: host:port is not accepted, use the config or --ssh-option Port=).
func CheckHost(h string) error {
	switch {
	case h == "":
		return errors.New("no host given")
	case strings.HasPrefix(h, "-"):
		return fmt.Errorf("host %q starts with '-'", h)
	case strings.Contains(h, ":"):
		return fmt.Errorf("host %q has a port or is an address with ':': use an alias in ~/.ssh/config or --ssh-option Port=<n>", h)
	case !hostRe.MatchString(h):
		return fmt.Errorf("host %q is not a plain [user@]host", h)
	}
	return nil
}

// Argv returns the full argv of a call (program first). It never contains anything but the
// program words, options, Extra, the host and the quoted remote words.
func (r *Runner) Argv(c Call) ([]string, error) { return r.argv(c, r.Defaults) }

func (r *Runner) argv(c Call, defaults []string) ([]string, error) {
	if err := CheckHost(c.Host); err != nil {
		return nil, err
	}
	if err := checkRemote(c.Remote); err != nil {
		return nil, err
	}
	argv := append([]string(nil), r.Program...)
	for _, o := range r.Options {
		argv = append(argv, "-o", o)
	}
	for _, o := range defaults {
		argv = append(argv, "-o", o)
	}
	if r.Batch && !c.TTY && !c.Interactive {
		argv = append(argv, "-o", "BatchMode=yes")
	}
	for i := 0; i < len(c.Extra); i++ {
		if c.Extra[i] == "-o" && i+1 < len(c.Extra) {
			if k, v, _ := splitKV(c.Extra[i+1]); forbidden(k, v) {
				return nil, ErrForbidden
			}
		}
	}
	argv = append(argv, c.Extra...)
	if c.TTY {
		argv = append(argv, "-t")
	} else if len(c.Remote) > 0 {
		argv = append(argv, "-T")
	}
	argv = append(argv, c.Host)
	if len(c.Remote) > 0 {
		argv = append(argv, "--", QuoteAll(c.Remote))
	}
	return argv, nil
}

// Result is the outcome of a captured call that reached the remote command.
type Result struct {
	Stdout []byte
	Stderr []byte
	Exit   int
}

func (r *Runner) env() []string {
	if r.Env != nil {
		return r.Env
	}
	return os.Environ()
}

// Run runs a captured call. A remote nonzero exit is a Result with that Exit and a nil
// error; ssh's own failure (exit 255) is an *Error with its class.
func (r *Runner) Run(ctx context.Context, c Call) (Result, error) {
	argv, err := r.argv(c, r.defaultsFor(ctx, c.Host))
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = r.env()
	cmd.WaitDelay = waitDelay
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	if c.Interactive {
		cmd.Stdin = r.stdin()
		cmd.Stderr = io.MultiWriter(&errb, r.stderr())
	} else {
		cmd.Stdin = c.Stdin
		cmd.Stderr = &errb
	}
	err = cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	return finish(c.Host, res, err)
}

// RunTTY runs a call with -t and the person's terminal inherited (stdin, stdout and stderr
// pass through, so a login code typed by the person goes straight to the remote program).
func (r *Runner) RunTTY(ctx context.Context, c Call) (int, error) {
	c.TTY = true
	argv, err := r.argv(c, r.defaultsFor(ctx, c.Host))
	if err != nil {
		return 0, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = r.env()
	cmd.WaitDelay = waitDelay
	var errb bytes.Buffer
	cmd.Stdin, cmd.Stdout = r.stdin(), r.stdout()
	cmd.Stderr = io.MultiWriter(r.stderr(), &tail{b: &errb})
	err = cmd.Run()
	res, err := finish(c.Host, Result{Stderr: errb.Bytes()}, err)
	return res.Exit, err
}

func (r *Runner) stdin() io.Reader {
	if r.Stdin != nil {
		return r.Stdin
	}
	return os.Stdin
}

func (r *Runner) stdout() io.Writer {
	if r.Stdout != nil {
		return r.Stdout
	}
	return os.Stdout
}

func (r *Runner) stderr() io.Writer {
	if r.Stderr != nil {
		return r.Stderr
	}
	return os.Stderr
}

// tail keeps at most the last 8 KiB written (enough to classify an ssh failure).
type tail struct{ b *bytes.Buffer }

func (t *tail) Write(p []byte) (int, error) {
	t.b.Write(p)
	if t.b.Len() > 8<<10 {
		rest := append([]byte(nil), t.b.Bytes()[t.b.Len()-8<<10:]...)
		t.b.Reset()
		t.b.Write(rest)
	}
	return len(p), nil
}

func finish(host string, res Result, err error) (Result, error) {
	if err == nil {
		return res, nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return res, fmt.Errorf("cannot run ssh: %w", err)
	}
	res.Exit = ee.ExitCode()
	if res.Exit == 255 {
		return res, Classify(host, res.Exit, string(res.Stderr))
	}
	if res.Exit < 0 {
		return res, fmt.Errorf("ssh to %s: %w", host, err)
	}
	return res, nil
}

// defaultsFor returns the default options for a call to host: r.Defaults when
// ApplyDefaults ran, else (with AutoDefaults) what `ssh -G host` says is missing, looked up
// once per host. When the lookup fails both defaults are added.
func (r *Runner) defaultsFor(ctx context.Context, host string) []string {
	r.mu.Lock()
	if r.applied || !r.AutoDefaults {
		d := r.Defaults
		r.mu.Unlock()
		return d
	}
	if d, ok := r.perHost[host]; ok {
		r.mu.Unlock()
		return d
	}
	r.mu.Unlock()
	var d []string
	if cfg, err := r.G(ctx, host); err == nil {
		d = defaultsFrom(cfg)
	} else if ctx.Err() == nil {
		d = []string{DefaultOptions["connecttimeout"], DefaultOptions["serveraliveinterval"]}
	} else {
		return nil
	}
	r.mu.Lock()
	if r.perHost == nil {
		r.perHost = map[string][]string{}
	}
	r.perHost[host] = d
	r.mu.Unlock()
	return d
}
