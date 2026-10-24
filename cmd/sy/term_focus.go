package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/board"
	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/record"
	"github.com/mraj602/switchyard/internal/status"
	"github.com/mraj602/switchyard/internal/termctl"
	"github.com/mraj602/switchyard/internal/termenv"
)

// `sy term focus --session <id> | --url <u>` (docs/spec/terminal.md T7): brings a Warp tab
// forward through the allow-listed opener. It reads state and runs the OS opener; it never
// types into a terminal and writes nothing outside <state>.

// Exit codes of `sy term focus` (T1).
const (
	exitFocusNone = 4 // no usable focus URL here
	exitFocusOpen = 6 // the opener failed
)

const termFocusUsage = `usage: sy term focus --session <id> | --url <warp://session/<32 hex>>

Brings that session's Warp tab forward. --session looks the tab up (this machine's outbox,
else the hub with the board token) and refuses a session of another machine. --url takes a
Warp focus URL directly and only works inside Warp. Exit 4: no usable focus URL here;
6: the opener failed.
`

// Seams for tests: the environment and the opener.
var (
	focusEnv    = termenv.FromOS
	focusOpener = termctl.NewOpener
	focusHubGet = hubSessionFocus
)

var focusSessionRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func runTermFocus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("term focus", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, termFocusUsage) }
	session := fs.String("session", "", "session id")
	url := fs.String("url", "", "Warp focus URL")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageErr(err)
	}
	if fs.NArg() > 0 || (*session == "") == (*url == "") {
		fmt.Fprint(stderr, termFocusUsage)
		return errUsage
	}
	env := focusEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	target := *url
	if *session != "" {
		if !focusSessionRe.MatchString(*session) {
			fmt.Fprintf(stderr, "sy term focus: %q is not a session id\n", *session)
			return errUsage
		}
		var err error
		if target, err = resolveFocus(ctx, env, *session); err != nil {
			return exitWith(exitFocusNone, err)
		}
	} else if !focusWarpHere(env) {
		return exitWith(exitFocusNone, errors.New("Warp does not run here (TERM_PROGRAM is not WarpTerminal)"))
	}
	if !termctl.ValidFocusURL(target) {
		return exitWith(exitFocusNone, errors.New("not a Warp focus URL"))
	}

	log, closeLog := focusSyLog(env)
	defer closeLog()
	if err := focusOpener(env, log).Open(ctx, target); err != nil {
		return exitWith(exitFocusOpen, err)
	}
	return nil
}

// focusWarpHere is the T7 test for `--url`: this process runs inside Warp.
func focusWarpHere(env termenv.Env) bool {
	return strings.EqualFold(env.Lookup("TERM_PROGRAM"), "WarpTerminal") || env.Lookup("WARP_TERMINAL_SESSION_UUID") != ""
}

// focusState is what resolveFocus needs from this machine.
type focusState struct {
	dirs    paths.Dirs
	cfg     config.Config
	machine string
}

func loadFocusState(env termenv.Env) (focusState, error) {
	var fsx focusState
	dirs, err := paths.Resolve(paths.Env{GOOS: env.GOOS, Home: env.Home, Getenv: env.Lookup, Exists: paths.StatExists})
	if err != nil {
		return fsx, err
	}
	fsx.dirs = dirs
	if f, err := config.Load(dirs.Config.Path); err == nil {
		fsx.cfg, _ = f.Typed()
	} else {
		fsx.cfg = config.Defaults()
	}
	m, err := identity.LoadOrCreateMachine(dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return fsx, err
	}
	fsx.machine = m.ID
	return fsx, nil
}

// resolveFocus finds the focus URL of a session on this machine: the local outbox first,
// then the hub (its own machine check applies). A session whose terminal is on another
// machine, or that is unknown, is an error and the opener is never called.
func resolveFocus(ctx context.Context, env termenv.Env, id string) (string, error) {
	st, err := loadFocusState(env)
	if err != nil {
		return "", err
	}
	if url, found, err := localFocus(st, id); found || err != nil {
		return url, err
	}
	url, err := focusHubGet(ctx, st, id)
	if err != nil {
		return "", err
	}
	if url == "" {
		return "", errors.New("the hub has no focus URL for this session on this machine")
	}
	return url, nil
}

// localFocus reads the session's newest terminal record from the local outbox.
func localFocus(st focusState, id string) (url string, found bool, err error) {
	recs, err := status.Load(filepath.Join(st.dirs.State.Path, "outbox"), time.Now().Add(-status.Window))
	if err != nil {
		return "", false, err
	}
	var best *record.Record
	var bestTS time.Time
	for _, r := range recs {
		if r.Kind != record.KindHookEvent || focusSessionOf(r) != id {
			continue
		}
		ts, terr := r.Time()
		if terr != nil {
			continue
		}
		found = true
		if r.Term != nil && r.Term.FocusURL != "" && (best == nil || ts.After(bestTS)) {
			best, bestTS = r, ts
		}
	}
	if !found {
		return "", false, nil
	}
	if best == nil {
		return "", true, errors.New("this session has no terminal focus URL (not started in Warp)")
	}
	if best.Machine != st.machine || (best.Term.Host != "" && best.Term.Host != st.machine) {
		return "", true, errors.New("this session's tab is on another machine; focus it from that machine")
	}
	return best.Term.FocusURL, true, nil
}

func focusSessionOf(r *record.Record) string {
	var p struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(r.Payload, &p)
	return p.SessionID
}

// hubSessionFocus asks the hub for the session with the board token (T3, T7):
// `<state>/board/token.json` for hub.url (board.LoadToken: a token bound to another URL is
// dropped), the http rule of `sy connect` (client.CheckURL, so loopback, Tailscale or
// hub.insecure_lan), and hub.dial for a forward-mode machine (client.DialTransport), as the
// board's hub source does. The hub sends focus_url only to a desktop device on the
// terminal's own machine, so a null answer means "not here". The token is read here and
// never printed. The View never carries the URL, so a board that is offline cannot focus a
// session of another machine: the lookup needs the hub (this machine's own sessions resolve
// from the outbox first).
func hubSessionFocus(ctx context.Context, st focusState, id string) (string, error) {
	if st.cfg.Hub.URL == "" {
		return "", errors.New("unknown session on this machine, and no hub (hub.url is not set)")
	}
	tok, err := board.LoadToken(st.dirs.State.Path, st.cfg.Hub.URL)
	if err != nil {
		return "", errors.New("unknown session on this machine, and no board token for the hub (run `sy board connect`)")
	}
	base, _, err := client.CheckURL(st.cfg.Hub.URL, st.cfg.Hub.InsecureLAN)
	if err != nil {
		return "", err
	}
	tr, err := client.DialTransport(st.cfg.Hub.Dial)
	if err != nil {
		return "", fmt.Errorf("hub.dial: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+neturl.PathEscape(id), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("User-Agent", "sy-board")
	hc := &http.Client{Transport: tr, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		return "", errors.New("hub unreachable")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", errors.New("unknown session")
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", errors.New("hub refused the board token (run `sy board connect`)")
	default:
		return "", fmt.Errorf("hub answered %d", resp.StatusCode)
	}
	var s struct {
		FocusURL *string `json:"focus_url"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&s) != nil || s.FocusURL == nil {
		return "", nil
	}
	return *s.FocusURL, nil
}

// focusSyLog opens <state>/logs/sy.log for the opener's stderr; when it cannot, the
// opener's stderr is dropped.
func focusSyLog(env termenv.Env) (io.Writer, func()) {
	dirs, err := paths.Resolve(paths.Env{GOOS: env.GOOS, Home: env.Home, Getenv: env.Lookup, Exists: paths.StatExists})
	if err != nil {
		return nil, func() {}
	}
	dir := filepath.Join(dirs.State.Path, "logs")
	if os.MkdirAll(dir, 0o700) != nil {
		return nil, func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, "sy.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, func() {}
	}
	return f, func() { f.Close() }
}
