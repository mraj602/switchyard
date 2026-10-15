package service

import (
	"net"
	"net/url"
	"strings"
)

// ServeHint is the `tailscale serve` command that puts https in front of a hub reached at
// addr: a listen address ("127.0.0.1:8787") or the hub's http URL ("http://127.0.0.1:19200").
// A loopback or wildcard address gives the short form with the port ("tailscale serve --bg
// 19200"); another host gives the full upstream ("tailscale serve --bg http://10.0.0.5:9000").
// An address that does not parse falls back to DefaultListen's port.
func ServeHint(addr string) string {
	host, port := splitAddr(addr)
	if port == "" {
		_, port = splitAddr(DefaultListen)
	}
	switch h := strings.Trim(host, "[]"); {
	case h == "", h == "0.0.0.0", h == "::", strings.EqualFold(h, "localhost"):
		return "tailscale serve --bg " + port
	default:
		if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
			return "tailscale serve --bg " + port
		}
		return "tailscale serve --bg http://" + net.JoinHostPort(h, port)
	}
}

// ServeHintFor is ServeHint for the hub's own http URL as a browser or phone reaches it
// (maybe by a LAN or tailnet name): the command runs on the hub, so only the port counts.
func ServeHintFor(hubURL string) string {
	_, port := splitAddr(hubURL)
	if port == "" {
		return ServeHint("")
	}
	return ServeHint(net.JoinHostPort("127.0.0.1", port))
}

// splitAddr is the host and port of a listen address or an http(s) URL ("" when unknown).
func splitAddr(addr string) (host, port string) {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" {
			return "", ""
		}
		port = u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		return u.Hostname(), port
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", ""
	}
	return h, p
}

// ListenOf is the --listen (or --addr) value of `sy hub serve` arguments, "" when absent.
func ListenOf(args []string) string {
	for i, a := range args {
		for _, f := range []string{"--listen", "-listen", "--addr", "-addr"} {
			if a == f && i+1 < len(args) {
				return args[i+1]
			}
			if v, ok := strings.CutPrefix(a, f+"="); ok {
				return v
			}
		}
	}
	return ""
}

// InstalledListen is the address the installed hub service listens on: its unit's --listen,
// else DefaultListen (also when no unit can be read).
func InstalledListen(e Env) string {
	if args, _, ok := InstalledUnit(e, DefaultName); ok {
		if l := ListenOf(args); l != "" {
			return l
		}
	}
	return DefaultListen
}
