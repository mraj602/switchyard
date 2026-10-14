package hook

import (
	"os"
	"time"

	"github.com/mraj602/switchyard/internal/clientenv"
	"github.com/mraj602/switchyard/internal/peers"
)

// recordPeers writes the session's peers file (<state>/peers/sessions/<session>.json,
// peers.md 2.4) when [peers] enabled is true. It does one small read and at most one atomic
// write, never connects to anything and never waits (hooks never wait). The prompt is only
// compared against the peer wrapper prefix and is never stored. Errors are logged, never
// returned.
func recordPeers(env *clientenv.Env, payload []byte, getenv func(string) string, event string) {
	if !env.Config.Peers.Enabled {
		return
	}
	ev, ok := peers.ParseHookEvent(payload)
	if !ok {
		return
	}
	proc := peers.NewProc()
	_, err := peers.UpdateSessionFile(peers.HookContext{
		State:     env.Dirs.State.Path,
		ConfigDir: env.Slot.Name,
		Now:       time.Now(),
		// The nearest Claude-looking ancestor of the hook (keys.md 8's walk). Only asked on
		// SessionStart or when the file is new.
		FindPID: func() int { return peers.NearestClaude(proc, os.Getppid()) },
	}, ev)
	if err != nil {
		logError(env, getenv, event, err)
	}
}
