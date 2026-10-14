package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mraj602/switchyard/internal/peers"
)

// Ops of the control socket (peers.md 2.3).
const (
	OpWhoami = "whoami"
	OpList   = "list"
	OpSend   = "send"
	OpOff    = "off"
	OpOn     = "on"
	OpState  = "state"
)

// MaxLine bounds one request line.
const MaxLine = 1 << 20

// Request is one JSON line to the control socket.
type Request struct {
	Op        string `json:"op"`
	ClaudePID int    `json:"claude_pid,omitempty"`
	ConfigDir string `json:"config_dir,omitempty"` // informational: the agent derives it from the process
	All       bool   `json:"all,omitempty"`
	To        string `json:"to,omitempty"`
	Text      string `json:"text,omitempty"`
	TTLs      int    `json:"ttl_s,omitempty"`
	WaitS     int    `json:"wait_s,omitempty"`
}

// Error is the refusal in a reply: Code is a peers refusal code.
type Error struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// SessionInfo is the caller's session (whoami).
type SessionInfo struct {
	Session   string `json:"session"`
	PID       int    `json:"pid"`
	ConfigDir string `json:"config_dir"`
}

// State is the reply of `state`, for `sy agent status`.
type State struct {
	PID     int        `json:"pid"`
	Started string     `json:"started"`
	Version string     `json:"version,omitempty"`
	Socket  string     `json:"socket"`
	Link    LinkStatus `json:"link"`
}

// Reply is one JSON line back. A send fills Msg, Thread, Status ("delivered"), To (the
// target's handle) and HeldLikely; a refusal fills only Error.
type Reply struct {
	Error      *Error            `json:"error,omitempty"`
	OK         bool              `json:"ok,omitempty"`
	Msg        string            `json:"msg,omitempty"`
	Thread     string            `json:"thread,omitempty"`
	Status     string            `json:"status,omitempty"`
	Detail     string            `json:"detail,omitempty"`
	To         string            `json:"to,omitempty"`
	HeldLikely bool              `json:"held_likely,omitempty"`
	Session    *SessionInfo      `json:"session,omitempty"`
	Entries    []peers.ListEntry `json:"entries,omitempty"`
	Asked      bool              `json:"asked,omitempty"`
	State      *State            `json:"state,omitempty"`
}

// Refusal is the reply's error as a *peers.Refusal, or nil.
func (r Reply) Refusal() *peers.Refusal {
	if r.Error == nil {
		return nil
	}
	return &peers.Refusal{Code: r.Error.Code, Detail: r.Error.Detail}
}

// SocketPath is the control socket (peers.md 2.3): $XDG_RUNTIME_DIR/sy/agent.sock on Linux,
// <state>/agent/agent.sock on macOS.
func SocketPath(goos, runtime, state string) string {
	if goos == "darwin" {
		return filepath.Join(state, "agent", "agent.sock")
	}
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return filepath.Join(runtime, "sy", "agent.sock")
}

// ErrNoAgent means nothing is listening on the control socket: the caller falls back to the
// direct path (peers.md 8.5 "PE1 without the agent").
var ErrNoAgent = errors.New("the sy agent is not running")

// Call sends one request and reads the reply. ErrNoAgent when the socket is missing or
// refuses; any other error happened after the connection was made, so the request may have
// been handled and must not be repeated by another path.
func Call(sock string, req Request, timeout time.Duration) (Reply, error) {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTDIR) {
			return Reply{}, ErrNoAgent
		}
		return Reply{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	b, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return Reply{}, err
	}
	line, err := bufio.NewReaderSize(io.LimitReader(c, MaxLine), 64<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Reply{}, fmt.Errorf("the sy agent closed the connection: %w", err)
	}
	var rep Reply
	if err := json.Unmarshal(line, &rep); err != nil {
		return Reply{}, fmt.Errorf("bad reply from the sy agent: %w", err)
	}
	return rep, nil
}
