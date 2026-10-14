package sshx

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Config is `ssh -G <host>` output: the effective options for a host, keys lower-cased,
// every value of a repeated key in order (remoteforward, identityfile, ...).
type Config map[string][]string

// ParseConfig reads `ssh -G` output ("key value" per line).
func ParseConfig(out []byte) Config {
	c := Config{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, _ := strings.Cut(line, " ")
		k = strings.ToLower(k)
		c[k] = append(c[k], strings.TrimSpace(v))
	}
	return c
}

// Get returns the first value of key ("" when absent).
func (c Config) Get(key string) string {
	if v := c[strings.ToLower(key)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// HostName, User and Port are what ssh will really connect to.
func (c Config) HostName() string { return c.Get("hostname") }
func (c Config) User() string     { return c.Get("user") }
func (c Config) Port() int {
	n, err := strconv.Atoi(c.Get("port"))
	if err != nil || n < 1 {
		return 22
	}
	return n
}

// ProxyJump is the jump host, "" when none.
func (c Config) ProxyJump() string {
	if v := c.Get("proxyjump"); v != "" && v != "none" {
		return v
	}
	return ""
}

// RemoteForwards are the RemoteForward entries, normalised to "listen target" (ssh -G prints
// "remoteforward [127.0.0.1]:47830 [127.0.0.1]:8787" on some versions and plain host:port on
// others).
func (c Config) RemoteForwards() []string {
	var out []string
	for _, v := range c["remoteforward"] {
		f := strings.Fields(v)
		for i := range f {
			f[i] = normHostPort(f[i])
		}
		out = append(out, strings.Join(f, " "))
	}
	return out
}

// normHostPort turns "[127.0.0.1]:47830" into "127.0.0.1:47830".
func normHostPort(s string) string {
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i > 0 {
			return s[1:i] + s[i+1:]
		}
	}
	return s
}

// HostKeyCheckingOff reports whether the person's config turns host key checking off for
// the host (sy warns; it never overrides the person's config, machines.md 10.1).
func (c Config) HostKeyCheckingOff() bool {
	return forbidden("stricthostkeychecking", c.Get("stricthostkeychecking")) ||
		forbidden("userknownhostsfile", c.Get("userknownhostsfile"))
}

// DefaultOptions are the options sy adds unless the person's config sets them (4.3).
var DefaultOptions = map[string]string{"connecttimeout": "ConnectTimeout=15", "serveraliveinterval": "ServerAliveInterval=15"}

// ApplyDefaults sets r.Defaults from c: ConnectTimeout=15 when ssh -G says "none",
// ServerAliveInterval=15 when it says "0".
func (r *Runner) ApplyDefaults(c Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = true
	r.Defaults = defaultsFrom(c)
}

// defaultsFrom lists the default options that c does not already set.
func defaultsFrom(c Config) []string {
	var d []string
	if v := c.Get("connecttimeout"); v == "" || v == "none" || v == "0" {
		d = append(d, DefaultOptions["connecttimeout"])
	}
	if v := c.Get("serveraliveinterval"); v == "" || v == "0" {
		d = append(d, DefaultOptions["serveraliveinterval"])
	}
	return d
}

// G runs `ssh -G <host>` with the runner's program and options (it never connects).
func (r *Runner) G(ctx context.Context, host string) (Config, error) {
	if err := CheckHost(host); err != nil {
		return nil, err
	}
	argv := append([]string(nil), r.Program...)
	for _, o := range r.Options {
		argv = append(argv, "-o", o)
	}
	argv = append(argv, "-G", host)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = r.env()
	cmd.WaitDelay = waitDelay
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh -G %s: %v: %s", host, err, lastLine(errb.String()))
	}
	return ParseConfig(out.Bytes()), nil
}
