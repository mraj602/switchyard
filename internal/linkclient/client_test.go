package linkclient

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	hublink "github.com/mraj602/watchyard/internal/hub/link"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/link/linktest"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// hub is a real hublink over a temp store, served at srv.
type hub struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	ks  *hubkeys.Service
	h   *hublink.Hub
	srv *httptest.Server
}

func newHub(t *testing.T) *hub {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	k, _ := hubkeys.FromSeed(bytes.Repeat([]byte{1}, 32))
	ks := hubkeys.New(st, k)
	h := hublink.New(hublink.Config{Store: st, Keys: ks, Log: log.New(&syncBuf{}, "", 0)})
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ks.OnMatrix = func(keys.Doc) { _ = h.PublishDocs(ctx) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/link" {
			h.ServeWS(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(func() { h.Close(); srv.Close() })
	return &hub{t: t, ctx: ctx, st: st, ks: ks, h: h, srv: srv}
}

// machine pairs a machine whose keys and hub pin live in a temp state folder.
func (hb *hub) machine(name string) (state, id string) {
	hb.t.Helper()
	state = hb.t.TempDir()
	dk, err := keys.NewDeviceKeys(time.Now(), nil)
	if err != nil {
		hb.t.Fatal(err)
	}
	c, err := auth.Create(hb.ctx, hb.st, auth.CreateRequest{Kind: "machine", Name: name, Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4" + name[:1]})
	if err != nil {
		hb.t.Fatal(err)
	}
	pub, _ := dk.Public()
	if err := hb.st.Write(hb.ctx, func(tx *store.Tx) error {
		_, err := hb.ks.RegisterTx(tx, c.Device, pub, keys.RegQR)
		return err
	}); err != nil {
		hb.t.Fatal(err)
	}
	dk.Device = c.Device.ID
	if err := keys.WriteDeviceKeys(state, dk); err != nil {
		hb.t.Fatal(err)
	}
	if err := keys.SetPin(keys.Path(state, keys.HubPinFile), keys.NewPin(hb.ks.Key.Pub, keys.PinQR, time.Now())); err != nil {
		hb.t.Fatal(err)
	}
	return state, c.Device.ID
}

func (hb *hub) url() string { u, _ := HubURL(hb.srv.URL); return u }

func (hb *hub) queued(id string) int {
	rows, err := hb.st.LinkPending(hb.ctx, id, 0, 0, 1000)
	if err != nil {
		hb.t.Fatal(err)
	}
	return len(rows)
}

type peers struct {
	mu   sync.Mutex
	got  []string
	fail int // fail this many calls first
}

func (p *peers) Queued(_ context.Context, t string, seq int64, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, t+" "+string(body))
	if p.fail > 0 {
		p.fail--
		return errors.New("not now")
	}
	return nil
}
func (p *peers) Ephemeral(string, []byte) {}
func (p *peers) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.got...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// start runs a client; done gets Run's result. The returned stop cancels it and waits
// (also run at cleanup).
func start(t *testing.T, cfg Config) (*Client, chan error, func()) {
	t.Helper()
	if cfg.Backoff == nil {
		cfg.Backoff = []time.Duration{20 * time.Millisecond}
	}
	logs := &syncBuf{}
	cfg.Log = log.New(logs, "", 0)
	c := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() { done <- c.Run(ctx); close(exited) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				t.Error("Run did not return")
			}
			if t.Failed() {
				t.Log(logs.String())
			}
		})
	}
	t.Cleanup(stop)
	return c, done, stop
}

// A machine stores the matrix and revocation list it is sent, hands peer frames to the peer
// handler and acks them all.
func TestMachineGetsDocsAndPeerFrames(t *testing.T) {
	hb := newHub(t)
	state, id := hb.machine("a-machine")
	_, phone := hb.machine("b-machine")
	p := &peers{}
	c, _, _ := start(t, Config{State: state, URL: hb.url(), HubMayWiden: true, Peers: p})
	waitFor(t, "connected", func() bool { return c.Status().State == "connected" })

	paused := true
	if _, err := hb.ks.EditMatrix(hb.ctx, hubkeys.MatrixEdit{Paused: &paused}, "test", false); err != nil {
		t.Fatal(err)
	}
	if err := hb.ks.RevokeDevice(hb.ctx, phone, "test", "lost"); err != nil {
		t.Fatal(err)
	}
	hb.h.Revoked(hb.ctx, phone)
	if _, err := hb.h.Enqueue(hb.ctx, id, link.TypePeerMsg, "", time.Hour, []byte(`{"hello":1}`), ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "matrix stored", func() bool {
		m, _ := keys.LoadAcceptedMatrix(state)
		return m != nil && m.Body.Paused
	})
	waitFor(t, "revocations stored", func() bool {
		r, _ := keys.LoadAcceptedRevocations(state)
		return r != nil && len(r.Body.Devices) == 1
	})
	waitFor(t, "peer frame", func() bool { return len(p.calls()) == 1 })
	if got := p.calls()[0]; got != `peer_msg {"hello":1}` {
		t.Fatalf("peer frame: %q", got)
	}
	waitFor(t, "acks", func() bool { return hb.queued(id) == 0 })
	if LastSeq(state) < 3 {
		t.Fatalf("last %d", LastSeq(state))
	}
}

// A peer_thread_resume (peers.md 6.3) reaches the peer handler only when its hub signature
// checks: one whose signature was changed after the hub signed it (anyone with write access to
// the hub's queue but not its key) is rejected and never handed over.
func TestThreadResumeNeedsTheHubSignature(t *testing.T) {
	hb := newHub(t)
	state, id := hb.machine("a-machine")
	if _, err := hb.h.Enqueue(hb.ctx, id, link.TypePeerThreadResume, "tr:t_aaaaaaaaaaaaaaaa", time.Hour,
		[]byte(`{"v":1,"thread":"t_aaaaaaaaaaaaaaaa"}`), ""); err != nil {
		t.Fatal(err)
	}
	if err := hb.st.Write(hb.ctx, func(tx *store.Tx) error {
		_, err := tx.ExecContext(hb.ctx, `UPDATE link_queue SET sig = randomblob(64) WHERE device_id = ?`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := hb.h.Enqueue(hb.ctx, id, link.TypePeerThreadResume, "tr:t_bbbbbbbbbbbbbbbb", time.Hour,
		[]byte(`{"v":1,"thread":"t_bbbbbbbbbbbbbbbb"}`), ""); err != nil {
		t.Fatal(err)
	}
	p := &peers{}
	start(t, Config{State: state, URL: hb.url(), Peers: p})
	waitFor(t, "good frame", func() bool { return len(p.calls()) >= 1 })
	waitFor(t, "queue drained", func() bool { return hb.queued(id) == 0 })
	if got := p.calls(); len(got) != 1 || got[0] != `peer_thread_resume {"v":1,"thread":"t_bbbbbbbbbbbbbbbb"}` {
		t.Fatalf("handed over %q", got)
	}
	if hb.h.Rejected(id) != 1 {
		t.Fatalf("rejected %d, want 1", hb.h.Rejected(id))
	}
}

// A frame the handler cannot process is not acked: the client reconnects and the hub
// replays it; a processed frame is never handed over twice (link.json survives restarts).
func TestReplayAfterFailureAndRestart(t *testing.T) {
	hb := newHub(t)
	state, id := hb.machine("a-machine")
	if _, err := hb.h.Enqueue(hb.ctx, id, link.TypePeerMsg, "", time.Hour, []byte(`{"n":1}`), ""); err != nil {
		t.Fatal(err)
	}
	p := &peers{fail: 1}
	_, _, stop := start(t, Config{State: state, URL: hb.url(), Peers: p})
	waitFor(t, "acked after replay", func() bool { return hb.queued(id) == 0 })
	if n := len(p.calls()); n != 2 {
		t.Fatalf("handler calls %d, want 2 (fail, replay)", n)
	}
	stop()
	// a second client over the same state: nothing is handed over again
	if _, err := hb.h.Enqueue(hb.ctx, id, link.TypePeerMsg, "", time.Hour, []byte(`{"n":2}`), ""); err != nil {
		t.Fatal(err)
	}
	p2 := &peers{}
	start(t, Config{State: state, URL: hb.url(), Peers: p2})
	waitFor(t, "second frame", func() bool { return len(p2.calls()) >= 1 })
	if got := p2.calls(); len(got) != 1 || !strings.Contains(got[0], `"n":2`) {
		t.Fatalf("second client got %q", got)
	}
}

// A removed machine is closed with 4403 and stops reconnecting.
func TestRevokedStops(t *testing.T) {
	hb := newHub(t)
	state, id := hb.machine("a-machine")
	c, done, _ := start(t, Config{State: state, URL: hb.url()})
	waitFor(t, "connected", func() bool { return c.Status().State == "connected" })
	if err := hb.ks.RevokeDevice(hb.ctx, id, "test", "lost"); err != nil {
		t.Fatal(err)
	}
	hb.h.Revoked(hb.ctx, id)
	select {
	case err := <-done:

		if err == nil || !strings.Contains(err.Error(), "4403") {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still running")
	}
	if s := c.Status(); s.State != "stopped" {
		t.Fatalf("status %+v", s)
	}
}

// A challenge not signed by the pinned hub key is refused before anything is signed.
func TestWrongHubKeyStops(t *testing.T) {
	hb := newHub(t)
	state, _ := hb.machine("a-machine")
	other, _ := hubkeys.FromSeed(bytes.Repeat([]byte{9}, 32))
	if err := os.Remove(keys.Path(state, keys.HubPinFile)); err != nil {
		t.Fatal(err)
	}
	if err := keys.SetPin(keys.Path(state, keys.HubPinFile), keys.NewPin(other.Pub, keys.PinHand, time.Now())); err != nil {
		t.Fatal(err)
	}
	c, done, _ := start(t, Config{State: state, URL: hb.url()})
	select {
	case err := <-done:

		if err == nil || !strings.Contains(err.Error(), "pinned hub key") {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still running")
	}
	if c.Status().State != "stopped" {
		t.Fatal(c.Status())
	}
}

func TestHubURL(t *testing.T) {
	for in, want := range map[string]string{"https://hub.example/": "wss://hub.example/v1/link", "http://127.0.0.1:8080": "ws://127.0.0.1:8080/v1/link"} {
		if got, err := HubURL(in); err != nil || got != want {
			t.Fatalf("%s: %s %v", in, got, err)
		}
	}
	if _, err := HubURL("hub.example"); err == nil {
		t.Fatal("no scheme accepted")
	}
}

var _ = linktest.WSURL
