package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/mraj602/switchyard/internal/config"
)

// Forward dialing (machines.md 3.3): with hub.dial set, the client opens its connection to
// that address (a loopback port or a Unix socket that an SSH RemoteForward carries to the
// hub) but keeps the URL for everything else: the TLS server name and certificate check and
// the Host header all name the real hub, so TLS stays end to end and the SSH channel only
// carries bytes.

// DialTransport returns an http.Transport that connects to dial instead of the URL's host.
// dial is "" (a plain clone of the default transport), a loopback host:port, or unix:<path>;
// anything else is refused (config.CheckDial).
func DialTransport(dial string) (*http.Transport, error) {
	if err := config.CheckDial(dial); err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	if dial == "" {
		return t, nil
	}
	t.Proxy = nil // the forward is local; a proxy variable must not divert it
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if path, ok := strings.CutPrefix(dial, config.UnixDialPrefix); ok {
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := d.DialContext(ctx, "unix", path)
			if err != nil && errors.Is(err, syscall.ECONNREFUSED) {
				removeStaleSocket(path)
			}
			return c, err
		}
		return t, nil
	}
	t.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			network = "tcp"
		}
		return d.DialContext(ctx, network, dial)
	}
	return t, nil
}

// HTTPClient is an http.Client with the given timeout that dials through hub.dial.
func HTTPClient(dial string, timeout time.Duration) (*http.Client, error) {
	t, err := DialTransport(dial)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: timeout, Transport: t}, nil
}

// ForConfig returns a client for hub.url with token, dialing through hub.dial when set. The
// HTTP client has the default 20 s timeout.
func ForConfig(cfg config.Config, token string) (*Client, error) {
	c := New(cfg.Hub.URL, token)
	if cfg.Hub.Dial != "" {
		hc, err := HTTPClient(cfg.Hub.Dial, 20*time.Second)
		if err != nil {
			return nil, err
		}
		c.HTTP = hc
	}
	return c, nil
}

// removeStaleSocket deletes a forward socket that refuses connections, when it is a socket
// owned by this user: sshd binds a RemoteForward socket only if the path is free (unless its
// StreamLocalBindUnlink is yes), so a socket left by a dropped connection would block the
// next forward. This attempt still fails; the next one, after the next SSH connection, works.
func removeStaleSocket(path string) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || !ownedByMe(fi) {
		return
	}
	if err := os.Remove(path); err == nil {
		fmt.Fprintf(os.Stderr, "sy: removed the stale hub socket %s (the next SSH connection can forward it again)\n", path)
	}
}
