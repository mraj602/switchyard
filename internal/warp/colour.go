package warp

import (
	"math"
	"strconv"
	"strings"
)

// Colour is one of the six chromatic colour names a Warp tab config takes, with the sRGB
// value it is matched against. `black` and `white` are never used: they read badly on dark
// and light themes (docs/spec/terminal.md T8).
type Colour struct {
	Name string
	Hex  string
}

// Colours are the names in Warp's order. The reference values are mid-lightness, saturated
// hues (the same family as the setup palette), so a seat colour maps by hue first.
var Colours = []Colour{
	{"red", "#ef4444"},
	{"green", "#22c55e"},
	{"yellow", "#eab308"},
	{"blue", "#3b82f6"},
	{"magenta", "#a855f7"},
	{"cyan", "#06b6d4"},
}

// Nearest returns the colour name nearest to hex ("#rrggbb") by OKLab distance. An invalid
// hex gives "".
func Nearest(hex string) string {
	l, a, b, ok := oklabHex(hex)
	if !ok {
		return ""
	}
	best, bestD := "", math.Inf(1)
	for _, c := range Colours {
		cl, ca, cb, _ := oklabHex(c.Hex)
		d := (l-cl)*(l-cl) + (a-ca)*(a-ca) + (b-cb)*(b-cb)
		if d < bestD {
			best, bestD = c.Name, d
		}
	}
	return best
}

// ValidName reports whether n is one of the six names.
func ValidName(n string) bool {
	for _, c := range Colours {
		if c.Name == n {
			return true
		}
	}
	return false
}

func oklabHex(hex string) (l, a, b float64, ok bool) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	r := linear(float64(v>>16&0xff) / 255)
	g := linear(float64(v>>8&0xff) / 255)
	bl := linear(float64(v&0xff) / 255)
	// Björn Ottosson's OKLab (2020), from linear sRGB.
	lm := 0.4122214708*r + 0.5363325363*g + 0.0514459929*bl
	mm := 0.2119034982*r + 0.6806995451*g + 0.1073969566*bl
	sm := 0.0883024619*r + 0.2817188376*g + 0.6299787005*bl
	l_, m_, s_ := math.Cbrt(lm), math.Cbrt(mm), math.Cbrt(sm)
	l = 0.2104542553*l_ + 0.7936177850*m_ - 0.0040720468*s_
	a = 1.9779984951*l_ - 2.4285922050*m_ + 0.4505937099*s_
	b = 0.0259040371*l_ + 0.7827717662*m_ - 0.8086757660*s_
	return l, a, b, true
}

func linear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}
