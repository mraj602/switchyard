package client

import (
	"net"
	"net/netip"
	"strings"
)

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isTailscale(host string) bool {
	h := strings.ToLower(host)
	if strings.HasSuffix(h, ".ts.net") {
		return true
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return cgnat.Contains(ip)
	}
	// A single-label name is a MagicDNS short name (or a LAN name; --insecure-lan is the
	// honest flag for that, but short names are by far the Tailscale case).
	return h != "" && !strings.Contains(h, ".") && net.ParseIP(h) == nil
}
