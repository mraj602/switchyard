package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/mraj602/switchyard/internal/agent"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/paths"
	peerspkg "github.com/mraj602/switchyard/internal/peers"
	"github.com/mraj602/switchyard/internal/service"
	"github.com/mraj602/switchyard/internal/timer"
	"github.com/mraj602/switchyard/internal/version"
)

const agentUsage = `usage: sy agent enable | disable | status
       sy agent run          (the foreground process the service runs)

The agent is the per-machine service behind sy peers (docs/spec/peers.md 2).
`

// Seams. Tests replace them; the defaults are the real thing.
var (
	// agentServiceEnv is the service manager environment of enable, disable and status.
	agentServiceEnv = func(stderr io.Writer) (service.Env, error) {
		pe := paths.FromOS()
		d, err := paths.Resolve(pe)
		if err != nil {
			return service.Env{}, err
		}
		if pe.Home == "" {
			return service.Env{}, errors.New("cannot find the home folder")
		}
		sy, err := os.Executable()
		if err != nil {
			return service.Env{}, err
		}
		if r, err := filepath.EvalSymlinks(sy); err == nil {
			sy = r
		}
		uname := os.Getenv("USER")
		if u, err := user.Current(); err == nil {
			uname = u.Username
		}
		return service.Env{GOOS: runtime.GOOS, Home: pe.Home, UID: os.Getuid(), User: uname, SyPath: sy,
			StateDir: d.State.Path, Getenv: os.Getenv, Runner: timer.ExecRunner{}, Out: stderr}, nil
	}
	// agentContext is the context of `sy agent run`: cancelled by SIGINT or SIGTERM.
	agentContext = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
	// agentPeerCred overrides how the agent reads a caller's pid (tests; nil: the real one).
	agentPeerCred func(*net.UnixConn) (int, int, error)
	// agentLink is the link client (link.md), nil until that package exists. `sy agent run`
	// passes it to agent.Run.
	agentLink agent.LinkClient
	// agentKeysStep is what `sy agent enable` does about device keys before installing the
	// service (peers.md 2.1: make them, register them, print the fingerprints). Nil until
	// the keys package provides it.
	agentKeysStep func(ctx context.Context, stdout io.Writer) error
)

func agentSocket(e peerspkg.Env) string {
	return agent.SocketPath(runtime.GOOS, e.Runtime, e.State)
}

func runAgent(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, agentUsage)
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	if runtime.GOOS == "windows" {
		return peersExit(peerspkg.ExitUnsupported, errors.New("sy agent is not supported on Windows (Claude Code's inbox sockets need a per-session token that Switchyard never reads)"))
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "sy agent %s: unexpected argument %q\n", args[0], args[1])
		return errUsage
	}
	switch args[0] {
	case "run":
		return agentRun(stderr)
	case "enable":
		return agentEnable(stdout, stderr)
	case "disable":
		return agentDisable(stdout, stderr)
	case "status":
		return agentStatus(stdout, stderr)
	}
	fmt.Fprintf(stderr, "sy agent: unknown subcommand %q\n\n%s", args[0], agentUsage)
	return errUsage
}

// agentRun is the foreground process the service runs. It never touches the user's settings.
func agentRun(stderr io.Writer) error {
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	ctx, stop := agentContext()
	defer stop()
	fmt.Fprintf(stderr, "sy agent: listening on %s\n", agentSocket(pe.Env))
	return agent.Run(ctx, agent.Config{
		Socket: agentSocket(pe.Env),
		Load: func() (peerspkg.Env, bool, error) {
			p, err := peersEnv()
			return p.Env, p.Enabled, err
		},
		Link:     agentLink,
		LogPath:  filepath.Join(pe.Env.State, "logs", "agent.log"),
		Version:  version.Version,
		PeerCred: agentPeerCred,
	})
}

// setPeersEnabled changes `[peers] enabled` in config.toml.
func setPeersEnabled(on bool) error {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return err
	}
	f, err := config.Load(d.Config.Path)
	if err != nil {
		return err
	}
	v := "false"
	if on {
		v = "true"
	}
	if err := f.Set("peers.enabled", []string{v}); err != nil {
		return err
	}
	return f.Save()
}

func agentEnable(stdout, stderr io.Writer) error {
	se, err := agentServiceEnv(stderr)
	if err != nil {
		return err
	}
	if agentKeysStep != nil {
		if err := agentKeysStep(context.Background(), stdout); err != nil {
			return err
		}
	}
	if err := setPeersEnabled(true); err != nil {
		return err
	}
	if err := service.New(se).Install(context.Background(), service.AgentSpec()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "sy agent installed and started (%s); peers are enabled.\n", service.UnitName(se.GOOS, service.AgentName))
	return nil
}

func agentDisable(stdout, stderr io.Writer) error {
	se, err := agentServiceEnv(stderr)
	if err != nil {
		return err
	}
	if err := service.New(se).Uninstall(context.Background(), service.AgentName); err != nil {
		return err
	}
	if err := setPeersEnabled(false); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "sy agent stopped and removed; peers are disabled. Keys stay.")
	return nil
}

func agentStatus(stdout, stderr io.Writer) error {
	se, err := agentServiceEnv(stderr)
	if err != nil {
		return err
	}
	st, err := service.New(se).Status(context.Background(), service.AgentName)
	if err != nil {
		return err
	}
	switch {
	case !st.Installed:
		fmt.Fprintln(stdout, "service: not installed (sy agent enable)")
	case st.Active:
		fmt.Fprintf(stdout, "service: installed, active (%s)\n", service.UnitName(se.GOOS, service.AgentName))
	default:
		fmt.Fprintf(stdout, "service: installed, not active (%s)\n", service.UnitName(se.GOOS, service.AgentName))
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	rep, err := agent.Call(agentSocket(pe.Env), agent.Request{Op: agent.OpState}, 3*time.Second)
	switch {
	case errors.Is(err, agent.ErrNoAgent):
		fmt.Fprintln(stdout, "agent: not running")
	case err != nil:
		fmt.Fprintf(stdout, "agent: %v\n", err)
	case rep.State == nil:
		fmt.Fprintf(stdout, "agent: %s\n", rep.Refusal())
	default:
		fmt.Fprintf(stdout, "agent: running, pid %d, since %s, link %s\n", rep.State.PID, rep.State.Started, rep.State.Link.State)
		fmt.Fprintf(stdout, "socket: %s\n", rep.State.Socket)
	}
	return nil
}
