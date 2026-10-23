package client

import (
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
	// A single-label name is not accepted: it could be any LAN name, which the system's search
	// domains may even send to the wrong network. Use the full <name>.<tailnet>.ts.net name, or
	// --insecure-lan.
	return false
}
