package hook

import (
	"encoding/json"
	"io"

	"github.com/mraj602/watchyard/internal/clientenv"
	"github.com/mraj602/watchyard/internal/peers"
)

// PeersContextFlag is the argument that turns `wy hook` into the SessionStart context entry:
// `wy hook --peers-context SessionStart` (docs/spec/peers.md 2.5).
const PeersContextFlag = "--peers-context"

// sessionStartOutput is Claude Code's SessionStart hook JSON output: additionalContext is
// added to the session's context.
type sessionStartOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// RunPeersContext is `wy hook --peers-context <Event>`: on SessionStart, when [peers] enabled
// is true and the local matrix lets this session's account reach another account, it prints
// the SessionStart hook JSON with peers.StartContextText as additionalContext; otherwise it
// prints nothing. It reads only local files (config, the slot's cached login, the off file,
// the accepted matrix), never connects to anything, never writes a record, and swallows every
// error and panic (the hook is synchronous, with a 1 s timeout). args are the arguments after
// the flag.
func RunPeersContext(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) {
	defer func() { _ = recover() }()
	in, err := readStdinWait(stdin, titleStdinWait)
	if err != nil {
		return
	}
	var p struct {
		SessionID string `json:"session_id"`
		Event     string `json:"hook_event_name"`
	}
	if json.Unmarshal(in, &p) != nil {
		return
	}
	event := p.Event
	if len(args) > 0 && args[0] != "" {
		event = args[0]
	}
	if event != "SessionStart" {
		return
	}
	env, err := clientenv.Load(getenv)
	if err != nil || !env.Config.Peers.Enabled || !env.Login.HasSeat {
		return
	}
	text := peers.StartContext(env.Dirs.State.Path, env.Login.Seat.Account)
	if text == "" {
		return
	}
	var out sessionStartOutput
	out.HookSpecificOutput.HookEventName = "SessionStart"
	out.HookSpecificOutput.AdditionalContext = text
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	_, _ = stdout.Write(append(b, '\n'))
}
