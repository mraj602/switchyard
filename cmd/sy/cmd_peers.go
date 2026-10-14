package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/paths"
	peerspkg "github.com/mraj602/switchyard/internal/peers"
)

const peersUsage = `usage: sy peers list [--all] [--json]
       sy peers send <to> <text> [--ttl 1h] [--wait 10] [--stdin]
       sy peers off | on

Message Claude Code sessions in other accounts through Switchyard (docs/spec/peers.md).
send and list work inside a Claude Code session (list --all also at a terminal).
`

// peersEnv builds the environment of one `sy peers` run. A variable so tests can give a
// temp HOME, runtime dir and fake /proc.
var peersEnv = func() (peerEnv, error) {
	pe := paths.FromOS()
	dirs, err := paths.Resolve(pe)
	if err != nil {
		return peerEnv{}, err
	}
	cfg := config.Defaults()
	if f, err := config.Load(dirs.Config.Path); err == nil {
		cfg, _ = f.Typed() // a broken key falls back to its default; hooks log the cause
	}
	m, err := identity.LoadOrCreateMachine(dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return peerEnv{}, err
	}
	home := pe.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return peerEnv{
		Env: peerspkg.Env{
			Home: home, State: dirs.State.Path, Runtime: os.Getenv("XDG_RUNTIME_DIR"), Machine: m,
			SlotExtra: cfg.Slots.Extra, SlotLabels: cfg.Slots.Labels, MaxTurns: peerspkg.DefaultMaxTurns,
			Proc: peerspkg.NewProc(), StartPID: os.Getppid(), FS: identity.OSFS{},
		},
		Enabled: cfg.Peers.Enabled,
	}, nil
}

// peerEnv is the peers environment plus the config switch that turns the commands on.
type peerEnv struct {
	peerspkg.Env
	Enabled bool
}

func peersExit(code int, err error) error { return &exitError{code: code, err: err} }

// runPeers is `sy peers`. PE1 builds the same-machine commands: list, send, off, on. The
// others (enable, matrix, allow, deny, pause, audit, ...) need the keys, matrix and agent
// work and exit 3 until then.
func runPeers(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, peersUsage)
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	if runtime.GOOS == "windows" {
		return peersExit(peerspkg.ExitUnsupported, errors.New("peers are not supported on Windows (Claude Code's inbox sockets need a per-session token that Switchyard never reads)"))
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return peersList(rest, stdout, stderr)
	case "send":
		return peersSend(rest, os.Stdin, stdout, stderr)
	case "off", "on":
		return peersOffOn(sub == "off", rest, stdout, stderr)
	case "enable", "disable", "status", "matrix", "allow", "deny", "same-account", "max-turns", "pause", "resume", "audit":
		return peersExit(peerspkg.ExitUnsupported, fmt.Errorf("peers %s is not built yet: it needs the keys, matrix and agent (peers.md PE1-PE3)", sub))
	}
	fmt.Fprintf(stderr, "sy peers: unknown subcommand %q\n\n%s", sub, peersUsage)
	return errUsage
}

func peersList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("peers list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "show every session, with what the matrix allows")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "sy peers list: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	if !pe.Enabled {
		return peersExit(peerspkg.ExitPolicy, errors.New("peers are not enabled on this machine (sy config set peers.enabled true)"))
	}
	e := pe.Env
	dir := e.Directory()
	var from *peerspkg.Session
	caller, rf := e.Whoami()
	switch {
	case rf == nil:
		for i := range dir {
			if dir[i].Entry.SessionID == caller.Entry.SessionID {
				from = &dir[i]
			}
		}
	case rf.Code == peerspkg.CodeNotASession && *all:
		// A terminal: every session, no asking session.
	case rf.Code == peerspkg.CodeNotASession:
		return peersExit(peerspkg.ExitGuard, errors.New("sy peers list works inside a Claude Code session; at a terminal use --all"))
	default:
		return peersExit(peerspkg.ExitCode(rf.Code), rf)
	}
	entries := e.Entries(dir, from, *all || from == nil)
	if *asJSON {
		if entries == nil {
			entries = []peerspkg.ListEntry{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(entries)
	}
	if len(entries) == 0 {
		if from != nil && !*all {
			if e.MatrixConfigured() {
				fmt.Fprintln(stdout, "No session you may message right now (the matrix allows none, or no other session is running).")
			} else {
				fmt.Fprintln(stdout, "No session you may message: the matrix is not configured on this machine, so nothing is allowed yet.")
			}
		} else {
			fmt.Fprintln(stdout, "No other live session on this machine.")
		}
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HANDLE\tACCOUNT\tPROJECT\tSTATE\tCAN")
	for _, le := range entries {
		can := le.Can
		if le.MayAsk && can != "none" {
			can += " (may ask for approval)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", le.Handle, le.AccountLabel, le.Project, le.State, can)
	}
	return tw.Flush()
}

// peersSend is `sy peers send <to> <text> [--ttl] [--wait] [--stdin]`. Flags may come before
// or after the positionals, since Claude writes it either way.
func peersSend(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("peers send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ttl := fs.Duration("ttl", time.Hour, "how long a queued message may wait (1m-24h; used for other machines)")
	wait := fs.Int("wait", 10, "seconds to wait for a final status (0-60; same-machine sends are final at once)")
	useStdin := fs.Bool("stdin", false, "read the text from standard input")
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return usageErr(err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if *ttl < time.Minute || *ttl > 24*time.Hour {
		fmt.Fprintln(stderr, "sy peers send: --ttl must be between 1m and 24h")
		return errUsage
	}
	if *wait < 0 || *wait > 60 {
		fmt.Fprintln(stderr, "sy peers send: --wait must be between 0 and 60")
		return errUsage
	}
	if len(pos) == 0 || (*useStdin && len(pos) != 1) || (!*useStdin && len(pos) < 2) {
		fmt.Fprint(stderr, "usage: sy peers send <to> <text> [--ttl 1h] [--wait 10]\n       sy peers send <to> --stdin\n")
		return errUsage
	}
	to := pos[0]
	var text string
	if *useStdin {
		b, err := io.ReadAll(io.LimitReader(stdin, 2*int64(peerspkg.MaxTextBytes)))
		if err != nil {
			return fmt.Errorf("read the text from stdin: %w", err)
		}
		text = string(b)
	} else {
		text = strings.Join(pos[1:], " ")
	}

	pe, err := peersEnv()
	if err != nil {
		return err
	}
	if !pe.Enabled {
		fmt.Fprintln(stdout, "not delivered: peers are not enabled on this machine. The owner enables them with: sy config set peers.enabled true. Do not retry.")
		return peersExit(peerspkg.ExitPolicy, nil)
	}
	caller, rf := pe.Env.Whoami()
	if rf != nil {
		return peersExit(peerspkg.ExitCode(rf.Code), errors.New(rf.Detail))
	}
	res, rf := pe.Env.Send(caller, to, text)
	if rf != nil {
		fmt.Fprintln(stdout, peerspkg.Describe(rf))
		return peersExit(peerspkg.ExitCode(rf.Code), nil)
	}
	fmt.Fprintln(stdout, res.Line())
	return nil
}

// peersOffOn is `sy peers off|on`: this machine only, works with no hub (peers.md 11).
func peersOffOn(off bool, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "sy peers: unexpected argument %q\n", args[0])
		return errUsage
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	if err := pe.Env.SetOff(off); err != nil {
		return err
	}
	if off {
		fmt.Fprintln(stdout, "peers are off on this machine: nothing is sent or delivered. sy peers on turns them back on.")
	} else {
		fmt.Fprintln(stdout, "peers are on again on this machine.")
	}
	return nil
}
