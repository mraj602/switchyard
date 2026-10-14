package pair

import (
	"errors"
	"strings"

	"rsc.io/qr"
)

// quietZone is the white border around a QR code, in modules (the standard asks for 4).
const quietZone = 4

// Matrix is a QR module matrix with its quiet zone, in the API's {size, rows} form: Rows
// holds Size strings of '0' (light) and '1' (dark).
type Matrix struct {
	Size int      `json:"size"`
	Rows []string `json:"rows"`
}

// NewMatrix encodes text as a QR code at error correction level M with a 4-module quiet
// zone (pairing.md 2.2).
func NewMatrix(text string) (Matrix, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return Matrix{}, err
	}
	n := c.Size + 2*quietZone
	m := Matrix{Size: n, Rows: make([]string, n)}
	for y := range n {
		row := make([]byte, n)
		for x := range n {
			cx, cy := x-quietZone, y-quietZone
			if cx >= 0 && cy >= 0 && cx < c.Size && cy < c.Size && c.Black(cx, cy) {
				row[x] = '1'
			} else {
				row[x] = '0'
			}
		}
		m.Rows[y] = string(row)
	}
	return m, nil
}

// Black reports whether module (x, y) is dark; outside the matrix is light.
func (m Matrix) Black(x, y int) bool {
	return y >= 0 && y < len(m.Rows) && x >= 0 && x < len(m.Rows[y]) && m.Rows[y][x] == '1'
}

const (
	ansiBlackOnWhite = "\x1b[30;47m"
	ansiReset        = "\x1b[0m"
)

// Terminal renders the matrix two module rows per text row with the half blocks ▀ ▄ █ and
// a space, each line in black on white so it scans on dark and light terminals.
func (m Matrix) Terminal() string {
	var b strings.Builder
	for y := 0; y < m.Size; y += 2 {
		b.WriteString(ansiBlackOnWhite)
		for x := range m.Size {
			top, bottom := m.Black(x, y), m.Black(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteString(ansiReset)
		b.WriteByte('\n')
	}
	return b.String()
}

// ParseTerminal reads back the output of Terminal (ANSI codes are ignored). It exists so
// tests can prove the rendering decodes to the encoder's matrix.
func ParseTerminal(s string) (Matrix, error) {
	var lines []string
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		ln = strings.ReplaceAll(ln, ansiBlackOnWhite, "")
		ln = strings.ReplaceAll(ln, ansiReset, "")
		lines = append(lines, ln)
	}
	if len(lines) == 0 {
		return Matrix{}, errors.New("empty rendering")
	}
	size := len([]rune(lines[0]))
	rows := make([][]byte, 0, 2*len(lines))
	for _, ln := range lines {
		rs := []rune(ln)
		if len(rs) != size {
			return Matrix{}, errors.New("ragged rendering")
		}
		top, bottom := make([]byte, size), make([]byte, size)
		for x, r := range rs {
			top[x], bottom[x] = '0', '0'
			switch r {
			case '█':
				top[x], bottom[x] = '1', '1'
			case '▀':
				top[x] = '1'
			case '▄':
				bottom[x] = '1'
			case ' ':
			default:
				return Matrix{}, errors.New("unexpected character in rendering")
			}
		}
		rows = append(rows, top, bottom)
	}
	m := Matrix{Size: size}
	for _, r := range rows[:size] {
		m.Rows = append(m.Rows, string(r))
	}
	return m, nil
}
