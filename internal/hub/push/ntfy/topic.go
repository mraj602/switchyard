package ntfy

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/mraj602/switchyard/internal/hub/push/netguard"
)

// DefaultServer is the public ntfy server.
const DefaultServer = "https://ntfy.sh"

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewTopic makes a topic name: "sy-" + 24 base62 characters, about 143 bits (push.md 7.1).
// On ntfy.sh the name is the only secret, so it comes from crypto/rand without modulo bias.
func NewTopic() (string, error) {
	const n = 24
	out := make([]byte, 0, n)
	buf := make([]byte, 64)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b < 248 && len(out) < n { // 248 = 4*62: reject the biased tail
				out = append(out, base62[int(b)%62])
			}
		}
	}
	return "sy-" + string(out), nil
}

var topicRE = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

// ValidTopic reports whether s is an ntfy topic name (api.yaml PushTargetNew.topic).
func ValidTopic(s string) bool { return topicRE.MatchString(s) }

// CheckServer validates an ntfy server URL and returns it normalised (no trailing slash).
// Empty means DefaultServer. The owner may point at their own server: https anywhere, or
// plain http for loopback, private, Tailscale (*.ts.net, 100.64.0.0/10) and single-label
// MagicDNS hosts, the rule of `sy connect` (tokens.md 4). A push-scoped caller (owner false)
// gets https to a public DNS name or public address only; the sender's dial guard enforces
// the same at connect time (push.md 7.4).
func CheckServer(raw string, owner bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultServer, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%q is not a server URL (want http(s)://host[:port])", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the ntfy server URL has no credentials, query or fragment")
	}
	if owner {
		if u.Scheme == "http" && !plainHTTPOK(u.Hostname()) {
			return "", errors.New("plain http is only allowed for loopback, private and tailnet servers; use https")
		}
	} else if u.Scheme != "https" || !publicHost(u.Hostname()) {
		return "", errors.New("a device may only use an https ntfy server on a public address; the owner can register private servers")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

func plainHTTPOK(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".ts.net") {
		return true
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return ip.IsLoopback() || ip.IsPrivate() || !netguard.PublicIP(ip)
	}
	return h != "" && !strings.Contains(h, ".") // MagicDNS short name
}

// publicHost reports whether host could be a public server: a dotted DNS name that is not a
// local suffix, or a public IP literal.
func publicHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if ip, err := netip.ParseAddr(h); err == nil {
		return netguard.PublicIP(ip)
	}
	if !strings.Contains(h, ".") {
		return false
	}
	for _, suf := range []string{".localhost", ".local", ".internal", ".lan", ".home", ".corp", ".ts.net", ".home.arpa"} {
		if strings.HasSuffix(h, suf) {
			return false
		}
	}
	return true
}

// JoinEndpoint is push_target.endpoint for an ntfy target: server + "/" + topic.
func JoinEndpoint(server, topic string) string { return strings.TrimRight(server, "/") + "/" + topic }

// SplitEndpoint is the inverse of JoinEndpoint.
func SplitEndpoint(endpoint string) (server, topic string) {
	i := strings.LastIndex(endpoint, "/")
	if i < 0 {
		return "", endpoint
	}
	return endpoint[:i], endpoint[i+1:]
}

// IsPublicServer reports whether server is ntfy.sh, where topics are readable by anyone who
// knows the name (so private text is the default, push.md 7.1).
func IsPublicServer(server string) bool {
	u, err := url.Parse(server)
	return err == nil && strings.EqualFold(u.Hostname(), "ntfy.sh")
}
