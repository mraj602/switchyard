package pair

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/service"
)

// ErrNoURL is returned when no hub address could be found (pairing.md 2.3, step 4).
var ErrNoURL = errors.New("pass --url https://<your hub address> (the address your phone opens)")

// ErrNeedsHTTPS is returned (wrapped, with the fix for that address) for phone, browser and
// owner links over plain http.
var ErrNeedsHTTPS = errors.New("a phone needs the hub's https address (service workers, install and Web Push need a secure context)")

// needsHTTPS is ErrNeedsHTTPS for the plain http address in use: it says what a phone needs,
// the address that was used, the two ways out (open the board at the hub's https address, or
// set one) and `tailscale serve` only as one option, with the hub's own port (not a fixed
// 8787). A loopback address can never be reached by a phone.
func needsHTTPS(httpURL string) error {
	msg := "but the address in use is " + httpURL + " (the board was opened over it)."
	if u, err := url.Parse(httpURL); err == nil && loopbackHost(u.Hostname()) {
		msg += " A loopback address can never be reached by a phone."
	}
	msg += " Open the board at the hub's https address, or set one: " + service.HTTPSFixFor(httpURL) + "."
	return fmt.Errorf("%w, %s", ErrNeedsHTTPS, msg)
}

// ResolveURL picks the hub's public address in the order of pairing.md 2.3: the explicit
// one (--url, or url in the request), the hub.public_url setting, the origin the request
// came from. It returns ErrNoURL when all are empty.
func ResolveURL(explicit, setting, requestOrigin string) (string, error) {
	for _, u := range []string{explicit, setting, requestOrigin} {
		if strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u), nil
		}
	}
	return "", ErrNoURL
}

// RequestOrigin is the origin the request was made to: the scheme (https when the request
// came over TLS, or through a loopback proxy that says X-Forwarded-Proto: https) and the
// Host header.
func RequestOrigin(r *http.Request) string {
	scheme := "http"
	if auth.SecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// NormalizeURL checks a hub address and returns its origin (scheme://host[:port], no
// trailing slash). Phone and browser codes need https (a secure context); insecure allows
// http anyway, and the caller should warn that the app then cannot install or receive
// pushes. Owner links also accept http on a loopback address (a browser treats it as a
// secure context). machine and desktop codes accept either scheme: sy connect applies its
// own rule (tokens.md 4).
func NormalizeURL(raw, kind string, insecure bool) (origin string, plainHTTP bool, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false, fmt.Errorf("%q is not an http(s) address like https://hub.example.ts.net", raw)
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", false, fmt.Errorf("%q must be an origin only (scheme, host, optional port)", raw)
	}
	origin = u.Scheme + "://" + u.Host
	if u.Scheme == "https" {
		return origin, false, nil
	}
	switch kind {
	case "phone", "browser":
		if !insecure {
			return "", false, needsHTTPS(origin)
		}
	case "owner":
		if !insecure && !loopbackHost(u.Hostname()) {
			return "", false, needsHTTPS(origin)
		}
	}
	return origin, kind == "phone" || kind == "browser", nil
}

func loopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// PairURL is the QR content: <origin>/#/pair?s=<secret>&c=<digits> (pairing.md 2.2). The
// fragment never reaches the server.
func PairURL(origin, secret, digits string) string {
	return origin + "/#/pair?s=" + url.QueryEscape(secret) + "&c=" + digits
}

// PairURLKeys is PairURL with keys.md 7's two fields: h=<hub key fingerprint> and
// k=<b64u seal key>.
func PairURLKeys(origin, secret, digits, hubFP, sealKey string) string {
	return PairURL(origin, secret, digits) + "&h=" + hubFP + "&k=" + sealKey
}

// OwnerURL is the owner sign-in link: <origin>/#/owner?s=<secret>.
func OwnerURL(origin, secret string) string {
	return origin + "/#/owner?s=" + url.QueryEscape(secret)
}
