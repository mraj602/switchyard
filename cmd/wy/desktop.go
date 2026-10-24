package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"runtime"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/desktop"
	"github.com/mraj602/watchyard/internal/machineadd"
	"github.com/mraj602/watchyard/internal/service"
	"github.com/mraj602/watchyard/internal/timer"
)

// `wy desktop` (docs/spec/desktop.md C4): the desktop app's hub-side actions, one JSON object
// on stdout each. Secrets travel only on stdout and stdin, never in argv. Exit codes: 0 ok,
// 2 usage, 3 refused by the hub, 4 not possible here, 5 hub unreachable. The logic is in
// internal/desktop; this file reads the machine and prints.

const desktopUsage = `usage: wy desktop <command> [--json]

  info                             this machine, its hub link and the admin channel
  token [--code-stdin] [--name N]  a desktop (read) token for the app, printed once
  owner-link                       a one-time owner sign-in link
  hub status|start|stop            the hub user service on this machine

Every command prints one JSON object on stdout. Exit codes: 0 ok, 2 usage, 3 refused by
the hub, 4 not possible here (no local hub, no admin channel, not connected), 5 hub
unreachable. With --code-stdin the first line of stdin is a desktop pairing code (482-913)
or its secret; no secret is ever put in an argument.
`

// desktopStdin is what --code-stdin reads; tests replace it.
var desktopStdin io.Reader = os.Stdin

// desktopEnvHook lets tests replace parts of the environment (service manager, HTTP).
var desktopEnvHook func(*desktop.Env)

func runDesktop(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, desktopUsage)
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	sub, rest := args[0], args[1:]
	var action string
	switch sub {
	case "info", "token", "owner-link":
	case "hub":
		if len(rest) == 0 || (rest[0] != desktop.ActionStatus && rest[0] != desktop.ActionStart && rest[0] != desktop.ActionStop) {
			fmt.Fprint(stderr, desktopUsage)
			return errUsage
		}
		action, rest = rest[0], rest[1:]
	default:
		fmt.Fprintf(stderr, "wy desktop: unknown command %q\n", sub)
		fmt.Fprint(stderr, desktopUsage)
		return errUsage
	}

	fs := flag.NewFlagSet("desktop "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print compact JSON (the output is always JSON)")
	var codeStdin *bool
	var name *string
	if sub == "token" {
		codeStdin = fs.Bool("code-stdin", false, "claim the desktop pairing code (or secret) on the first line of stdin")
		name = fs.String("name", "", "device name (1 to 40 characters)")
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "wy desktop %s: unexpected argument %q\n", sub, pos[0])
		return errUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, err := osDesktopEnv()
	if err != nil {
		return err
	}
	var out any
	switch sub {
	case "info":
		out, err = desktop.RunInfo(ctx, env)
	case "token":
		out, err = desktop.RunToken(ctx, env, desktop.TokenOptions{CodeStdin: *codeStdin, Name: *name})
	case "owner-link":
		out, err = desktop.RunOwnerLink(ctx, env)
	case "hub":
		out, err = desktop.RunHub(ctx, env, action)
	}
	if err != nil {
		var de *desktop.Error
		if errors.As(err, &de) {
			if de.Code == desktop.ExitUsage {
				fmt.Fprintf(stderr, "wy desktop %s: %v\n", sub, de.Err)
				return errUsage
			}
			return exitWith(de.Code, de.Err)
		}
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if !*asJSON {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(out)
}

// osDesktopEnv reads this machine: folders, config, the admin channel of machines.md 4.4 (with
// ssh in BatchMode, so it never waits on a prompt nobody sees) and the hub user service.
func osDesktopEnv() (desktop.Env, error) {
	me, err := osMachineEnv()
	if err != nil {
		return desktop.Env{}, err
	}
	f, err := config.Load(me.dirs.Config.Path)
	if err != nil {
		return desktop.Env{}, err
	}
	cfg, err := f.Typed()
	if err != nil {
		return desktop.Env{}, err
	}
	// A bad machines.hub_admin must not break `info` (or the hub service actions): it is
	// reported there, and only token and owner-link, which need the channel, fail with it.
	admin, adminName, adminErr := adminChannel(me, "", cfg)
	if adminErr != nil {
		admin, adminName = nil, cfg.Machines.HubAdmin
	}
	if p, ok := admin.(machineadd.PrefixAdmin); ok {
		admin = desktop.BatchMode(p)
	}
	uname := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	svc := service.New(service.Env{GOOS: runtime.GOOS, Home: me.home, UID: os.Getuid(), User: uname, SyPath: me.exe,
		StateDir: me.dirs.State.Path, Getenv: me.getenv, Runner: timer.ExecRunner{}})
	env := desktop.Env{Dirs: me.dirs, Config: cfg, Admin: admin, AdminName: adminName, AdminErr: adminErr, Service: svc,
		GOOS: runtime.GOOS, Stdin: desktopStdin}
	if desktopEnvHook != nil {
		desktopEnvHook(&env)
	}
	return env, nil
}
