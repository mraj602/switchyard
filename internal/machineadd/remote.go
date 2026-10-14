package machineadd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mraj602/switchyard/internal/sshx"
	"github.com/mraj602/switchyard/internal/update"
)

// RemoteSy is the remote's sy relative to its home folder (ssh starts there): no remote
// command depends on the remote PATH (machines.md 4.3).
const RemoteSy = ".local/bin/sy"

// Platform maps `uname -sm` to Go's goos/goarch. Windows-like systems are ErrWindows.
func Platform(uname string) (goos, goarch string, err error) {
	f := strings.Fields(uname)
	if len(f) < 2 {
		return "", "", fmt.Errorf("cannot read the remote platform from %q", strings.TrimSpace(uname))
	}
	sys, mach := f[0], f[len(f)-1]
	switch {
	case sys == "Linux":
		goos = "linux"
	case sys == "Darwin":
		goos = "darwin"
	case sys == "FreeBSD":
		goos = "freebsd"
	case sys == "Windows_NT", strings.HasPrefix(sys, "MINGW"), strings.HasPrefix(sys, "CYGWIN"), strings.HasPrefix(sys, "MSYS"):
		return "", "", ErrWindows
	default:
		return "", "", fmt.Errorf("unsupported remote system %q", sys)
	}
	switch mach {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64", "armv8l":
		goarch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported remote machine type %q", mach)
	}
	return goos, goarch, nil
}

// ErrWindows is a remote that runs Windows: sy machine add supports Linux and macOS remotes.
var ErrWindows = errors.New("the remote runs Windows: use install.ps1 on that machine")

// remote runs things on one host through the person's ssh.
type remote struct {
	ssh  *sshx.Runner
	host string
}

func (r remote) run(ctx context.Context, words []string, stdin []byte) (sshx.Result, error) {
	c := sshx.Call{Host: r.host, Remote: words}
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	return r.ssh.Run(ctx, c)
}

// script runs a fixed sh script with plain-word arguments.
func (r remote) script(ctx context.Context, s string, stdin io.Reader, args ...string) (sshx.Result, error) {
	words, err := sshx.Script(s, args...)
	if err != nil {
		return sshx.Result{}, err
	}
	return r.ssh.Run(ctx, sshx.Call{Host: r.host, Remote: words, Stdin: stdin})
}

// Transfer scripts (machines.md 4.2 step 3). $1 is the destination kind (bin: ~/.local/bin;
// update: <remote state>/update), $2 the file name, $3 a unique suffix. The first writes
// stdin to a temporary file and prints its SHA-256 with whatever tool the remote has; the
// second moves it into place (atomic within the folder: running hooks keep the old inode).
const (
	destDir = `if [ "$1" = update ]; then case "$XDG_STATE_HOME" in /*) d=$XDG_STATE_HOME/switchyard/update;; *) d=$HOME/.local/state/switchyard/update;; esac; else d=.local/bin; fi; t=$d/.$2.$3.tmp`

	writeScript = `set -e; ` + destDir + `; mkdir -p "$d"; umask 077; rm -f "$t"; cat > "$t"; if [ "$1" = bin ]; then chmod 0755 "$t"; fi; echo "path=$t"; ` +
		`if command -v sha256sum >/dev/null 2>&1; then echo "sha=$(sha256sum < "$t")"; ` +
		`elif command -v shasum >/dev/null 2>&1; then echo "sha=$(shasum -a 256 < "$t")"; ` +
		`elif command -v openssl >/dev/null 2>&1; then echo "sha=$(openssl dgst -sha256 -r < "$t")"; ` +
		`else echo sha=none; fi`

	moveScript    = `set -e; ` + destDir + `; mv -f "$t" "$d/$2"; echo "path=$d/$2"`
	discardScript = destDir + `; rm -f "$t"`
	versionScript = destDir + `; "$t" version --short`
)

// pushed is the outcome of a transfer.
type pushed struct {
	Path     string // remote path after the move (relative to the remote home for bin)
	Verified bool   // the remote's SHA-256 matched
}

// push streams the local file to the remote and moves it into place only when the SHA-256
// the remote computes equals ours. With no hash tool on the remote, wantVersion (when set)
// must be what the new file prints for `version --short`; the caller warns that the hash
// was not checked remotely.
func (r remote) push(ctx context.Context, local, kind, name, wantVersion string) (pushed, error) {
	sum, err := fileSHA256(local)
	if err != nil {
		return pushed{}, err
	}
	f, err := os.Open(local)
	if err != nil {
		return pushed{}, err
	}
	defer f.Close()
	suffix := strconv.Itoa(os.Getpid())
	res, err := r.script(ctx, writeScript, f, kind, name, suffix)
	if err != nil {
		return pushed{}, err
	}
	if res.Exit != 0 {
		return pushed{}, fmt.Errorf("writing %s on %s failed (exit %d): %s", name, r.host, res.Exit, firstLine(res.Stderr))
	}
	kv := parseKV(res.Stdout)
	got := strings.ToLower(firstField(kv["sha"]))
	out := pushed{}
	switch {
	case got == sum:
		out.Verified = true
	case got == "none":
		if wantVersion == "" {
			_, _ = r.script(ctx, discardScript, nil, kind, name, suffix)
			return pushed{}, fmt.Errorf("%s has no SHA-256 tool (sha256sum, shasum or openssl), so the copy cannot be checked", r.host)
		}
		v, verr := r.script(ctx, versionScript, nil, kind, name, suffix)
		if verr != nil || v.Exit != 0 || strings.TrimSpace(string(v.Stdout)) != wantVersion {
			_, _ = r.script(ctx, discardScript, nil, kind, name, suffix)
			return pushed{}, fmt.Errorf("the copied sy on %s does not print version %s", r.host, wantVersion)
		}
	default:
		_, _ = r.script(ctx, discardScript, nil, kind, name, suffix)
		return pushed{}, fmt.Errorf("the copy on %s has SHA-256 %s, not %s: nothing was replaced", r.host, short(got), short(sum))
	}
	mv, err := r.script(ctx, moveScript, nil, kind, name, suffix)
	if err != nil {
		return pushed{}, err
	}
	if mv.Exit != 0 {
		return pushed{}, fmt.Errorf("moving %s into place on %s failed: %s", name, r.host, firstLine(mv.Stderr))
	}
	out.Path = parseKV(mv.Stdout)["path"]
	return out, nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	if s == "" {
		return "(none)"
	}
	return s
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// parseKV reads key=value lines (the first value of each key; hostkey lines are collected
// by parseKVAll).
func parseKV(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			if _, seen := out[k]; !seen {
				out[k] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

func parseKVAll(b []byte, key string) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), key+"="); ok && strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// FleetRemote implements update.Remote for `sy update --machines` with the person's ssh.
type FleetRemote struct{ SSH *sshx.Runner }

// Run implements update.Remote.
func (f FleetRemote) Run(ctx context.Context, m update.FleetMachine, argv []string) (string, int, error) {
	res, err := remote{ssh: f.SSH, host: m.Target}.run(ctx, argv, nil)
	if err != nil {
		return string(res.Stdout), res.Exit, err
	}
	return string(res.Stdout), res.Exit, nil
}

// Platform implements update.Remote (uname -sm).
func (f FleetRemote) Platform(ctx context.Context, m update.FleetMachine) (string, string, error) {
	res, err := remote{ssh: f.SSH, host: m.Target}.run(ctx, []string{"uname", "-sm"}, nil)
	if err != nil {
		return "", "", err
	}
	if res.Exit != 0 {
		return "", "", fmt.Errorf("uname failed on %s", m.Name)
	}
	return Platform(string(res.Stdout))
}

// Push implements update.Remote: the hash-checked transfer into <remote state>/update/.
func (f FleetRemote) Push(ctx context.Context, m update.FleetMachine, localPath, name string) (string, error) {
	if err := sshx.CheckWord(name); err != nil {
		return "", err
	}
	p, err := remote{ssh: f.SSH, host: m.Target}.push(ctx, localPath, "update", name, "")
	if err != nil {
		return "", err
	}
	return p.Path, nil
}

var _ update.Remote = FleetRemote{}
