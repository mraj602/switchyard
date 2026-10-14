package peers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// Inbox socket facts (peers.md 8.1). Documented unless marked uncertain.
const (
	// MaxFrameBytes: Claude Code drops the connection for a line over about 1 MiB.
	MaxFrameBytes = 1 << 20
	// DialTimeout bounds the connect; the socket is local, so a slow one is a dead one.
	DialTimeout = 250 * time.Millisecond
	// WriteTimeout bounds the one write.
	WriteTimeout = 2 * time.Second
	// SocksDirName is the folder of inbox sockets under the runtime dir.
	SocksDirName = "cc-socks"
)

// Errors of the inbox writer.
var (
	// ErrTargetGone: nothing listens on the socket (session exited): peers.md 9.3 `target_gone`.
	ErrTargetGone = errors.New("the session's inbox socket is gone")
	// ErrFrameTooBig: the frame line is over MaxFrameBytes.
	ErrFrameTooBig = errors.New("frame is too big for Claude Code's inbox")
	// ErrUnsupported: this OS has no inbox sockets Switchyard can write (Windows).
	ErrUnsupported = errors.New("peers are not supported on this platform")
)

// Frame is one JSON line to an inbox socket (peers.md 8.5).
type Frame struct {
	MsgV      int          `json:"msgV"`
	MsgID     string       `json:"msg_id"`
	Type      string       `json:"type"`
	SessionID string       `json:"session_id"`
	Message   FrameMessage `json:"message"`
	Priority  string       `json:"priority"`
	From      string       `json:"from"`
}

// FrameMessage is the frame's message.
type FrameMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// NewFrame builds the type "user" frame for a wrapper. msgID is the Switchyard message id,
// sessionID the target's session id (a guard against a recycled pid), and the wrapper's
// From is repeated as the frame's `from`.
func NewFrame(msgID, sessionID string, w Wrapper) Frame {
	return Frame{
		MsgV: FrameMsgV, MsgID: msgID, Type: "user", SessionID: sessionID,
		Message:  FrameMessage{Role: "user", Content: w.String()},
		Priority: FramePriority, From: w.From,
	}
}

// Line is the frame as one newline-terminated JSON line, with no HTML escaping so the
// `<cross-session-message` text stays readable (Claude Code parses either form).
func (f Frame) Line() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if b.Len() > MaxFrameBytes {
		return nil, ErrFrameTooBig
	}
	return b.Bytes(), nil
}

// WriteFrame connects to the socket, writes the frame line and closes. The inbox never
// writes back. A refused or missing socket is ErrTargetGone.
func WriteFrame(sock string, f Frame) error {
	line, err := f.Line()
	if err != nil {
		return err
	}
	c, err := net.DialTimeout("unix", sock, DialTimeout)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %v", ErrTargetGone, err)
		}
		return err
	}
	defer c.Close()
	_ = c.SetWriteDeadline(time.Now().Add(WriteTimeout))
	_, err = c.Write(line)
	return err
}

// Alive reports whether something accepts a connection on sock within DialTimeout, without
// writing anything (peers.md 3.1: the 250 ms connect). Only ever call it with a socket from
// a registry file, never a guessed path.
func Alive(sock string) bool {
	c, err := net.DialTimeout("unix", sock, DialTimeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// SocksDir is the default inbox folder: <runtime>/cc-socks. **Uncertain** on macOS (peers.md
// 14: there the registry's messagingSocketPath is used, never this guess). runtime is
// $XDG_RUNTIME_DIR, or /run/user/<uid> when it is empty.
func SocksDir(runtime string) string {
	if runtime == "" {
		runtime = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return filepath.Join(runtime, SocksDirName)
}

// DefaultSocketPath is where a session with this pid listens when its registry file does not
// say otherwise.
func DefaultSocketPath(runtime string, pid int) string {
	return filepath.Join(SocksDir(runtime), strconv.Itoa(pid)+".sock")
}
