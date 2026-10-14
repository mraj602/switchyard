package hubsrc

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
)

// A server-sent events parser (WHATWG "Event stream interpretation"): lines end in CRLF, LF
// or a bare CR; a line starting with ':' is a comment; `field: value` with one optional
// space after the colon; several `data` lines join with "\n"; a blank line dispatches the
// event. An `id` with no `data` sets the last event id but dispatches nothing; `retry` is
// kept for the caller.

// MaxEventBytes caps one event's data (and one line). T3: replies are capped at 4 MiB.
const MaxEventBytes = 4 << 20

// ErrTooLarge is returned when an event or line exceeds MaxEventBytes.
var ErrTooLarge = errors.New("hubsrc: stream event too large")

// Event is one dispatched message, or a comment.
type Event struct {
	// ID is the last event id seen up to and including this event ("" when none yet).
	ID string
	// Type is the `event` field ("message" when absent).
	Type string
	Data string
	// Retry is the last `retry` value in milliseconds (0: none).
	Retry int
	// Comment is true for a ": …" line; Data holds the text after the colon. A comment
	// carries no event; the caller treats it as a sign of life.
	Comment bool
}

// Parser reads Events from a stream.
type Parser struct {
	r      *bufio.Reader
	skipLF bool
	lastID string
	retry  int
	typ    string
	data   []byte
	hasDat bool
}

// NewParser reads from r.
func NewParser(r io.Reader) *Parser { return &Parser{r: bufio.NewReaderSize(r, 16<<10)} }

// LastID is the last event id the stream set.
func (p *Parser) LastID() string { return p.lastID }

// line reads one line without its terminator.
func (p *Parser) line() (string, error) {
	var buf []byte
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				// An unterminated last line is dropped, as browsers do.
				return "", io.ErrUnexpectedEOF
			}
			return "", err
		}
		if p.skipLF {
			p.skipLF = false
			if c == '\n' {
				continue
			}
		}
		switch c {
		case '\n':
			return string(buf), nil
		case '\r':
			p.skipLF = true
			return string(buf), nil
		}
		if len(buf) >= MaxEventBytes {
			return "", ErrTooLarge
		}
		buf = append(buf, c)
	}
}

// Next returns the next event or comment. It returns the reader's error (io.EOF at a clean
// end of stream) when the stream ends; a partial event at the end is dropped.
func (p *Parser) Next() (Event, error) {
	for {
		ln, err := p.line()
		if err != nil {
			return Event{}, err
		}
		if ln == "" {
			if !p.hasDat || len(p.data) == 0 {
				p.typ, p.data, p.hasDat = "", p.data[:0], false
				continue
			}
			ev := Event{ID: p.lastID, Type: p.typ, Data: string(p.data), Retry: p.retry}
			if ev.Type == "" {
				ev.Type = "message"
			}
			p.typ, p.data, p.hasDat = "", p.data[:0], false
			return ev, nil
		}
		if ln[0] == ':' {
			return Event{Comment: true, Data: strings.TrimPrefix(ln[1:], " ")}, nil
		}
		name, value, _ := strings.Cut(ln, ":")
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "event":
			p.typ = value
		case "data":
			if len(p.data)+len(value)+1 > MaxEventBytes {
				return Event{}, ErrTooLarge
			}
			if p.hasDat {
				p.data = append(p.data, '\n')
			}
			p.data = append(p.data, value...)
			p.hasDat = true
		case "id":
			// An id with a control byte (or an absurd length) would make every reconnect
			// fail as an invalid header; ignore it and keep the last good one.
			if validEventID(value) {
				p.lastID = value
			}
		case "retry":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 && value != "" && isDigits(value) {
				p.retry = n
			}
		}
	}
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validEventID(v string) bool {
	if len(v) > 256 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return false
		}
	}
	return true
}
