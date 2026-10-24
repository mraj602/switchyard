package webpush

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/netguard"
)

// Endpoint allow-list and dial guard (push.md 6.6). A push-scoped token chooses the URL the
// hub POSTs to, so the URL is checked when it is registered and again when it is used, and
// the HTTP client refuses to connect to non-public addresses whatever DNS says.

// CheckEndpoint validates a subscription endpoint: https only, port 443 (or none), no
// credentials, and a host that matches one of hosts (nil: model.DefaultWebPushHosts). A
// pattern "*.example.com" matches any subdomain but not example.com itself.
func CheckEndpoint(endpoint string, hosts []string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return errors.New("endpoint is not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("endpoint must be https")
	}
	if u.User != nil {
		return errors.New("endpoint must not carry credentials")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("endpoint must use port 443")
	}
	if hosts == nil {
		hosts = model.DefaultWebPushHosts
	}
	if !HostAllowed(u.Hostname(), hosts) {
		return errors.New("unknown push service host; the owner can add it to webpush_hosts")
	}
	return nil
}

// HostAllowed reports whether host matches a pattern of the allow-list (case-insensitive).
func HostAllowed(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		switch {
		case p == "":
		case strings.HasPrefix(p, "*."):
			if suf := p[1:]; len(host) > len(suf) && strings.HasSuffix(host, suf) {
				return true
			}
		case p == host:
			return true
		}
	}
	return false
}

var errBlockedAddr = netguard.ErrBlocked

// NewGuardedClient is the HTTP client the sender uses: 10 s timeout, no redirects, no
// proxy, and a dialer that refuses non-public addresses (see netguard).
func NewGuardedClient() *http.Client { return netguard.NewClient() }
