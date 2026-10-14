package doctor

import (
	"fmt"
	"runtime"
	"time"

	"github.com/mraj602/switchyard/internal/agent"
	"github.com/mraj602/switchyard/internal/config"
)

// checkAgent reports the peers agent (peers.md 2) when peers are enabled. A machine with
// peers on and no agent still works on the direct same-machine path, so a missing agent is a
// warning, not a failure. It only connects to the agent's own control socket.
func checkAgent(rep *Report, o Options, cfg config.Config) {
	if !cfg.Peers.Enabled || runtime.GOOS == "windows" {
		return
	}
	xdg := ""
	if o.Getenv != nil {
		xdg = o.Getenv("XDG_RUNTIME_DIR")
	}
	sock := agent.SocketPath(runtime.GOOS, xdg, o.Dirs.State.Path)
	rep2, err := agent.Call(sock, agent.Request{Op: agent.OpState}, 2*time.Second)
	switch {
	case err == nil && rep2.State != nil:
		rep.add(Check{Name: "agent", Level: OK, Detail: fmt.Sprintf("running (pid %d), link: %s", rep2.State.PID, rep2.State.Link.State)})
	case err == nil:
		rep.add(Check{Name: "agent", Level: Warn, Detail: "answers on its socket but gave no state"})
	default:
		rep.add(Check{Name: "agent", Level: Warn, Detail: "peers are enabled but the sy agent is not running (same-machine sends write the inbox directly)",
			Fix: "sy agent enable"})
	}
}
