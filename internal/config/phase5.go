package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Reach values of hub.reach (machines.md 3.1).
const (
	ReachDirect  = "direct"
	ReachForward = "forward"
	ReachPull    = "pull"
)

// Direct push defaults (machines.md 5.2, 5.4).
const (
	DefaultDirectServer = "https://ntfy.sh"
	DefaultDirectAfterS = 120
	// DefaultDirectAfterPullS is the default `after_s` in pull mode (machines.md 5.4 condition 4):
	// a pull interval is up to a minute, so the hub counts as unreachable later.
	DefaultDirectAfterPullS = 180
)

// UnixDialPrefix starts a hub.dial value that names a Unix socket (forward mode with
// --forward-socket).
const UnixDialPrefix = "unix:"

// CheckDial validates a hub.dial value (machines.md 3.3): empty, `unix:<path>`, or a
// loopback `host:port` (127.0.0.0/8, ::1 or localhost). Anything else is refused: dial exists
// only so a forwarded port on this machine can carry the hub's TLS end to end, and a dial
// to another host would let a config file redirect tokens.
func CheckDial(v string) error {
	if v == "" {
		return nil
	}
	if path, ok := strings.CutPrefix(v, UnixDialPrefix); ok {
		if path == "" || strings.ContainsRune(path, 0) {
			return fmt.Errorf("hub.dial: %q has no socket path (want unix:<path>)", v)
		}
		return nil
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return fmt.Errorf("hub.dial: want 127.0.0.1:<port>, unix:<path> or empty, got %q", v)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("hub.dial: bad port in %q", v)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("hub.dial: %q is not a loopback address (a dial must be this machine's own forwarded port)", v)
		}
	}
	return nil
}

// CheckUpdateBase validates update.base_url (machines.md 7.3 step 1): empty (GitHub
// releases), https, or http only for a loopback host (tests).
func CheckUpdateBase(v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return fmt.Errorf("update.base_url: want an https URL or empty, got %q", v)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("update.base_url: http is accepted only for a loopback host, got %q", v)
	}
	return fmt.Errorf("update.base_url: want an https URL or empty, got %q", v)
}
