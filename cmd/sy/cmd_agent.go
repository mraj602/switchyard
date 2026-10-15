package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/mraj602/switchyard/internal/agent"
	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/linkclient"
	"github.com/mraj602/switchyard/internal/localtime"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/peerlink"
	peerspkg "github.com/mraj602/switchyard/internal/peers"
	"github.com/mraj602/switchyard/internal/service"
	"github.com/mraj602/switchyard/internal/timer"
	"github.com/mraj602/switchyard/internal/version"
)

const agentUsage = `usage: sy agent enable [--repin-hub] | disable | status
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
	// agentLink overrides the link client (tests); nil: `sy agent run` builds the real one
	// from hub.url (newAgentLink).
	agentLink agent.LinkClient
	// agentKeysStep is what `sy agent enable` does about device keys before installing the
	// service (peers.md 2.1: make them, register them, pin the hub key, print the
	// fingerprints); repin is --repin-hub. keys_link.go sets the real one; nil skips it.
	agentKeysStep func(ctx context.Context, stdout io.Writer, repin bool) error
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
	repin := args[0] == "enable" && len(args) == 2 && args[1] == "--repin-hub"
	if len(args) > 1 && !repin {
		fmt.Fprintf(stderr, "sy agent %s: unexpected argument %q\n", args[0], args[1])
		return errUsage
	}
	switch args[0] {
	case "run":
		return agentRun(stderr)
	case "enable":
		return agentEnable(stdout, stderr, repin)
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
	lc := agentLink
	// Sessions on other machines are reached through the link (peers.md 8.2-8.4).
	remote := peerlink.New(pe.Env, log.New(stderr, "sy agent: ", log.LstdFlags))
	remote.HubMayWiden = !pe.NoHubWiden // a hub-signed thread resume widens (keys.md 5.4)
	if lc == nil {
		var c *linkclient.Client
		if c, err = newAgentLink(pe.Env.State, remote, stderr); err != nil {
			fmt.Fprintf(stderr, "sy agent: no link to the hub: %v\n", err)
		}
		if c != nil {
			lc, remote.Link = c, c
		}
	} else if s, ok := lc.(peerlink.Sender); ok {
		remote.Link = s
	}
	var rm peerspkg.Remote
	if remote.Link != nil {
		rm = remote
	}
	return agent.Run(ctx, agent.Config{
		Socket: agentSocket(pe.Env),
		Load: func() (peerspkg.Env, bool, error) {
			p, err := peersEnv()
			return p.Env, p.Enabled, err
		},
		Link:     lc,
		Remote:   rm,
		LogPath:  filepath.Join(pe.Env.State, "logs", "agent.log"),
		Version:  version.Version,
		PeerCred: agentPeerCred,
	})
}

// newAgentLink builds the machine link client from config hub.url and hub.dial (link.md:
// machines always use their configured hub address, never the welcome's urls). (nil, nil)
// when no hub is configured.
func newAgentLink(state string, ph linkclient.PeerHandler, stderr io.Writer) (*linkclient.Client, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return nil, err
	}
	cfg := config.Defaults()
	if f, err := config.Load(d.Config.Path); err == nil {
		cfg, _ = f.Typed()
	}
	if cfg.Hub.URL == "" {
		return nil, nil
	}
	u, err := linkclient.HubURL(cfg.Hub.URL)
	if err != nil {
		return nil, err
	}
	var hc *http.Client
	if cfg.Hub.Dial != "" {
		if hc, err = client.HTTPClient(cfg.Hub.Dial, 0); err != nil {
			return nil, err
		}
	}
	return linkclient.New(linkclient.Config{State: state, URL: u, HTTP: hc, HubMayWiden: cfg.Peers.HubMayWiden, Peers: ph,
		Version: version.Version, Log: log.New(stderr, "sy agent: ", log.LstdFlags)}), nil
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

// agentEnable is `sy agent enable [--repin-hub]` (peers.md 2.1): the keys step, then peers
// on and the service.
func agentEnable(stdout, stderr io.Writer, repin bool) error {
	se, err := agentServiceEnv(stderr)
	if err != nil {
		return err
	}
	if agentKeysStep != nil {
		if err := agentKeysStep(context.Background(), stdout, repin); err != nil {
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
		fmt.Fprintf(stdout, "agent: running, pid %d, since %s, link %s\n", rep.State.PID, localtime.String(rep.State.Started), rep.State.Link.State)
		fmt.Fprintf(stdout, "socket: %s\n", rep.State.Socket)
	}
	return nil
}
