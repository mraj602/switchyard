package peers

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTextBytes is the longest message text after cleaning (peers.md 9.1).
const MaxTextBytes = 64 * 1024

// CleanText applies the cleaning of peers.md 9.1: `\r\n` becomes `\n`; NUL, C0 and C1
// control characters other than `\n` and `\t` are removed; Unicode bidi controls
// (U+202A-U+202E, U+2066-U+2069) and U+200B-U+200F are removed.
func CleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r):
			return -1
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r >= 0x200B && r <= 0x200F:
			return -1
		}
		return r
	}, s)
}

// secretPatterns is the small fixed list of peers.md 9.1: a seatbelt, not a DLP system. The
// names are what a refusal says was found, never the match itself.
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"a PEM private key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"a Watchyard token", regexp.MustCompile(`\bsy_[mdpbo]_[0-9A-Za-z]{16,}`)},
	{"an Anthropic API key", regexp.MustCompile(`sk-ant-[0-9A-Za-z_-]{8,}`)},
	{"an AWS access key id", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"a GitHub token", regexp.MustCompile(`\b(?:ghp_[0-9A-Za-z]{20,}|github_pat_[0-9A-Za-z_]{20,})`)},
	{"a Slack token", regexp.MustCompile(`\bxox[abprs]-[0-9A-Za-z-]{8,}`)},
}

// FindSecret returns what kind of secret s appears to hold, or "".
func FindSecret(s string) string {
	for _, p := range secretPatterns {
		if p.re.MatchString(s) {
			return p.name
		}
	}
	return ""
}

// CheckText cleans text and applies the content rules of peers.md 9.1. It returns the cleaned
// text, or a Refusal: not_text (invalid UTF-8, or empty after cleaning), too_big, secret.
func CheckText(text string) (string, *Refusal) {
	if !utf8.ValidString(text) {
		return "", Refuse(CodeNotText, "the text is not valid UTF-8")
	}
	clean := CleanText(text)
	if strings.TrimSpace(clean) == "" {
		return "", Refuse(CodeNotText, "the text is empty")
	}
	if len(clean) > MaxTextBytes {
		return "", Refuse(CodeTooBig, "the text is %d bytes; the limit is %d", len(clean), MaxTextBytes)
	}
	if kind := FindSecret(clean); kind != "" {
		return "", Refuse(CodeSecret, "the text looks like it holds %s", kind)
	}
	return clean, nil
}
