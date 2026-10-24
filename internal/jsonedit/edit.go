package jsonedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Errors of the editor.
var (
	ErrInvalid    = errors.New("jsonedit: document is not valid JSON")
	ErrNotObject  = errors.New("jsonedit: pointer passes through a value that is not an object or array")
	ErrBadPointer = errors.New("jsonedit: bad JSON pointer")
	ErrBadValue   = errors.New("jsonedit: value is not valid JSON")
	ErrOutOfRange = errors.New("jsonedit: array index out of range")
)

// Change is one planned splice: replace doc[Start:End] with Repl. Everything outside
// [Start, End) is untouched by construction.
type Change struct {
	Start, End int
	Repl       []byte
	// BeforePresent says the pointer existed; Before is its bytes as written in the document.
	BeforePresent bool
	Before        []byte
	// Changed is false when the document already had that value (set) or lacked the key
	// (delete); the splice is then empty.
	Changed bool
}

// Apply returns doc with the change spliced in (a new slice).
func (c Change) Apply(doc []byte) []byte {
	if !c.Changed {
		return append([]byte(nil), doc...)
	}
	out := make([]byte, 0, len(doc)-(c.End-c.Start)+len(c.Repl))
	out = append(out, doc[:c.Start]...)
	out = append(out, c.Repl...)
	return append(out, doc[c.End:]...)
}

// ParsePointer splits an RFC 6901 pointer ("/a/b~1c") into its unescaped tokens. The empty
// pointer (the whole document) is refused: wy never replaces a whole file.
func ParsePointer(p string) ([]string, error) {
	if p == "" || p[0] != '/' {
		return nil, fmt.Errorf("%w: %q", ErrBadPointer, p)
	}
	parts := strings.Split(p[1:], "/")
	for i, s := range parts {
		s = strings.ReplaceAll(s, "~1", "/")
		parts[i] = strings.ReplaceAll(s, "~0", "~")
	}
	return parts, nil
}

// Get returns the bytes of the value at pointer exactly as written.
func Get(doc []byte, pointer string) ([]byte, bool, error) {
	c, err := PlanDelete(doc, pointer)
	if err != nil {
		return nil, false, err
	}
	return c.Before, c.BeforePresent, nil
}

// Set sets pointer to value. Missing objects along the way are created. An array element
// may be replaced by index, appended with "-" or with index == length.
func Set(doc []byte, pointer string, value []byte) ([]byte, Change, error) {
	c, err := PlanSet(doc, pointer, value)
	if err != nil {
		return nil, c, err
	}
	return c.Apply(doc), c, nil
}

// Delete removes the member or element at pointer (parents stay, possibly empty).
func Delete(doc []byte, pointer string) ([]byte, Change, error) {
	c, err := PlanDelete(doc, pointer)
	if err != nil {
		return nil, c, err
	}
	return c.Apply(doc), c, nil
}

type ent struct{ ks, ke, vs, ve int } // key start/end (objects only), value start/end

type container struct {
	obj         bool
	open, close int
	ents        []ent
	idx         int // entry the pointer went through, -1 when none
}

func skipWS(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

func stringEnd(b []byte, i int) int { // b[i] == '"'; returns index after the closing quote
	i++
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i + 1
		}
		i++
	}
	return i
}

// valueEnd returns the index after the value starting at b[i] (b is valid JSON).
func valueEnd(b []byte, i int) int {
	switch b[i] {
	case '"':
		return stringEnd(b, i)
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				i = stringEnd(b, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return i
	}
	for i < len(b) && !strings.ContainsRune(",}] \t\r\n", rune(b[i])) {
		i++
	}
	return i
}

func parseContainer(b []byte, open int) container {
	c := container{obj: b[open] == '{', open: open, idx: -1}
	i := skipWS(b, open+1)
	for i < len(b) && b[i] != '}' && b[i] != ']' {
		var e ent
		e.ks = i
		if c.obj {
			e.ke = stringEnd(b, i)
			i = skipWS(b, e.ke)
			i = skipWS(b, i+1) // ':'
		}
		e.vs = i
		e.ve = valueEnd(b, i)
		c.ents = append(c.ents, e)
		i = skipWS(b, e.ve)
		if i < len(b) && b[i] == ',' {
			i = skipWS(b, i+1)
		}
	}
	c.close = i
	return c
}

func keyOf(b []byte, e ent) string {
	var s string
	if json.Unmarshal(b[e.ks:e.ke], &s) != nil {
		return string(b[e.ks+1 : e.ke-1])
	}
	return s
}

// walk follows tokens from the root. It returns the deepest container reached, the number
// of tokens consumed, and whether all were found (then the container's idx names the entry).
func walk(doc []byte, toks []string) (c container, used int, found bool, err error) {
	curStart := skipWS(doc, 0)
	if curStart >= len(doc) {
		return c, 0, false, ErrInvalid
	}
	for i, tok := range toks {
		if doc[curStart] != '{' && doc[curStart] != '[' {
			return c, i, false, ErrNotObject
		}
		c = parseContainer(doc, curStart)
		if c.obj {
			for j := len(c.ents) - 1; j >= 0; j-- { // last duplicate wins, as JSON.parse
				if keyOf(doc, c.ents[j]) == tok {
					c.idx = j
					break
				}
			}
		} else if n, perr := strconv.Atoi(tok); perr == nil && n >= 0 && n < len(c.ents) && strconv.Itoa(n) == tok {
			c.idx = n
		}
		if c.idx < 0 {
			return c, i, false, nil
		}
		curStart = c.ents[c.idx].vs
	}
	return c, len(toks), true, nil
}

func quote(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func lineStart(b []byte, pos int) int {
	for pos > 0 && b[pos-1] != '\n' {
		pos--
	}
	return pos
}

// lineIndent is the leading blanks of the line containing pos.
func lineIndent(b []byte, pos int) string {
	s := lineStart(b, pos)
	e := s
	for e < len(b) && (b[e] == ' ' || b[e] == '\t') {
		e++
	}
	return string(b[s:e])
}

// indentUnit detects the indent of the first nested line (all spaces or all tabs), default
// two spaces.
func indentUnit(b []byte) string {
	for i := 0; i < len(b); i++ {
		if b[i] != '\n' {
			continue
		}
		j := i + 1
		for j < len(b) && (b[j] == ' ' || b[j] == '\t') {
			j++
		}
		if j > i+1 && j < len(b) && b[j] != '\n' && b[j] != '\r' {
			u := string(b[i+1 : j])
			if strings.Trim(u, " ") == "" || strings.Trim(u, "\t") == "" {
				return u
			}
		}
	}
	return "  "
}

func docMultiline(doc []byte) bool {
	root := skipWS(doc, 0)
	if root >= len(doc) || (doc[root] != '{' && doc[root] != '[') {
		return false
	}
	inner := doc[root:valueEnd(doc, root)]
	if len(bytes.TrimSpace(inner[1:len(inner)-1])) == 0 {
		return true // an empty root is written pretty
	}
	return bytes.IndexByte(inner, '\n') >= 0
}

func formatValue(v []byte, indent, unit string, multi bool) []byte {
	var buf bytes.Buffer
	if multi {
		_ = json.Indent(&buf, v, indent, unit)
	} else {
		_ = json.Compact(&buf, v)
	}
	return buf.Bytes()
}

func canon(b []byte) (any, bool) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return nil, false
	}
	return v, true
}

func sameValue(a, b []byte) bool {
	x, ok1 := canon(a)
	y, ok2 := canon(b)
	return ok1 && ok2 && reflect.DeepEqual(x, y)
}

func buildNested(toks []string, value []byte) []byte {
	if len(toks) == 0 {
		return value
	}
	out := []byte{'{'}
	out = append(out, quote(toks[0])...)
	out = append(out, ':')
	out = append(out, buildNested(toks[1:], value)...)
	return append(out, '}')
}

// PlanSet computes the splice that sets pointer to value.
func PlanSet(doc []byte, pointer string, value []byte) (Change, error) {
	toks, err := ParsePointer(pointer)
	if err != nil {
		return Change{}, err
	}
	if !json.Valid(value) {
		return Change{}, ErrBadValue
	}
	if !json.Valid(doc) {
		return Change{}, ErrInvalid
	}
	c, used, found, err := walk(doc, toks)
	if err != nil {
		return Change{}, err
	}
	multi := docMultiline(doc)
	unit := indentUnit(doc)
	if found {
		e := c.ents[c.idx]
		before := doc[e.vs:e.ve]
		ch := Change{BeforePresent: true, Before: append([]byte(nil), before...)}
		if sameValue(before, value) {
			return ch, nil
		}
		ind := lineIndent(doc, e.ks)
		ch.Start, ch.End, ch.Repl, ch.Changed = e.vs, e.ve, formatValue(value, ind, unit, multi), true
		return ch, nil
	}
	rest := toks[used:]
	if !c.obj {
		n, perr := strconv.Atoi(rest[0])
		if len(rest) > 1 || !(rest[0] == "-" || (perr == nil && n == len(c.ents) && strconv.Itoa(n) == rest[0])) {
			return Change{}, ErrOutOfRange
		}
		return insertEntry(doc, c, nil, value, unit, multi), nil
	}
	return insertEntry(doc, c, quote(rest[0]), buildNested(rest[1:], value), unit, multi), nil
}

func insertEntry(doc []byte, c container, key, val []byte, unit string, multi bool) Change {
	ch := Change{Changed: true}
	openInd := lineIndent(doc, c.open)
	pretty := multi
	if len(c.ents) > 0 {
		pretty = bytes.IndexByte(doc[c.open:c.ents[0].ks], '\n') >= 0
	}
	if len(c.ents) == 0 {
		ind := openInd + unit
		var item []byte
		if key != nil {
			item = append(append([]byte(nil), key...), ':')
			if pretty {
				item = append(item, ' ')
			}
		}
		ch.Start, ch.End = c.open+1, c.close
		if pretty {
			item = append(item, formatValue(val, ind, unit, true)...)
			ch.Repl = append(append([]byte("\n"+ind), item...), []byte("\n"+openInd)...)
		} else {
			ch.Repl = append(item, formatValue(val, "", unit, false)...)
		}
		return ch
	}
	last := c.ents[len(c.ents)-1]
	sep := []byte(": ")
	if key != nil && last.ke > 0 {
		if s := doc[last.ke:last.vs]; bytes.IndexByte(s, '\n') < 0 {
			sep = s
		}
	}
	ch.Start, ch.End = last.ve, last.ve
	if pretty {
		ind := openInd + unit
		for j := len(c.ents) - 1; j >= 0; j-- {
			ls := lineStart(doc, c.ents[j].ks)
			if len(bytes.Trim(doc[ls:c.ents[j].ks], " \t")) == 0 {
				ind = string(doc[ls:c.ents[j].ks])
				break
			}
		}
		item := []byte(",\n" + ind)
		if key != nil {
			item = append(append(item, key...), sep...)
		}
		ch.Repl = append(item, formatValue(val, ind, unit, true)...)
	} else {
		item := append([]byte(","), doc[c.open+1:c.ents[0].ks]...)
		if key != nil {
			item = append(append(item, key...), sep...)
		}
		ch.Repl = append(item, formatValue(val, "", unit, false)...)
	}
	return ch
}

// PlanDelete computes the splice that removes pointer. A missing key is not an error: the
// change has Changed == false (and BeforePresent == false).
func PlanDelete(doc []byte, pointer string) (Change, error) {
	toks, err := ParsePointer(pointer)
	if err != nil {
		return Change{}, err
	}
	if !json.Valid(doc) {
		return Change{}, ErrInvalid
	}
	c, _, found, err := walk(doc, toks)
	if err != nil {
		if errors.Is(err, ErrNotObject) {
			return Change{}, nil // a path through a scalar: the key is not there
		}
		return Change{}, err
	}
	if !found {
		return Change{}, nil
	}
	e := c.ents[c.idx]
	ch := Change{BeforePresent: true, Before: append([]byte(nil), doc[e.vs:e.ve]...), Changed: true}
	n := len(c.ents)
	switch {
	case n == 1:
		ch.Start, ch.End = c.open+1, c.close
	case c.idx == n-1:
		ch.Start, ch.End = c.ents[c.idx-1].ve, e.ve
	default:
		ch.Start, ch.End = e.ks, c.ents[c.idx+1].ks
	}
	return ch, nil
}
