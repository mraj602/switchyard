package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/mraj602/switchyard/internal/board"
	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/desktop"
	"github.com/mraj602/switchyard/internal/identity"
)

// `sy board connect` and `sy board disconnect` (docs/spec/terminal.md T1, T3): the board's
// read token, a desktop-kind device named after this machine. The token goes from the hub
// (admin channel, or a desktop pairing code on stdin) straight into
// <state>/board/token.json; it is never printed, never in argv, never in the environment.
// Exit codes: 0, 2 usage, 3 refused by the hub, 4 no admin channel and no --code-stdin (or
// not connected to a hub), 5 hub unreachable.

// boardEnvHook lets tests adjust the desktop environment `sy board connect` runs on.
var boardEnvHook func(*desktop.Env)

func runBoardConnect(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("board connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	codeStdin := fs.Bool("code-stdin", false, "claim the desktop pairing code (or secret) on the first line of stdin")
	asJSON := fs.Bool("json", false, "print {device, hub} as JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "sy board connect: unexpected argument %q\n", pos[0])
		return errUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, err := osDesktopEnv()
	if err != nil {
		return err
	}
	if boardEnvHook != nil {
		boardEnvHook(&env)
	}
	state := env.Dirs.State.Path
	m, err := identity.LoadOrCreateMachine(state, identity.MachineOptions{})
	if err != nil {
		return err
	}
	// The board supports forward mode (hub.dial), which `sy desktop token` refuses: hand
	// desktop.RunToken a config without hub.dial and an HTTP client that dials through it.
	if dial := env.Config.Hub.Dial; dial != "" {
		env.Config.Hub.Dial = ""
		if env.HTTP == nil {
			env.HTTP = func(timeout time.Duration) (*http.Client, error) { return client.HTTPClient(dial, timeout) }
		}
	}
	boundURL := env.Config.Hub.URL // the URL sy dials; LoadToken compares against it
	tok, err := desktop.RunToken(ctx, env, desktop.TokenOptions{CodeStdin: *codeStdin, Name: board.TokenName(m.Name)})
	if err != nil {
		var de *desktop.Error
		if errors.As(err, &de) {
			if de.Code == desktop.ExitUsage {
				fmt.Fprintf(stderr, "sy board connect: %v\n", de.Err)
				return errUsage
			}
			return exitWith(de.Code, de.Err)
		}
		return err
	}
	if err := board.SaveToken(state, board.Token{URL: boundURL, Device: tok.Device, Token: tok.Token}); err != nil {
		return err
	}
	out := struct {
		Device string `json:"device"`
		Hub    string `json:"hub"`
	}{tok.Device, boundURL}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(out)
	}
	fmt.Fprintf(stdout, "board token saved for %s (device %s).\nThe token stays in %s; `sy board disconnect` removes it.\n",
		out.Hub, out.Device, board.TokenFile(state))
	return nil
}

func runBoardDisconnect(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("board disconnect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "sy board disconnect: unexpected argument %q\n", pos[0])
		return errUsage
	}
	me, err := osMachineEnv()
	if err != nil {
		return err
	}
	state := me.dirs.State.Path
	prev, had := board.PeekToken(state)
	removed, err := board.RemoveToken(state)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintln(stdout, "no board token to remove.")
		return nil
	}
	fmt.Fprintln(stdout, "board token removed from this machine.")
	if had && prev.Device != "" {
		fmt.Fprintf(stdout, "The hub still lists the device; revoke it with: sy hub token revoke %s\n", prev.Device)
	}
	return nil
}
