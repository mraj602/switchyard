// Package ntfytest is a stub ntfy server for tests (push.md 11.3). It records every JSON
// message published to its root, checks the Bearer token when told to, answers scripted
// statuses, and serves recorded messages back on GET /<topic>/json?poll=1 like ntfy does.
// It shares no code with the ntfy package.
package ntfytest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Action is a published action button.
type Action struct {
	Action  string            `json:"action"`
	Label   string            `json:"label"`
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Clear   bool              `json:"clear"`
}

// Message is a published message.
type Message struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click"`
	Actions  []Action `json:"actions"`
}

// Received is one publish request.
type Received struct {
	Header http.Header
	Raw    []byte
	Msg    Message
	// Err is set when the request was not a valid ntfy publish (not POST /, bad JSON, over
	// 4096 bytes, no topic).
	Err error
}

// Reply is one scripted answer.
type Reply struct {
	Status     int // 0 means 200
	RetryAfter string
	Body       string
	Delay      time.Duration
}

// Server is the stub.
type Server struct {
	srv *httptest.Server

	mu     sync.Mutex
	token  string
	script []Reply
	got    []Received
}

// New starts the stub on a loopback port (plain http).
func New() *Server {
	s := &Server{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the stub.
func (s *Server) Close() { s.srv.Close() }

// URL is the server URL (http://127.0.0.1:port).
func (s *Server) URL() string { return s.srv.URL }

// RequireToken makes the stub answer 401 unless the request carries this Bearer token
// ("" turns the check off).
func (s *Server) RequireToken(tok string) {
	s.mu.Lock()
	s.token = tok
	s.mu.Unlock()
}

// Script queues answers, one per request in order; an empty queue answers 200.
func (s *Server) Script(r ...Reply) {
	s.mu.Lock()
	s.script = append(s.script, r...)
	s.mu.Unlock()
}

// Requests returns a copy of everything published so far.
func (s *Server) Requests() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.got...)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") {
		s.poll(w, r)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	rec := Received{Header: r.Header.Clone(), Raw: body}
	switch {
	case r.Method != http.MethodPost || r.URL.Path != "/":
		rec.Err = fmt.Errorf("%s %s is not a publish to the server root", r.Method, r.URL.Path)
	case len(body) > 4096:
		rec.Err = fmt.Errorf("message is %d bytes, limit 4096", len(body))
	case !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json"):
		rec.Err = fmt.Errorf("Content-Type %q", r.Header.Get("Content-Type"))
	default:
		if err := json.Unmarshal(body, &rec.Msg); err != nil {
			rec.Err = err
		} else if rec.Msg.Topic == "" {
			rec.Err = fmt.Errorf("no topic")
		}
	}

	s.mu.Lock()
	var reply Reply
	if len(s.script) > 0 {
		reply, s.script = s.script[0], s.script[1:]
	}
	want := s.token
	s.got = append(s.got, rec)
	idx := len(s.got) - 1
	s.mu.Unlock()

	if reply.Delay > 0 {
		select {
		case <-time.After(reply.Delay):
		case <-r.Context().Done():
			return
		}
	}
	status := reply.Status
	if status == 0 {
		status = http.StatusOK
		switch {
		case want != "" && r.Header.Get("Authorization") != "Bearer "+want:
			status = http.StatusUnauthorized
		case rec.Err != nil:
			status = http.StatusBadRequest
		}
	}
	if reply.RetryAfter != "" {
		w.Header().Set("Retry-After", reply.RetryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if reply.Body != "" {
		_, _ = io.WriteString(w, reply.Body)
	} else if status == http.StatusOK {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("stub%d", idx), "event": "message", "topic": rec.Msg.Topic})
	}
}

// poll serves GET /<topic>/json?poll=1: the accepted messages of the topic, one JSON object
// per line.
func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	topic := strings.Trim(strings.TrimSuffix(r.URL.Path, "/json"), "/")
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	for i, g := range s.Requests() {
		if g.Err == nil && g.Msg.Topic == topic {
			_ = enc.Encode(map[string]any{"id": fmt.Sprintf("stub%d", i), "event": "message", "topic": topic,
				"title": g.Msg.Title, "message": g.Msg.Message, "priority": g.Msg.Priority, "tags": g.Msg.Tags,
				"click": g.Msg.Click, "actions": g.Msg.Actions})
		}
	}
}
