package pull

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// File names inside the pull folder (paths.md).
const (
	ConfigFile     = "ssh_config"
	KnownHostsFile = "known_hosts"
)

var (
	hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,252}$`)
	userRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
	syRe   = regexp.MustCompile(`^[A-Za-z0-9._/~-]{1,200}$`)
)

// ValidateTarget checks the fields that go into ssh_config, so a value can never add an
// option or a line (no whitespace, quotes or newlines), and that host and user cannot be
// read as ssh options (no leading dash).
func ValidateTarget(t store.PullTarget) error {
	switch {
	case !hostRe.MatchString(t.Host):
		return fmt.Errorf("bad SSH host %q", t.Host)
	case !userRe.MatchString(t.User):
		return fmt.Errorf("bad SSH user %q", t.User)
	case t.Port < 1 || t.Port > 65535:
		return fmt.Errorf("bad SSH port %d", t.Port)
	case t.Jump != "":
		// A ProxyJump host would be reached with ssh's own defaults (no pinned key, the
		// person's known_hosts, a prompt): the pinned-host-key guarantee would stop at the hop.
		return fmt.Errorf("ProxyJump (%q) is not supported: the jump host's key cannot be pinned; reach the server directly (a tailnet address) instead", t.Jump)
	case t.RemoteSy != "" && !syRe.MatchString(t.RemoteSy):
		return fmt.Errorf("bad remote wy path %q", t.RemoteSy)
	}
	return nil
}

// Alias is the ssh Host (and HostKeyAlias) of a target.
func Alias(t store.PullTarget) string { return "sy-" + t.ID }

// WriteConfig writes <dir>/ssh_config and <dir>/known_hosts (0600) from the targets
// (machines.md 3.4): one Host block per target with the fixed option set, and one
// known_hosts line per pinned key under the target's alias (plus the `[alias]:port` form
// for a non-default port). The person's own ~/.ssh/config is irrelevant: ssh runs with -F.
// A target that fails ValidateTarget is left out and reported in the returned error; the
// rest are still written.
func WriteConfig(dir string, targets []store.PullTarget) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sorted := append([]store.PullTarget(nil), targets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var cfg, kh strings.Builder
	var bad []error
	for _, t := range sorted {
		if err := ValidateTarget(t); err != nil {
			bad = append(bad, fmt.Errorf("target %s: %w", t.Name, err))
			continue
		}
		a := Alias(t)
		fmt.Fprintf(&cfg, "Host %s\n  HostName %s\n  Port %d\n  User %s\n", a, t.Host, t.Port, t.User)
		fmt.Fprintf(&cfg, "  HostKeyAlias %s\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile /dev/null\n"+
			"  StrictHostKeyChecking yes\n  UpdateHostKeys no\n  IdentityFile %s\n  IdentitiesOnly yes\n  IdentityAgent none\n"+
			"  BatchMode yes\n  ForwardAgent no\n  ForwardX11 no\n  ClearAllForwardings yes\n  RequestTTY no\n"+
			"  ControlMaster no\n  ControlPath none\n  ConnectTimeout 10\n  ServerAliveInterval 15\n  ServerAliveCountMax 2\n"+
			"  LogLevel ERROR\n\n",
			a, quotePath(filepath.Join(dir, KnownHostsFile)), quotePath(filepath.Join(dir, KeyFile)))
		for _, k := range t.HostKeys {
			f := strings.Fields(k)
			if len(f) < 2 || !hostKeyTypes[f[0]] {
				continue
			}
			fmt.Fprintf(&kh, "%s %s %s\n", a, f[0], f[1])
			if t.Port != 22 {
				fmt.Fprintf(&kh, "[%s]:%d %s %s\n", a, t.Port, f[0], f[1])
			}
		}
	}
	if err := atomicfile.Write(filepath.Join(dir, ConfigFile), []byte(cfg.String()), 0o600); err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(dir, KnownHostsFile), []byte(kh.String()), 0o600); err != nil {
		return err
	}
	return errors.Join(bad...)
}

// quotePath makes a path safe as an ssh_config value: ssh expands %-tokens in file names, so
// a literal % is doubled, and a path with spaces is quoted.
func quotePath(p string) string {
	p = strings.ReplaceAll(p, "%", "%%")
	if strings.ContainsAny(p, " \t") {
		return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
	}
	return p
}

// SSHArgs is the argv of one run (after the ssh program): the generated config, no
// terminal, the target's alias and the remote command (ignored under a forced command).
func SSHArgs(dir string, t store.PullTarget) []string {
	remote := t.RemoteSy
	if remote == "" {
		remote = ".local/bin/wy"
	}
	return []string{"-F", filepath.Join(dir, ConfigFile), "-T", Alias(t), remote, "outbox", "drain", "--stdio"}
}
