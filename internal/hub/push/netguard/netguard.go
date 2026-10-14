// Package netguard is the outbound dial guard shared by the push senders (push.md 6.6): a
// client that refuses to connect to non-public addresses whatever DNS says, and follows no
// redirect. It is a leaf (standard library only).
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlocked is wrapped by the dial error when an address is not public.
var ErrBlocked = errors.New("refusing to connect to a non-public address")

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT, where tailnets live
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved (and 255.255.255.255)
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64: embeds an IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
}

// PublicIP reports whether ip is an address a remote service may live at: not loopback,
// private, link-local, unspecified, multicast, carrier-grade NAT or another reserved range.
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Control is a net.Dialer.Control that runs after DNS resolution, on the address that is
// about to be dialled.
func Control(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !PublicIP(ip) {
		return fmt.Errorf("%w (%s)", ErrBlocked, host)
	}
	return nil
}

// NewClient is an HTTP client with a 10 s timeout, no redirects, no proxy and a dialer that
// refuses non-public addresses.
func NewClient() *http.Client { return newClient(Control) }

// NewOpenClient is the same client without the address check, for servers the owner chose
// (a self-hosted ntfy on the tailnet or loopback). It still follows no redirect.
func NewOpenClient() *http.Client { return newClient(nil) }

func newClient(ctl func(string, string, syscall.RawConn) error) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: ctl}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
