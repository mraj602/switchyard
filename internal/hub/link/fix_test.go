package hublink

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/link/linktest"
)

var t0 = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type fix struct {
	t    *testing.T
	ctx  context.Context
	clk  *clock
	st   *store.Store
	ks   *hubkeys.Service
	h    *Hub
	srv  *httptest.Server
	mux  *swapHandler
	logs *syncBuf
	cfg  Config
}

// swapHandler lets a test replace the Hub behind the same URL (a hub restart).
type swapHandler struct {
	mu sync.Mutex
	h  *Hub
}

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/link":
		h.ServeWS(w, r)
	case "POST /v1/link/poll":
		h.ServePoll(w, r)
	default:
		http.NotFound(w, r)
	}
}

func newFix(t *testing.T, mod ...func(*Config)) *fix {
	t.Helper()
	clk := &clock{t: t0}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	k, _ := hubkeys.FromSeed(bytes.Repeat([]byte{1}, 32))
	ks := hubkeys.New(st, k)
	f := &fix{t: t, ctx: context.Background(), clk: clk, st: st, ks: ks, logs: &syncBuf{}}
	f.cfg = Config{Store: st, Keys: ks, Now: clk.Now, Log: log.New(f.logs, "", 0), PollWait: 300 * time.Millisecond}
	for _, m := range mod {
		m(&f.cfg)
	}
	f.h = New(f.cfg)
	f.mux = &swapHandler{h: f.h}
	f.srv = httptest.NewServer(f.mux)
	t.Cleanup(func() {
		f.mux.mu.Lock()
		h := f.mux.h
		f.mux.mu.Unlock()
		h.Close()
		f.srv.Close()
	})
	return f
}

// restart replaces the Hub with a new one over the same store (a hub restart).
func (f *fix) restart() {
	f.h.Close()
	f.h = New(f.cfg)
	f.mux.mu.Lock()
	f.mux.h = f.h
	f.mux.mu.Unlock()
}

func (f *fix) wsURL() string   { return linktest.WSURL(f.srv.URL) }
func (f *fix) pollURL() string { return f.srv.URL + "/v1/link/poll" }

// device pairs a test device: a device row, keys registered (a phone also gets its link
// target), and the hub key pinned.
func (f *fix) device(d *linktest.Device, name string) *linktest.Device {
	f.t.Helper()
	kind := d.Kind
	req := auth.CreateRequest{Kind: kind, Name: name}
	if kind == "machine" {
		req.Machine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4" + name[:1]
	}
	c, err := auth.Create(f.ctx, f.st, req)
	if err != nil {
		f.t.Fatal(err)
	}
	d.ID = c.Device.ID
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error {
		_, err := f.ks.RegisterTx(tx, c.Device, d.KeysNew(), keys.RegQR)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	d.Pin(f.ks.Key.Pub)
	return d
}

func (f *fix) phone(seed byte) *linktest.Device {
	return f.device(linktest.NewPhone(seed), "phone"+string(rune('a'+seed%26)))
}
func (f *fix) machine(seed byte) *linktest.Device {
	return f.device(linktest.NewMachine(seed), string(rune('a'+seed%26))+"-machine")
}

func (f *fix) enqueue(dev *linktest.Device, t, tag, body string) int64 {
	f.t.Helper()
	seq, err := f.h.Enqueue(f.ctx, dev.ID, t, tag, time.Hour, []byte(body), "")
	if err != nil {
		f.t.Fatalf("enqueue %s: %v", t, err)
	}
	return seq
}

func (f *fix) exec(q string, args ...any) {
	f.t.Helper()
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error { _, err := tx.ExecContext(f.ctx, q, args...); return err }); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fix) queued(dev *linktest.Device) []int64 {
	f.t.Helper()
	rows, err := f.st.LinkPending(f.ctx, dev.ID, 0, 0, 10_000)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []int64
	for _, r := range rows {
		out = append(out, r.Seq)
	}
	return out
}

func (f *fix) state(dev *linktest.Device) store.LinkStateRow {
	f.t.Helper()
	s, err := f.st.LinkStateOf(f.ctx, dev.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fix) dial(dev *linktest.Device) *linktest.Conn {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	c, err := linktest.Dial(ctx, f.wsURL(), dev)
	if err != nil {
		f.t.Fatalf("dial: %v", err)
	}
	f.t.Cleanup(c.Close)
	return c
}

// recv reads one processed frame within 5 s.
func recv(t *testing.T, c *linktest.Conn) linktest.Msg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := c.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return m
}

// closed waits for the hub to close c and returns the code.
func closed(t *testing.T, c *linktest.Conn) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, err := c.Next(ctx)
		if err != nil {
			return linktest.CloseCode(err)
		}
	}
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var _ = link.Version
