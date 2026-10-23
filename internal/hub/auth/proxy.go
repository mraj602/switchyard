package auth

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

// trustedProxies are the ranges forwarded headers (X-Forwarded-For, X-Forwarded-Proto) are
// believed from, besides loopback (--trusted-proxy). Empty by default.
var trustedProxies atomic.Pointer[[]*net.IPNet]

// ParseTrustedProxies parses a comma-separated list of CIDRs or single addresses.
func ParseTrustedProxies(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			ip := net.ParseIP(p)
			if ip == nil {
				return nil, fmt.Errorf("%q is not an address or CIDR range", p)
			}
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			p += fmt.Sprintf("/%d", bits)
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address or CIDR range", p)
		}
		out = append(out, n)
	}
	return out, nil
}

// SetTrustedProxies sets the ranges forwarded headers are trusted from (loopback always is).
func SetTrustedProxies(n []*net.IPNet) { trustedProxies.Store(&n) }

// fromTrustedProxy reports whether the connection's peer is loopback or a configured proxy.
func fromTrustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if p := trustedProxies.Load(); p != nil {
		for _, n := range *p {
			if n.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// TrustedProxy is fromTrustedProxy for other packages (scheme detection).
func TrustedProxy(r *http.Request) bool { return fromTrustedProxy(r) }
