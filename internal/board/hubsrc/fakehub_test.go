package hubsrc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/board"
)

// fakeHub speaks the slice of the hub API the board reads.
type fakeHub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	token    string
	seats    []map[string]any
	sessions []map[string]any
	machines []map[string]any
	streamID int64
	status   int // when non-zero, every request answers it (401 for a revoked device)
	redirect string
	bigReply bool
	page2    []map[string]any // when set, /v1/sessions has a second page behind cursor=p2
	reqs     []reqLog
	streams  []*fakeStream
	hello    bool
}

type reqLog struct {
	Path, Query, LastEventID, Auth string
}

type fakeStream struct {
	ch   chan string
	done chan struct{}
	once sync.Once
}

func (s *fakeStream) close() { s.once.Do(func() { close(s.done) }) }

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{t: t, token: "sy_d_s3cret", hello: true}
	h.seats = []map[string]any{
		{"id": "s_work", "label": "work", "colour": "#f59e0b", "hidden": false, "windows": []any{
			map[string]any{"key": "five_hour", "used_pct": 41.0, "resets_at": "2099-01-01T15:10:00.000Z"},
			map[string]any{"key": "seven_day", "used_pct": 30.0, "resets_at": "2099-01-05T15:10:00.000Z"}}},
		{"id": "s_home", "label": "home", "colour": "#3b82f6", "hidden": false, "windows": []any{}},
	}
	base := time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
	h.sessions = []map[string]any{
		sess("c1", "asked", "question", true, "s_work", "work", "#f59e0b", "m_me", "laptop", "api", base, "Keep the old column?"),
		sess("c2", "working", "", false, "s_work", "work", "#f59e0b", "m_other", "gpu-box", "web", base.Add(time.Minute), ""),
		sess("c3", "done", "", false, "s_home", "home", "#3b82f6", "m_me", "laptop", "docs", base.Add(2*time.Minute), ""),
	}
	h.machines = []map[string]any{{"id": "m_me", "name": "laptop"}, {"id": "m_other", "name": "gpu-box"}}
	h.streamID = 7
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.close)
	return h
}

func sess(id, state, detail string, needs bool, seat, label, colour, machine, mname, project string, since time.Time, question string) map[string]any {
	m := map[string]any{"id": id, "state": state, "needs_you": needs, "state_since": since.Format("2006-01-02T15:04:05.000Z"),
		"seat": seat, "seat_label": label, "seat_colour": colour, "machine": machine, "machine_name": mname, "project": project,
		"last_event_at": since.Format("2006-01-02T15:04:05.000Z"), "resume": "claude --resume " + id}
	if detail != "" {
		m["detail"] = detail
	}
	if question != "" {
		m["question"] = question
	}
	return m
}

func (h *fakeHub) close() {
	h.mu.Lock()
	for _, s := range h.streams {
		s.close()
	}
	h.mu.Unlock()
	h.srv.Close()
}

func (h *fakeHub) log(r *http.Request) {
	h.reqs = append(h.reqs, reqLog{Path: r.URL.Path, Query: r.URL.RawQuery, LastEventID: r.Header.Get("Last-Event-ID"), Auth: r.Header.Get("Authorization")})
}

func (h *fakeHub) requests(path string) []reqLog {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []reqLog
	for _, r := range h.reqs {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.log(r)
	status, redirect, token := h.status, h.redirect, h.token
	big := h.bigReply
	h.mu.Unlock()
	if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusFound)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.URL.Path {
	case "/v1/overview":
		writeJSON(w, map[string]any{"time": "2026-10-20T14:06:00.000Z", "seats": h.seats, "needs_you": []any{},
			"machines": h.machines, "counts": map[string]any{"needs_you": 1}, "stream_id": h.streamID})
	case "/v1/accounts":
		writeJSON(w, map[string]any{"accounts": h.seats})
	case "/v1/sessions":
		if big {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"sessions":[],"pad":"%s"}`, strings.Repeat("x", MaxReply+100))
			return
		}
		if h.page2 != nil {
			if r.URL.Query().Get("cursor") == "p2" {
				writeJSON(w, map[string]any{"sessions": h.page2, "next": nil})
			} else {
				writeJSON(w, map[string]any{"sessions": h.sessions, "next": "p2"})
			}
			return
		}
		writeJSON(w, map[string]any{"sessions": h.sessions, "next": nil})
	case "/v1/stream":
		st := &fakeStream{ch: make(chan string, 64), done: make(chan struct{})}
		h.streams = append(h.streams, st)
		h.mu.Unlock()
		defer h.mu.Lock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		if h.hello {
			fmt.Fprint(w, "event: hello\ndata: {\"version\":\"x\"}\n\n")
		}
		fl.Flush()
		for {
			select {
			case s := <-st.ch:
				fmt.Fprint(w, s)
				fl.Flush()
			case <-st.done:
				return
			case <-r.Context().Done():
				return
			}
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// push sends a raw SSE chunk to the newest stream.
func (h *fakeHub) push(raw string) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		n := len(h.streams)
		var st *fakeStream
		if n > 0 {
			st = h.streams[n-1]
		}
		h.mu.Unlock()
		if st != nil {
			select {
			case <-st.done:
			default:
				st.ch <- raw
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("no open stream to push to")
}

func (h *fakeHub) event(id int64, typ string, data any) {
	b, _ := json.Marshal(data)
	h.push("id: " + strconv.FormatInt(id, 10) + "\nevent: " + typ + "\ndata: " + string(b) + "\n\n")
}

// dropStreams closes every open stream from the hub side.
func (h *fakeHub) dropStreams() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.streams {
		s.close()
	}
}

func (h *fakeHub) streamCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.streams)
}

func (h *fakeHub) set(f func(h *fakeHub)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(h)
}

// fakeLocal is a board.Source the test drives.
type fakeLocal struct {
	ch        chan board.View
	refreshes int
	mu        sync.Mutex
}

func newFakeLocal() *fakeLocal { return &fakeLocal{ch: make(chan board.View, 4)} }

func (f *fakeLocal) Start(ctx context.Context) (<-chan board.View, error) { return f.ch, nil }
func (f *fakeLocal) Refresh()                                             { f.mu.Lock(); f.refreshes++; f.mu.Unlock() }

// start runs a Source against the fake hub with fast timings and returns the views.
func startSource(t *testing.T, h *fakeHub, mod func(*Options)) (*Source, <-chan board.View, context.CancelFunc) {
	t.Helper()
	o := Options{
		URL: h.srv.URL, Token: h.token, Machine: board.Machine{ID: "m_me", Name: "laptop"},
		Backoff: func(int) time.Duration { return 20 * time.Millisecond },
		Loc:     time.UTC,
		Now:     func() time.Time { return time.Date(2026, 10, 20, 14, 6, 0, 0, time.UTC) },
	}
	if mod != nil {
		mod(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, err := s.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s, ch, cancel
}

// waitView returns the first View for which ok is true, within d.
func waitView(t *testing.T, ch <-chan board.View, d time.Duration, ok func(board.View) bool) board.View {
	t.Helper()
	deadline := time.After(d)
	var last board.View
	for {
		select {
		case v, open := <-ch:
			if !open {
				t.Fatalf("view channel closed; last view: %+v", last)
			}
			last = v
			if ok(v) {
				return v
			}
		case <-deadline:
			b, _ := json.Marshal(last)
			t.Fatalf("no matching View within %v; last: %s", d, b)
		}
	}
}
