package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/board/tui"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/termctl"
	"github.com/mraj602/watchyard/internal/termenv"
)

// `wy board` (docs/spec/terminal.md T1, T4): the session board in this terminal. This file
// owns the flags, the modes (--once, --json, --follow --ndjson, the interactive TUI of
// internal/board/tui) and the call that picks a source. `connect` and `disconnect` are
// routed to board_connect.go.
//
// Read only: nothing here writes outside stdout (and `<state>` through the source's own
// cache), nothing types into another terminal, and the board token never reaches this file.

const boardUsage = `usage: wy board [--once] [--json] [--follow --ndjson] [--compact] [--ascii]
                [--source auto|hub|local] [--filter all|needs|here|machine:<name>]
                [--session <id>] [--cols N]
       wy board connect [--code-stdin] [--json]
       wy board disconnect

Without --once or --follow: the interactive board. --once: one text frame. --once --json:
one View (docs/spec/terminal.md T2). --follow --ndjson: one View per line on change.
`

// Errors a source selector returns for the exit codes of T1: 4 for --source hub without a
// usable hub, 5 for a hub that cannot be reached with --source hub.
var (
	errBoardNoHub   = errors.New("no usable hub: run `wy board connect`, or use --source local")
	errBoardHubDown = errors.New("hub unreachable")
)

// boardSelection is what a source selector gets.
type boardSelection struct {
	Mode       string // auto | hub | local
	Env        termenv.Env
	StateDir   string
	ConfigPath string
	Config     config.Config
	// Session is the `--session` id ("" when unset); the board marks that row this_session.
	Session string
}

// Seams, so tests run the whole command with a fake source, terminal and opener.
var (
	// boardSelectSource builds the board.Source (T3: the hub source when `hub.url` and a
	// board token exist, else the local fold; --source hub/local force one). See
	// board_source.go.
	boardSelectSource = selectBoardSource
	// boardOpenURL opens an https://claude.ai/ link through the T7 opener (the `o` key).
	// nil means the key answers "not available in this build".
	boardOpenURL = openRemoteURL
	// boardFocus brings the row's tab forward: `wy term focus --session <id>`.
	boardFocus = func(ctx context.Context, id string) error {
		var out, errb bytes.Buffer
		if err := runTermFocus([]string{"--session", id}, &out, &errb); err != nil {
			if msg := strings.TrimSpace(errb.String()); msg != "" {
				return errors.New(msg)
			}
			var ee *exitError
			if errors.As(err, &ee) && ee.err != nil {
				return ee.err
			}
			return err
		}
		return nil
	}
	boardIsTerminal = func(f *os.File) bool { return f != nil && term.IsTerminal(int(f.Fd())) }
	boardMakeRaw    = func(f *os.File) (*term.State, error) { return term.MakeRaw(int(f.Fd())) }
	boardRestore    = func(f *os.File, st *term.State) error { return term.Restore(int(f.Fd()), st) }
	boardSize       = func(f *os.File) (int, int) {
		c, r, err := term.GetSize(int(f.Fd()))
		if err != nil {
			return 0, 0
		}
		return c, r
	}
	// boardFirstView is how long --once waits for the first View.
	boardFirstView = 15 * time.Second
	// boardNDJSON are the timings of --follow --ndjson (T5): a line on change, debounced
	// 250 ms, and one every 30 s as a heartbeat.
	boardNDJSON = ndjsonTiming{debounce: 250 * time.Millisecond, heartbeat: 30 * time.Second}
)

func runBoard(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "connect":
			return runBoardConnect(args[1:], stdout, stderr)
		case "disconnect":
			return runBoardDisconnect(args[1:], stdout, stderr)
		case "-h", "--help", "help":
			fmt.Fprint(stderr, boardUsage)
			return nil
		}
	}
	return runBoardView(args, stdout, stderr)
}

type boardFlags struct {
	once, asJSON, follow, ndjson, compact, ascii bool
	source, filter, session                      string
	cols                                         int
}

func parseBoardFlags(args []string, stderr io.Writer, def config.Config) (boardFlags, error) {
	var f boardFlags
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, boardUsage) }
	fs.BoolVar(&f.once, "once", false, "print one frame and exit")
	fs.BoolVar(&f.asJSON, "json", false, "with --once: print the View as JSON")
	fs.BoolVar(&f.follow, "follow", false, "stream Views")
	fs.BoolVar(&f.ndjson, "ndjson", false, "with --follow: one View per line")
	fs.BoolVar(&f.compact, "compact", false, "one line per session")
	fs.BoolVar(&f.ascii, "ascii", false, "ASCII glyphs")
	fs.StringVar(&f.source, "source", "", "auto, hub or local (default terminal.source)")
	fs.StringVar(&f.filter, "filter", tui.FilterAll, "all, needs, here or machine:<name>")
	fs.StringVar(&f.session, "session", "", "this session's id (marked on the board)")
	fs.IntVar(&f.cols, "cols", 0, "frame width for --once (default: terminal width, $COLUMNS, 100)")
	if err := fs.Parse(args); err != nil {
		return f, usageErr(err)
	}
	bad := func(format string, a ...any) (boardFlags, error) {
		fmt.Fprintf(stderr, "wy board: "+format+"\n", a...)
		return f, errUsage
	}
	if fs.NArg() > 0 {
		return bad("unexpected argument %q", fs.Arg(0))
	}
	if f.source == "" {
		f.source = def.Terminal.Source
	}
	if f.source == "" {
		f.source = config.TerminalSourceAuto
	}
	if err := config.CheckTerminalSource(f.source); err != nil {
		return bad("--source: %v", err)
	}
	switch fl := strings.ToLower(f.filter); {
	case fl == tui.FilterAll, fl == tui.FilterNeeds, fl == tui.FilterHere:
		f.filter = fl
	case strings.HasPrefix(fl, "machine:") && len(f.filter) > len("machine:"):
	default:
		return bad("--filter: want all, needs, here or machine:<name>, got %q", f.filter)
	}
	switch {
	case f.asJSON && !f.once:
		return bad("--json needs --once")
	case f.follow && f.once:
		return bad("--follow and --once do not combine")
	case f.follow != f.ndjson:
		return bad("--follow and --ndjson go together")
	case f.cols < 0 || (f.cols > 0 && f.cols < 20):
		return bad("--cols: want 20 or more")
	}
	return f, nil
}

func runBoardView(args []string, stdout, stderr io.Writer) error {
	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return err
	}
	cf, err := config.Load(d.Config.Path)
	if err != nil {
		return err
	}
	cfg, err := cf.Typed()
	if err != nil {
		return err
	}
	f, err := parseBoardFlags(args, stderr, cfg)
	if err != nil {
		return err
	}
	env := termenv.FromOS()
	src, err := boardSelectSource(boardSelection{
		Mode: f.source, Env: env, StateDir: d.State.Path, ConfigPath: d.Config.Path, Config: cfg, Session: f.session,
	})
	if err != nil {
		switch {
		case errors.Is(err, errBoardNoHub):
			return exitWith(4, err)
		case errors.Is(err, errBoardHubDown):
			return exitWith(5, err)
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	views, err := src.Start(ctx)
	if err != nil {
		return err
	}
	views = decorate(ctx, views, f.session)

	switch {
	case f.follow:
		return followNDJSON(ctx, views, stdout, boardNDJSON)
	case f.once:
		return boardOnce(ctx, views, f, env, cfg, stdout)
	}
	out, _ := stdout.(*os.File)
	if out == nil || !boardIsTerminal(out) || !boardIsTerminal(os.Stdin) || env.Lookup("TERM") == "dumb" {
		// The line fallback: not a terminal (or TERM=dumb): one frame, no raw mode.
		return boardOnce(ctx, views, f, env, cfg, stdout)
	}
	return boardInteractive(ctx, src, views, f, env, cfg, out)
}

// decorate marks the --session row this_session on every View. It copies the row map, so a
// source's own cache is never written.
func decorate(ctx context.Context, in <-chan board.View, session string) <-chan board.View {
	if session == "" {
		return in
	}
	out := make(chan board.View)
	go func() {
		defer close(out)
		for v := range in {
			if r, ok := v.Rows[session]; ok && !r.ThisSession {
				rows := make(map[string]board.Row, len(v.Rows))
				for k, x := range v.Rows {
					rows[k] = x
				}
				r.ThisSession = true
				rows[session] = r
				v.Rows = rows
			}
			select {
			case out <- v:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func boardOptions(f boardFlags, env termenv.Env, cfg config.Config, isTTY bool) tui.Options {
	return tui.Options{
		Mode:    tui.DetectMode(env, isTTY),
		ASCII:   f.ascii || cfg.Terminal.ASCII || tui.DetectASCII(env),
		Compact: f.compact,
		Filter:  f.filter,
		Session: f.session,
	}
}

// boardOnce prints one View (--once) as text or JSON. It never touches the terminal mode.
func boardOnce(ctx context.Context, views <-chan board.View, f boardFlags, env termenv.Env, cfg config.Config, stdout io.Writer) error {
	var v board.View
	select {
	case got, ok := <-views:
		if !ok {
			return exitWith(5, errors.New("the source ended before it produced a view"))
		}
		v = got
	case <-time.After(boardFirstView):
		return exitWith(5, errors.New("no view within "+boardFirstView.String()))
	case <-ctx.Done():
		return nil
	}
	if f.asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	out, _ := stdout.(*os.File)
	tty := out != nil && boardIsTerminal(out)
	cols := f.cols
	if cols == 0 && tty {
		cols, _ = boardSize(out)
	}
	if cols == 0 {
		fmt.Sscanf(env.Lookup("COLUMNS"), "%d", &cols)
	}
	_, err := io.WriteString(stdout, tui.RenderOnce(v, cols, boardOptions(f, env, cfg, tty)))
	return err
}

// openRemoteURL opens a session's claude.ai link with the OS opener (termctl.NewOpener, so
// the fakeopen seam applies), and only a link termctl allows; the opener's stderr goes to
// <state>/logs/wy.log as for focus.
func openRemoteURL(ctx context.Context, url string) error {
	if !termctl.ValidRemoteURL(url) {
		return errors.New("only https://claude.ai/ links are opened")
	}
	env := focusEnv()
	log, closeLog := focusSyLog(env)
	defer closeLog()
	return focusOpener(env, log).Open(ctx, url)
}

// boardInteractive runs the TUI on out (a terminal). The console is put in VT mode first and
// restored last (Windows); raw mode is entered around Run and always left again.
func boardInteractive(ctx context.Context, src board.Source, views <-chan board.View, f boardFlags, env termenv.Env, cfg config.Config, out *os.File) error {
	restoreVT, err := tui.EnableVT(os.Stdin, out)
	if err != nil {
		return err
	}
	defer restoreVT()
	st, err := boardMakeRaw(os.Stdin)
	if err != nil {
		return err
	}
	defer func() { _ = boardRestore(os.Stdin, st) }()

	size := func() (int, int) { return boardSize(out) }
	o := boardOptions(f, env, cfg, true)
	o.In, o.Out, o.Size, o.AltScreen = os.Stdin, out, size, true
	o.Resize = tui.WatchResize(ctx, size)
	o.Focus, o.OpenURL, o.Refresh = boardFocus, boardOpenURL, src.Refresh
	return tui.Run(ctx, views, o)
}

// ---- --follow --ndjson ----

type ndjsonTiming struct{ debounce, heartbeat time.Duration }

// followNDJSON writes one View per line: the first at once, then one after a change settles
// (debounce) and one every heartbeat so a reader can tell a quiet hub from a dead child.
// A change is any difference other than generated_at. It returns nil when the source ends or
// ctx does, and the write error when the reader went away.
func followNDJSON(ctx context.Context, views <-chan board.View, w io.Writer, tm ndjsonTiming) error {
	var (
		latest, pending board.View
		haveLatest      bool
		lastSent        []byte
		debounce        <-chan time.Time
		beat            = time.NewTimer(tm.heartbeat)
	)
	defer beat.Stop()
	fingerprint := func(v board.View) []byte {
		v.GeneratedAt = time.Time{}
		b, _ := json.Marshal(v)
		return b
	}
	emit := func(v board.View) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		lastSent = fingerprint(v)
		if !beat.Stop() {
			select {
			case <-beat.C:
			default:
			}
		}
		beat.Reset(tm.heartbeat)
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case v, ok := <-views:
			if !ok {
				return nil
			}
			latest = v
			if !haveLatest {
				haveLatest = true
				if err := emit(v); err != nil {
					return err
				}
				continue
			}
			if debounce == nil && !bytes.Equal(fingerprint(v), lastSent) {
				debounce = time.After(tm.debounce)
			}
			pending = v
		case <-debounce:
			debounce = nil
			if bytes.Equal(fingerprint(pending), lastSent) {
				continue
			}
			if err := emit(pending); err != nil {
				return err
			}
		case <-beat.C:
			v := latest
			if debounce != nil {
				v = pending
				debounce = nil
			}
			v.GeneratedAt = time.Now().UTC()
			if err := emit(v); err != nil {
				return err
			}
		}
	}
}
