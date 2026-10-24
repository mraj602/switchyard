package termctl

import (
	"regexp"
	"strings"

	"github.com/mraj602/switchyard/internal/brand"
)

// The allow-list of docs/spec/terminal.md T7. Patterns are matched on the raw string with
// no decoding, so `%2f`, upper-case hex, a trailing space or a newline never match. This
// is the only place a new terminal is added.
var (
	focusRe     = regexp.MustCompile(`^(warp|warppreview)://session/[0-9a-f]{32}$`)
	tabConfigRe = regexp.MustCompile(`^(?:warp|warppreview)://tab_config/([a-z0-9-]+)$`)
	remoteRe    = regexp.MustCompile(`^https://claude\.ai/[\x21-\x7e]*$`)
)

const maxURL = 128

// ValidFocusURL reports whether u is a Warp tab focus URL.
func ValidFocusURL(u string) bool {
	return len(u) <= maxURL && focusRe.MatchString(u)
}

// ValidTabConfigURL reports whether u opens a tab config file sy wrote
// (`warp://tab_config/switchyard-<label>`, or the prefix from before a rename).
func ValidTabConfigURL(u string) bool {
	m := tabConfigRe.FindStringSubmatch(u)
	if len(u) > maxURL || m == nil {
		return false
	}
	if strings.HasPrefix(m[1], brand.Slug+"-") && len(m[1]) > len(brand.Slug)+1 {
		return true
	}
	return brand.Renamed() && strings.HasPrefix(m[1], brand.OldSlug()+"-") && len(m[1]) > len(brand.OldSlug())+1
}

// ValidRemoteURL reports whether u is a claude.ai link (a session's remote_url).
func ValidRemoteURL(u string) bool {
	return len(u) <= 2048 && remoteRe.MatchString(u)
}

// Allowed is true for every URL Open will pass to the OS.
func Allowed(u string) bool {
	return ValidFocusURL(u) || ValidTabConfigURL(u) || ValidRemoteURL(u)
}
