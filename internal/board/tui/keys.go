package tui

import "unicode/utf8"

type keyKind int

const (
	kRune keyKind = iota
	kUp
	kDown
	kPgUp
	kPgDn
	kHome
	kEnd
	kEnter
	kEsc
	kBackspace
	kCtrlC
)

type key struct {
	kind keyKind
	r    rune
}

// parseKeys turns the bytes of one terminal read into keys: arrows (CSI and SS3), Home/End,
// PgUp/PgDn, Enter, Esc, Backspace, Ctrl-C and printable runes. Other escape sequences are
// dropped whole, so a mouse report or a function key never types into the search box.
func parseKeys(b []byte) []key {
	var out []key
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			if i+1 >= len(b) || (b[i+1] != '[' && b[i+1] != 'O') {
				out = append(out, key{kind: kEsc})
				i++
				continue
			}
			j := i + 2
			for j < len(b) && !(b[j] >= 0x40 && b[j] <= 0x7e) {
				j++
			}
			if j >= len(b) {
				i = len(b)
				continue
			}
			params := string(b[i+2 : j])
			switch b[j] {
			case 'A':
				out = append(out, key{kind: kUp})
			case 'B':
				out = append(out, key{kind: kDown})
			case 'H':
				out = append(out, key{kind: kHome})
			case 'F':
				out = append(out, key{kind: kEnd})
			case '~':
				switch params {
				case "5":
					out = append(out, key{kind: kPgUp})
				case "6":
					out = append(out, key{kind: kPgDn})
				case "1", "7":
					out = append(out, key{kind: kHome})
				case "4", "8":
					out = append(out, key{kind: kEnd})
				}
			}
			i = j + 1
		case c == '\r' || c == '\n':
			out = append(out, key{kind: kEnter})
			i++
		case c == 0x7f || c == 0x08:
			out = append(out, key{kind: kBackspace})
			i++
		case c == 0x03:
			out = append(out, key{kind: kCtrlC})
			i++
		case c < 0x20:
			i++
		default:
			r, n := utf8.DecodeRune(b[i:])
			if r != utf8.RuneError || n > 1 {
				out = append(out, key{kind: kRune, r: r})
			}
			i += n
		}
	}
	return out
}
