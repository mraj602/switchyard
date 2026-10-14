package netguard_test

import (
	"net/netip"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/push/netguard"
)

func TestPublicIP(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !netguard.PublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be public", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1",
		"100.127.255.255", "0.0.0.0", "0.1.2.3", "198.18.0.1", "198.19.255.255", "240.0.0.1", "255.255.255.255",
		"64:ff9b::7f00:1", "64:ff9b::808:808", "224.0.0.1", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "::"} {
		if netguard.PublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
}
