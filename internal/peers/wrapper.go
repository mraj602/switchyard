package peers

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The inbox contract with Claude Code 2.1.300 (peers.md 8.1, 8.5). Read from the installed
// program; every item here is **uncertain** until the PE0 lab confirms it (E3, E4, E10), and
// the golden files under testdata/ are the one place that should change with it.
const (
	// WrapperTag is the element name Claude Code wraps a peer message in.
	WrapperTag = "cross-session-message"
	// MaxHopTokens is how many hop-chain tokens Claude Code keeps (the last ones).
	MaxHopTokens = 32
	// HopTokenLen is the length of one hop token in hex characters.
	HopTokenLen = 24
	// MaxFromLen is the longest `from` address Claude Code accepts, "uds:" included.
	MaxFromLen = 300
	// MaxNameRunes is where from-name is cut (Claude Code's own cleaner adds "…" after it).
	MaxNameRunes = 64
	// FrameMsgV is the msgV of a frame.
	FrameMsgV = 1
	// FramePriority is the priority of every frame Switchyard writes.
	FramePriority = "next"
)

// Mode is the permission class a wrapper asserts (from-mode).
type Mode string

const (
	ModeBypass    Mode = "bypass"
	ModePrompting Mode = "prompting"
)

var (
	fromRe        = regexp.MustCompile(`^[A-Za-z0-9%:_/.\\-]{1,300}$`)
	fromSessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	hopTokenRe    = regexp.MustCompile(`^[0-9a-f]{24}$`)
	hopChainRe    = regexp.MustCompile(`^[0-9a-f]{24}(?:,[0-9a-f]{24}){0,31}$`)
	// wrapperRe is Claude Code's own parse: optional attributes in this order, then the body.
	wrapperRe = regexp.MustCompile(`^<` + WrapperTag +
		`(?: from="([A-Za-z0-9%:_/.\\-]+)")?` +
		`(?: from-session="([A-Za-z0-9_-]{1,80})")?` +
		`(?: hop-chain="([0-9a-f]{24}(?:,[0-9a-f]{24}){0,31})")?` +
		`(?: from-name="([^"<>\n\r]+)")?` +
		`(?: from-mode="(bypass|prompting)")?` +
		`(?: from-plugin="([^"<>\n\r]+)")?` +
		`>\n([\s\S]*)\n</` + WrapperTag + `>$`)
)

// Wrapper holds the attributes of one <cross-session-message> element. Empty fields are
// omitted. Attribute order is fixed: from, from-session, hop-chain, from-name, from-mode,
// from-plugin.
type Wrapper struct {
	From        string // already "uds:" + encoded address (UDSAddress)
	FromSession string
	Hop         []string
	FromName    string
	FromMode    Mode
	Plugin      string // Switchyard never sets it (peers.md 8.5)
	Body        string // the text inside the element, before escaping
}

// UDSAddress is `uds:` + path with every byte outside A-Za-z0-9:_/.\- percent-encoded as
// Claude Code does (uppercase hex, per UTF-8 byte). It fails when the result is longer than
// MaxFromLen.
func UDSAddress(path string) (string, error) {
	var b strings.Builder
	b.WriteString("uds:")
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == ':' || c == '_' || c == '/' || c == '.' || c == '\\' || c == '-' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	if b.Len() > MaxFromLen {
		return "", fmt.Errorf("reply address is %d characters, more than %d", b.Len(), MaxFromLen)
	}
	return b.String(), nil
}

// CleanName is Claude Code's name cleaner: `"<>` removed, then every Unicode format, control,
// surrogate and line/paragraph-separator character removed, trimmed, and cut to MaxNameRunes
// code points (Claude Code appends "…" to a cut name, and so does this).
func CleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '"' || r == '<' || r == '>':
			return -1
		case unicode.In(r, unicode.Cf, unicode.Cc, unicode.Cs, unicode.Zl, unicode.Zp):
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxNameRunes {
		r := []rune(s)
		return string(r[:MaxNameRunes]) + "…"
	}
	return s
}

// closeTagRe finds a closing-tag lookalike: an opener ("<", fullwidth or small less-than),
// optional invisible filler, a slash, filler, then the tag name in any case with format
// characters allowed between letters. **Uncertain**: Claude Code's real pattern is larger
// (lab E10 settles it); this covers case, zero-width characters and the lookalike openers.
var closeTagRe = func() *regexp.Regexp {
	const fill = `[\p{Cf}\s]*`
	var name strings.Builder
	for i, r := range WrapperTag {
		if i > 0 {
			name.WriteString(`[\p{Cf}]*`)
		}
		name.WriteString(regexp.QuoteMeta(string(r)))
	}
	return regexp.MustCompile(`(?i)[<\x{FF1C}\x{FE64}]` + fill + `[/\x{FF0F}\x{2044}]` + fill + name.String())
}()

// EscapeBody rewrites each closing-tag lookalike in body so Claude Code cannot see the
// element end early: the opener becomes `<\`, so `</cross-session-message` is
// `<\/cross-session-message`.
func EscapeBody(body string) string {
	return closeTagRe.ReplaceAllStringFunc(body, func(m string) string {
		_, n := utf8.DecodeRuneInString(m)
		return `<\` + m[n:]
	})
}

// String renders the element exactly as Claude Code's builder does. An attribute that would
// not pass Claude Code's own pattern is left out (the element would otherwise be plain
// text to it, which in a bypass receiver means a hold); Valid tells whether the result is
// canonical.
func (w Wrapper) String() string {
	return "<" + WrapperTag + w.attrs() + ">\n" + EscapeBody(w.Body) + "\n</" + WrapperTag + ">"
}

func (w Wrapper) attrs() string {
	var a []string
	if w.From != "" && fromRe.MatchString(w.From) {
		a = append(a, `from="`+w.From+`"`)
	}
	if w.FromSession != "" && fromSessionRe.MatchString(w.FromSession) {
		a = append(a, `from-session="`+w.FromSession+`"`)
	}
	if len(w.Hop) > 0 {
		if chain := strings.Join(w.Hop, ","); hopChainRe.MatchString(chain) {
			a = append(a, `hop-chain="`+chain+`"`)
		}
	}
	if n := CleanName(w.FromName); n != "" {
		a = append(a, `from-name="`+n+`"`)
	}
	// The mode of a remote message comes from the envelope: anything but the two classes
	// would be written into the element verbatim.
	if w.FromMode == ModeBypass || w.FromMode == ModePrompting {
		a = append(a, `from-mode="`+string(w.FromMode)+`"`)
	}
	if w.Plugin != "" {
		if p := CleanName(w.Plugin); p != "" {
			a = append(a, `from-plugin="`+p+`"`)
		}
	}
	if len(a) == 0 {
		return ""
	}
	return " " + strings.Join(a, " ")
}

// Valid reports whether Claude Code will read the element as canonical: parsing String()
// and rebuilding gives the same bytes.
func (w Wrapper) Valid() bool {
	_, ok := ParseWrapper(w.String())
	return ok
}

// ParseWrapper is Claude Code's canonical check: the text must match the element pattern and
// rebuilding it from the parts must give the same bytes. An escaped closing tag in the body
// stays escaped.
func ParseWrapper(s string) (Wrapper, bool) {
	m := wrapperRe.FindStringSubmatch(s)
	if m == nil {
		return Wrapper{}, false
	}
	w := Wrapper{From: m[1], FromSession: m[2], FromName: m[4], FromMode: Mode(m[5]), Plugin: m[6], Body: m[7]}
	if m[3] != "" {
		w.Hop = strings.Split(m[3], ",")
	}
	if w.String() != s {
		return Wrapper{}, false
	}
	return w, true
}

// CutHop drops tokens that are not 24 hex characters and keeps the last MaxHopTokens.
func CutHop(tokens []string) []string {
	var out []string
	for _, t := range tokens {
		if hopTokenRe.MatchString(t) {
			out = append(out, t)
		}
	}
	if len(out) > MaxHopTokens {
		out = out[len(out)-MaxHopTokens:]
	}
	return out
}
