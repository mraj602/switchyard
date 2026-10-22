//go:build fakeinbox

// Package fakeinbox is a test double of Claude Code's inbox socket (peers.md 8.1, 15): it
// listens on a Unix socket, reads JSON lines, applies the gate Claude Code applies, and
// records what it admitted, held and dropped. It only exists under the `fakeinbox` build
// tag, and scripts/ci/check-release-seams.sh proves a release binary has none of it.
//
// The gate is what the spec says (and what Claude Code 2.1.300 shows in its program), with
// every undocumented rule in one function (Gate) for the lab checks (peers.md 13) to adjust.
package fakeinbox

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/peers"
)

// SeamMarker is a string only this package contains; the release check looks for it.
const SeamMarker = "SY_FAKE_INBOX_SEAM"

var _ = SeamMarker

// Outcome is what the gate decided for one frame.
type Outcome string

const (
	Deliver Outcome = "deliver" // admitted without a dialog
	Hold    Outcome = "hold"    // waits for the user's approval
	Drop    Outcome = "drop"    // thrown away
)

// Setting is the receiver's crossSessionInbound setting.
type Setting string

const (
	SettingDefault Setting = ""
	SettingAccept  Setting = "accept" // admit everything (fallback A)
	SettingHold    Setting = "hold"   // hold everything
	SettingRefuse  Setting = "refuse" // managed setting: drop everything
)

// Config is the receiver the fake stands for.
type Config struct {
	Path      string             // socket path to listen on
	SessionID string             // the receiver's session id
	Mode      peers.Mode         // the receiver's permission class
	Setting   Setting            // crossSessionInbound
	IsChild   func(pid int) bool // true when the writer is a descendant of the receiver (fallback B)
	Now       func() time.Time
	// FirstLineWait is how long a connection may stay silent before it is closed (30 s in
	// Claude Code).
	FirstLineWait time.Duration
}

// Received is one frame and what happened to it.
type Received struct {
	Frame   peers.Frame
	Raw     []byte
	Wrapper peers.Wrapper
	Canon   bool // the content was a canonical wrapper
	Outcome Outcome
	Reason  string
	PeerPID int
}

// Limits of Claude Code's inbox (peers.md 9.2).
const (
	Burst         = 30
	RefillPerSec  = 0.5
	DuplicateWait = 30 * time.Second
)

// Gate is the decision for one frame. Order: a bad frame or a wrong session id is dropped;
// the limits and the duplicate check come next; then own child and the setting; then, with
// the default setting, the from-mode of a canonical wrapper must equal the receiver's class
// (a mismatch is held, and so is a missing mode in a bypass receiver; a plain frame to a
// prompting receiver is admitted, lab E1, E2). Inferred from the program, not documented (E3, E4, E5, E6).
func Gate(c Config, f peers.Frame, w peers.Wrapper, canon, child bool) (Outcome, string) {
	if f.Type != "user" {
		return Drop, "not_user"
	}
	if f.SessionID != "" && f.SessionID != c.SessionID {
		return Drop, "session_mismatch"
	}
	if child {
		return Deliver, "own_child"
	}
	switch c.Setting {
	case SettingAccept:
		return Deliver, "explicit_setting"
	case SettingHold:
		return Hold, "explicit_setting"
	case SettingRefuse:
		return Drop, "managed_setting"
	}
	switch {
	case !canon || w.FromMode == "":
		if c.Mode == peers.ModeBypass {
			return Hold, "no_mode_asserted"
		}
		return Deliver, "prompting_default"
	case w.FromMode == c.Mode:
		return Deliver, "mode_match"
	}
	return Hold, "mode_mismatch"
}

// Server is a running fake inbox.
type Server struct {
	cfg Config
	ln  *net.UnixListener

	mu       sync.Mutex
	got      []Received
	tokens   float64
	lastFill time.Time
	seen     map[string]time.Time
	changed  chan struct{}
	wg       sync.WaitGroup
}

// Listen starts the fake on cfg.Path.
func Listen(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.FirstLineWait == 0 {
		cfg.FirstLineWait = 30 * time.Second
	}
	_ = os.Remove(cfg.Path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.Path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, ln: ln, tokens: Burst, lastFill: cfg.Now(), seen: map[string]time.Time{}, changed: make(chan struct{}, 1)}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Close stops the server and removes the socket.
func (s *Server) Close() {
	s.ln.Close()
	s.wg.Wait()
	_ = os.Remove(s.cfg.Path)
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.AcceptUnix()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(c)
		}()
	}
}

func (s *Server) serve(c *net.UnixConn) {
	defer c.Close()
	pid, _, _ := peers.PeerPID(c)
	_ = c.SetReadDeadline(time.Now().Add(s.cfg.FirstLineWait))
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), peers.MaxFrameBytes)
	for sc.Scan() {
		s.handle(append([]byte(nil), sc.Bytes()...), pid)
	}
	// A line over the limit makes Scan fail: the connection is dropped, nothing recorded.
}

func (s *Server) handle(raw []byte, pid int) {
	r := Received{Raw: raw, PeerPID: pid}
	if err := json.Unmarshal(raw, &r.Frame); err != nil {
		r.Outcome, r.Reason = Drop, "bad_json"
		s.record(r)
		return
	}
	r.Wrapper, r.Canon = peers.ParseWrapper(r.Frame.Message.Content)
	now := s.cfg.Now()
	s.mu.Lock()
	s.tokens += now.Sub(s.lastFill).Seconds() * RefillPerSec
	if s.tokens > Burst {
		s.tokens = Burst
	}
	s.lastFill = now
	limited := s.tokens < 1
	if !limited {
		s.tokens--
	}
	dup := false
	if t, ok := s.seen[r.Frame.Message.Content]; ok && now.Sub(t) < DuplicateWait {
		dup = true
	}
	s.seen[r.Frame.Message.Content] = now
	s.mu.Unlock()
	switch {
	case limited:
		r.Outcome, r.Reason = Drop, "rate_limited"
	case dup:
		r.Outcome, r.Reason = Drop, "duplicate"
	default:
		child := s.cfg.IsChild != nil && pid > 0 && s.cfg.IsChild(pid)
		r.Outcome, r.Reason = Gate(s.cfg, r.Frame, r.Wrapper, r.Canon, child)
	}
	s.record(r)
}

func (s *Server) record(r Received) {
	s.mu.Lock()
	s.got = append(s.got, r)
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// All returns every received frame, oldest first.
func (s *Server) All() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.got...)
}

// By returns the frames with one outcome.
func (s *Server) By(o Outcome) []Received {
	var out []Received
	for _, r := range s.All() {
		if r.Outcome == o {
			out = append(out, r)
		}
	}
	return out
}

// Wait blocks until at least n frames are recorded, or the timeout passes; it returns
// whether they arrived.
func (s *Server) Wait(n int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		if len(s.All()) >= n {
			return true
		}
		select {
		case <-s.changed:
		case <-deadline:
			return len(s.All()) >= n
		case <-time.After(10 * time.Millisecond):
		}
	}
}
