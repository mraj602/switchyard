package tui

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/mraj602/watchyard/internal/termenv"
)

// Mode is how much styling a frame carries (docs/spec/terminal.md T4).
type Mode int

const (
	// ModePlain writes no escape codes at all (a pipe, TERM=dumb).
	ModePlain Mode = iota
	// ModeMono is NO_COLOR: bold and dim stay, colours go.
	ModeMono
	// Mode256 maps account colours to the nearest of the 256-colour palette.
	Mode256
	// ModeTrue writes 24-bit account colours (COLORTERM=truecolor|24bit).
	ModeTrue
)

// DetectMode picks the styling for a terminal from the environment: NO_COLOR (any
// non-empty value) gives mono, TERM=dumb or not a terminal plain, COLORTERM=truecolor|24bit
// 24-bit colour, anything else 256 colours.
func DetectMode(env termenv.Env, isTTY bool) Mode {
	if !isTTY || env.Lookup("TERM") == "dumb" {
		return ModePlain
	}
	if env.Lookup("NO_COLOR") != "" {
		return ModeMono
	}
	switch strings.ToLower(env.Lookup("COLORTERM")) {
	case "truecolor", "24bit":
		return ModeTrue
	}
	return Mode256
}

// DetectASCII is true when the locale is not UTF-8 (LC_ALL, then LC_CTYPE, then LANG; none
// set means UTF-8, and Windows is always UTF-8 here).
func DetectASCII(env termenv.Env) bool {
	if env.GOOS == "windows" {
		return false
	}
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := env.Lookup(k); v != "" {
			l := strings.ToLower(v)
			return !(strings.Contains(l, "utf-8") || strings.Contains(l, "utf8"))
		}
	}
	return false
}

// sty is the style of one span. fg is "" (default), a basic colour name, or "#rrggbb".
type sty struct {
	fg   string
	bold bool
	dim  bool
}

type span struct {
	text string
	sty  sty
}

type line []span

var basicFG = map[string]int{
	"red": 31, "green": 32, "yellow": 33, "blue": 34, "magenta": 35, "cyan": 36, "grey": 90,
}

func (s sty) sgr(m Mode) string {
	if m == ModePlain {
		return ""
	}
	var p []string
	if s.bold {
		p = append(p, "1")
	}
	if s.dim {
		p = append(p, "2")
	}
	if s.fg != "" && m != ModeMono {
		if strings.HasPrefix(s.fg, "#") {
			if r, g, b, ok := parseHex(s.fg); ok {
				if m == ModeTrue {
					p = append(p, "38;2;"+strconv.Itoa(r)+";"+strconv.Itoa(g)+";"+strconv.Itoa(b))
				} else {
					p = append(p, "38;5;"+strconv.Itoa(nearest256(r, g, b)))
				}
			}
		} else if c, ok := basicFG[s.fg]; ok {
			p = append(p, strconv.Itoa(c))
		}
	}
	if len(p) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(p, ";") + "m"
}

func parseHex(h string) (r, g, b int, ok bool) {
	if len(h) != 7 || h[0] != '#' {
		return
	}
	v, err := strconv.ParseUint(h[1:], 16, 32)
	if err != nil {
		return
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}

// nearest256 maps an RGB colour to the nearest of the xterm 6x6x6 cube or 24 greys.
func nearest256(r, g, b int) int {
	lv := [6]int{0, 95, 135, 175, 215, 255}
	idx := func(c int) int {
		best, bd := 0, 1<<30
		for i, l := range lv {
			if d := (c - l) * (c - l); d < bd {
				best, bd = i, d
			}
		}
		return best
	}
	ri, gi, bi := idx(r), idx(g), idx(b)
	cube := 16 + 36*ri + 6*gi + bi
	cd := sq(r-lv[ri]) + sq(g-lv[gi]) + sq(b-lv[bi])
	avg := (r + g + b) / 3
	g2 := (avg - 8 + 5) / 10
	if g2 < 0 {
		g2 = 0
	}
	if g2 > 23 {
		g2 = 23
	}
	gv := 8 + 10*g2
	if gd := sq(r-gv) + sq(g-gv) + sq(b-gv); gd < cd {
		return 232 + g2
	}
	return cube
}

func sq(x int) int { return x * x }

// runeWidth is the display width of r: 0 for controls and combining marks, 2 for East Asian
// wide and emoji, else 1.
func runeWidth(r rune) int {
	switch {
	case r == 0 || r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200b || r == 0x200d:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe6f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f300 && r <= 0x1f64f, r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

func strWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func (l line) width() int {
	w := 0
	for _, s := range l {
		w += strWidth(s.text)
	}
	return w
}

// fit cuts the line to at most w columns, ending in an ellipsis when it cut.
func (l line) fit(w int, ascii bool) line {
	if w <= 0 {
		return nil
	}
	if l.width() <= w {
		return l
	}
	ell := "…"
	if ascii {
		ell = "~"
	}
	room := w - 1
	var out line
	for _, s := range l {
		var b strings.Builder
		for _, r := range s.text {
			rw := runeWidth(r)
			if rw > room {
				if b.Len() > 0 {
					out = append(out, span{b.String(), s.sty})
				}
				return append(out, span{ell, s.sty})
			}
			room -= rw
			b.WriteRune(r)
		}
		out = append(out, span{b.String(), s.sty})
	}
	return out
}

// render writes the line with its escape codes for mode m.
func (l line) render(m Mode) string {
	var b strings.Builder
	for _, s := range l {
		if s.text == "" {
			continue
		}
		if c := s.sty.sgr(m); c != "" {
			b.WriteString(c)
			b.WriteString(s.text)
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString(s.text)
		}
	}
	return b.String()
}

func padLeft(s string, w int) string {
	if n := w - strWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

func padRight(s string, w int) string {
	if n := w - strWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// cutWidth truncates a plain string to w columns with an ellipsis.
func cutWidth(s string, w int, ascii bool) string {
	return line{{text: s}}.fit(w, ascii).render(ModePlain)
}
