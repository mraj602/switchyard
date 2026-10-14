package board

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text safety (docs/spec/terminal.md T2). Every string that comes from a record is passed
// through Clean (or Line) before it enters a View, so a renderer may print it raw and a tab
// title may carry it. Nothing here allocates a terminal sequence; it only removes them.

// escSeq matches the escape sequences worth removing whole, so that "\x1b]2;x\x07" does not
// leave "]2;x" behind: OSC (ended by BEL or ST), CSI, and two-byte escapes.
var escSeq = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|\x1b\\[[0-?]*[ -/]*[@-~]|\x1b[ -/]*[0-~]")

// unsafeRune reports runes that never survive Clean: C0 and C1 controls (the caller turns
// the whitespace ones into spaces first), DEL, bidi embeddings, overrides, isolates and
// marks, and invisible format characters that hide text.
func unsafeRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x061c, r == 0x200b, r == 0x200c, r == 0x200d, r == 0x200e, r == 0x200f,
		r >= 0x202a && r <= 0x202e, r == 0x2060, r >= 0x2066 && r <= 0x2069, r == 0xfeff,
		r == 0xfffd:
		return true
	case r >= 0xe0000 && r <= 0xe007f: // tag characters
		return true
	}
	return false
}

// Clean strips terminal escape sequences, control characters, bidi controls and invalid
// UTF-8, and turns every kind of line break and tab into a space. Runs of spaces are kept
// as they are; use Line for the one-line form.
func Clean(s string) string {
	if s == "" {
		return ""
	}
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = escSeq.ReplaceAllString(s, "")
	}
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t', '\v', '\f', 0x85, 0x2028, 0x2029:
			b.WriteByte(' ')
		default:
			if !unsafeRune(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// Line is Clean, collapsed to one line: runs of white space become one space and the ends
// are trimmed.
func Line(s string) string {
	return strings.Join(strings.Fields(Clean(s)), " ")
}

// Width is the number of terminal columns s takes: East Asian wide and fullwidth
// characters and emoji count 2, combining marks 0.
func Width(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20, r >= 0x7f && r < 0xa0:
		return 0
	case r < 0x300:
		return 1
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case isWide(r):
		return 2
	}
	return 1
}

// wide holds the East Asian Wide and Fullwidth ranges (UAX 11) and the emoji blocks that
// terminals draw two columns wide, sorted.
var wide = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0},
	{0x23f3, 0x23f3}, {0x25fd, 0x25fe}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f},
	{0x2693, 0x2693}, {0x26a1, 0x26a1}, {0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5},
	{0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea}, {0x26f2, 0x26f3}, {0x26f5, 0x26f5},
	{0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b}, {0x2728, 0x2728},
	{0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27b0, 0x27b0}, {0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
	{0x2e80, 0x303e}, {0x3041, 0x33ff}, {0x3400, 0x4dbf}, {0x4e00, 0x9fff}, {0xa000, 0xa4cf},
	{0xa960, 0xa97f}, {0xac00, 0xd7a3}, {0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe6f},
	{0xff00, 0xff60}, {0xffe0, 0xffe6}, {0x1f004, 0x1f004}, {0x1f0cf, 0x1f0cf},
	{0x1f18e, 0x1f18e}, {0x1f191, 0x1f19a}, {0x1f200, 0x1f320}, {0x1f32d, 0x1f335},
	{0x1f337, 0x1f37c}, {0x1f37e, 0x1f393}, {0x1f3a0, 0x1f3ca}, {0x1f3cf, 0x1f3d3},
	{0x1f3e0, 0x1f3f0}, {0x1f3f4, 0x1f3f4}, {0x1f3f8, 0x1f43e}, {0x1f440, 0x1f440},
	{0x1f442, 0x1f4fc}, {0x1f4ff, 0x1f53d}, {0x1f54b, 0x1f54e}, {0x1f550, 0x1f567},
	{0x1f57a, 0x1f57a}, {0x1f595, 0x1f596}, {0x1f5a4, 0x1f5a4}, {0x1f5fb, 0x1f64f},
	{0x1f680, 0x1f6c5}, {0x1f6cc, 0x1f6cc}, {0x1f6d0, 0x1f6d2}, {0x1f6d5, 0x1f6d7},
	{0x1f6eb, 0x1f6ec}, {0x1f6f4, 0x1f6fc}, {0x1f7e0, 0x1f7eb}, {0x1f90c, 0x1f93a},
	{0x1f93c, 0x1f945}, {0x1f947, 0x1f9ff}, {0x1fa70, 0x1faff}, {0x20000, 0x2fffd},
	{0x30000, 0x3fffd},
}

func isWide(r rune) bool {
	lo, hi := 0, len(wide)-1
	for lo <= hi {
		m := (lo + hi) / 2
		switch {
		case r < wide[m][0]:
			hi = m - 1
		case r > wide[m][1]:
			lo = m + 1
		default:
			return true
		}
	}
	return false
}

// ellipsis is what Cut appends; it is one column wide.
const ellipsis = "…"

// Cut shortens s to at most width columns, ending in "…" when it had to cut. A wide
// character is never split. width < 1 gives "".
func Cut(s string, width int) string {
	if width < 1 {
		return ""
	}
	if Width(s) <= width {
		return s
	}
	limit := width - 1 // room for the ellipsis
	w := 0
	end := 0
	for i, r := range s {
		rw := runeWidth(r)
		if w+rw > limit {
			break
		}
		w += rw
		end = i + utf8.RuneLen(r)
	}
	return strings.TrimRight(s[:end], " ") + ellipsis
}

// field cleans s to one line and cuts it to width columns: the form of every free-text
// field of a View.
func field(s string, width int) string { return Cut(Line(s), width) }

var fence = regexp.MustCompile("(?s)```.*?(?:```|$)")

// Recap makes a session's last message a one-line recap: code fences removed, white space
// collapsed, cut to width columns.
func Recap(s string, width int) string {
	return field(fence.ReplaceAllString(s, " "), width)
}
