// Package tui is `sy board`'s screen (docs/spec/terminal.md T4): the session board as a
// dense terminal UI that lays out a board.View, never builds one. It has a compact (one line
// per session) and a full (two lines) layout, a header, a NEEDS YOU block, one block per
// account and a footer of keys, 24-bit / 256 / mono colour, Unicode or ASCII glyphs, and the
// keys `↑↓ j k Enter c o f / e r ? q`.
//
// No TUI framework. The caller puts the terminal in raw mode (golang.org/x/term, and the
// VT console mode on Windows, console_windows.go) and hands Run its input, its output and a
// size function, so tests drive it with scripted keys over a fake terminal. Run writes only to
// Options.Out (its stdout); no key sends anything to another terminal or session. Enter
// and `o` run callbacks the caller supplies (`sy term focus`, the T7 opener); the TUI itself
// opens nothing.
//
// Folders and the environment come from termenv.Env (internal/termenv/lint_test.go).
//
// Phase 7, WP-C. Depends on: board (View, Source), termenv.
package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/board"
)

// Options configures Run and RenderOnce.
type Options struct {
	// In delivers keys (raw mode); Out is the screen. Run writes nowhere else.
	In  io.Reader
	Out io.Writer
	// Size reports the terminal size; nil or 0 values mean 100 columns and no height limit.
	Size func() (cols, rows int)
	// Resize ticks when the terminal was resized (SIGWINCH, or the Windows poll).
	Resize <-chan struct{}

	Mode    Mode
	ASCII   bool
	Compact bool
	// Filter is the initial filter (FilterAll, FilterNeeds, FilterHere, "machine:<name>").
	Filter string
	// Session puts the cursor on this row when it first shows.
	Session string
	// AltScreen enters the alternate screen and hides the cursor for the life of Run.
	AltScreen bool

	// Focus runs `sy term focus --session <id>`; OpenURL opens an https://claude.ai/ link
	// through the T7 opener; Refresh asks the source for a refetch. Nil ones answer with a
	// footer message.
	Focus   func(ctx context.Context, id string) error
	OpenURL func(ctx context.Context, url string) error
	Refresh func()

	// Throttle is the shortest time between redraws caused by new Views (default 100 ms).
	Throttle time.Duration
}

// RenderOnce lays v out as plain text lines for `sy board --once` and the line fallback: no
// cursor, no key footer, and a hint line when the hub is not in use. Width is cols (100 when
// <= 0).
func RenderOnce(v board.View, cols int, o Options) string {
	m := newModel(&o)
	m.v, m.have = v, true
	return strings.Join(m.frame(cols, 0, false), "\n") + "\n"
}

// Osc52 is the exact byte sequence the `c` key writes to copy text.
func Osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
}

const (
	enterScreen = "\x1b[?1049h\x1b[?25l\x1b[2J"
	leaveScreen = "\x1b[0m\x1b[?25h\x1b[?1049l"
)

type screen struct {
	o          *Options
	prev       []string
	cols, rows int
}

func (s *screen) size() (int, int) {
	if s.o.Size != nil {
		if c, r := s.o.Size(); c > 0 {
			return c, r
		}
	}
	return defaultCols, 0
}

// draw writes the frame, only the lines that changed since the last one; extra (an OSC 52
// copy) goes first.
func (s *screen) draw(m *model, extra string) {
	cols, rows := s.size()
	lines := m.frame(cols, rows, true)
	var b bytes.Buffer
	b.WriteString(extra)
	if cols != s.cols || rows != s.rows {
		s.prev = nil
		s.cols, s.rows = cols, rows
		b.WriteString("\x1b[2J")
	}
	for i, l := range lines {
		if i < len(s.prev) && s.prev[i] == l {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[K", i+1, l)
	}
	if len(s.prev) > len(lines) {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[J", len(lines)+1)
	}
	s.prev = lines
	if b.Len() > 0 {
		_, _ = s.o.Out.Write(b.Bytes())
	}
}

// Run shows the board until the person quits (q, Ctrl-C, end of input) or ctx ends. views
// delivers every View; keys are read from o.In once the first View is in. Run restores the
// screen on the way out and waits for a pending focus/open callback (they get a cancelled
// context). It returns nil on a normal quit.
func Run(ctx context.Context, views <-chan board.View, o Options) error {
	if o.Throttle <= 0 {
		o.Throttle = 100 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(&o)
	sc := &screen{o: &o}
	if o.AltScreen {
		_, _ = io.WriteString(o.Out, enterScreen)
		defer func() { _, _ = io.WriteString(o.Out, leaveScreen) }()
	}

	keyCh := make(chan []byte)
	if o.In != nil {
		go func() {
			defer close(keyCh)
			buf := make([]byte, 256)
			for {
				n, err := o.In.Read(buf)
				if n > 0 {
					chunk := append([]byte(nil), buf[:n]...)
					select {
					case keyCh <- chunk:
					case <-ctx.Done():
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
	} else {
		close(keyCh)
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	notes := make(chan string, 8)
	note := func(s string) {
		select {
		case notes <- s:
		default:
		}
	}
	async := func(what string, f func(context.Context) error, okMsg string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(ctx); err != nil {
				if ctx.Err() == nil {
					note(what + ": " + oneLine(err.Error()))
				}
				return
			}
			note(okMsg)
		}()
	}

	var (
		last  time.Time
		timer *time.Timer
		tick  <-chan time.Time
		dirty bool
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	redraw := func(extra string) {
		sc.draw(m, extra)
		last, dirty, tick = time.Now(), false, nil
	}
	redraw("")

	for {
		var keysIn <-chan []byte
		if m.have {
			keysIn = keyCh
		}
		select {
		case <-ctx.Done():
			return nil
		case v, ok := <-views:
			if !ok {
				if !m.have {
					return nil
				}
				views = nil
				continue
			}
			first := !m.have
			m.v, m.have = v, true
			if first && o.Session != "" {
				if _, ok := v.Rows[o.Session]; ok {
					m.cursor = o.Session
				}
			}
			m.settle()
			dirty = true
			if first || time.Since(last) >= o.Throttle {
				redraw("")
			} else if tick == nil {
				if timer == nil {
					timer = time.NewTimer(o.Throttle - time.Since(last))
				} else {
					timer.Reset(o.Throttle - time.Since(last))
				}
				tick = timer.C
			}
		case <-tick:
			tick = nil
			if dirty {
				redraw("")
			}
		case <-o.Resize:
			sc.prev = nil
			redraw("")
		case n := <-notes:
			m.note = n
			redraw("")
		case b, ok := <-keysIn:
			if !ok {
				return nil
			}
			extra := ""
			for _, k := range parseKeys(b) {
				e := m.press(k)
				if e.quit {
					return nil
				}
				if e.refresh && o.Refresh != nil {
					o.Refresh()
				}
				if e.copy != "" {
					extra += Osc52(e.copy)
				}
				if id := e.focus; id != "" {
					if o.Focus == nil {
						m.note = "focus is not available in this build"
					} else {
						async("focus", func(c context.Context) error { return o.Focus(c, id) }, "focused")
					}
				}
				if u := e.open; u != "" {
					if o.OpenURL == nil {
						m.note = "opening links is not available in this build"
					} else {
						async("open", func(c context.Context) error { return o.OpenURL(c, u) }, "opened "+u)
					}
				}
			}
			redraw(extra)
		}
	}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
