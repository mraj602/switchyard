//go:build fakeinbox

package peers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/agent"
	"github.com/mraj602/switchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	hublink "github.com/mraj602/switchyard/internal/hub/link"
	"github.com/mraj602/switchyard/internal/hub/peerrelay"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/link"
	"github.com/mraj602/switchyard/internal/linkclient"
	"github.com/mraj602/switchyard/internal/peerlink"
	"github.com/mraj602/switchyard/internal/peers"
	"github.com/mraj602/switchyard/internal/peers/fakeinbox"
)

// Accounts of the test world: laptop's session is work, worker's two are personal and other.
const (
	accWork     = "acc-work"
	accPersonal = "acc-personal"
	accOther    = "acc-other"
)

// clock is the one test clock: the hub, the store, both agents and both link clients.
type clock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}

// intercept lets a test play a hub that ignores its own rules: handled frames never reach
// the real relay.
type intercept func(ctx context.Context, device string, u link.Up) (handled bool, t string, body any, err error)

type testHub struct {
	t     *testing.T
	ctx   context.Context
	clk   *clock
	dir   string
	st    *store.Store
	ks    *hubkeys.Service
	lk    *hublink.Hub
	relay *peerrelay.Relay
	srv   *httptest.Server
	owner ed25519.PrivateKey

	mu    sync.Mutex
	rogue intercept
}

// relayFront is the hub's PeerRelay: the real relay unless a test set rogue.
type relayFront struct{ h *testHub }

func (f relayFront) Up(ctx context.Context, device string, u link.Up) (string, any, error) {
	f.h.mu.Lock()
	r := f.h.rogue
	f.h.mu.Unlock()
	if r != nil {
		if ok, t, b, err := r(ctx, device, u); ok {
			return t, b, err
		}
	}
	return f.h.relay.Up(ctx, device, u)
}

func (h *testHub) setRogue(r intercept) {
	h.mu.Lock()
	h.rogue = r
	h.mu.Unlock()
}

// newHub starts a hub with a confirmed owner key and an owner-signed matrix that lets work
// start threads with personal (and nothing else across accounts), maxTurns turns.
func newHub(t *testing.T, maxTurns int) *testHub {
	t.Helper()
	ctx := context.Background()
	clk := &clock{}
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "hub.db"), store.Options{NoBackupBeforeMigrate: true, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	k, err := hubkeys.FromSeed(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := &testHub{t: t, ctx: ctx, clk: clk, dir: dir, st: st, ks: hubkeys.New(st, k), owner: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))}
	quiet := log.New(io.Discard, "", 0)
	h.relay = peerrelay.New(st, h.ks, clk.Now, quiet)
	h.lk = hublink.New(hublink.Config{Store: st, Keys: h.ks, Now: clk.Now, Log: quiet, Peers: relayFront{h}, PeerExpired: h.relay.Expired})
	h.relay.Link = h.lk
	h.ks.OnMatrix = func(d keys.Doc) {
		if err := h.relay.MatrixChanged(ctx, d); err != nil {
			t.Errorf("matrix changed: %v", err)
		}
		_ = h.lk.PublishDocs(ctx)
	}
	if err := h.lk.Start(ctx); err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/link" {
			h.lk.ServeWS(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(func() { h.lk.Close(); h.srv.Close(); st.Close() })

	od, err := keys.NewOwnerKeyDoc(h.owner, k.FP, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ks.ProposeAndConfirm(ctx, od); err != nil {
		t.Fatal(err)
	}
	h.ownerMatrix(func(b *keys.MatrixBody) {
		b.MaxTurns = maxTurns
		b.Accounts = map[string]string{accWork: "work", accPersonal: "personal", accOther: "other"}
		b.Allow = [][]string{{accOther, accPersonal}, {accWork, accPersonal}} // sorted
	})
	return h
}

// ownerMatrix stores the next matrix version, signed by the owner key: the current body
// changed by edit.
func (h *testHub) ownerMatrix(edit func(*keys.MatrixBody)) {
	h.t.Helper()
	cur, err := h.ks.MatrixCurrent(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	b := cur.Signed.Body
	edit(&b)
	b.Version = cur.Signed.Body.Version + 1
	b.IssuedAt = keys.FormatTime(h.clk.Now())
	raw, _ := json.Marshal(b)
	d := keys.NewDoc(keys.TypeMatrix, raw)
	if err := d.Sign(keys.ByOwner, h.owner); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.ks.PutOwnerMatrix(h.ctx, d, "test"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *testHub) url() string { u, _ := linkclient.HubURL(h.srv.URL); return u }

// rows are the hub's peer_message rows, newest first.
func (h *testHub) rows() []store.PeerMessageRow {
	h.t.Helper()
	r, err := h.st.PeerMessages(h.ctx, store.PeerMessageQuery{Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

// waitRow waits until message id has status.
func (h *testHub) waitRow(id, status string) store.PeerMessageRow {
	h.t.Helper()
	var last store.PeerMessageRow
	ok := eventually(10*time.Second, func() bool {
		for _, r := range h.rows() {
			if r.ID == id {
				last = r
				return r.Status == status
			}
		}
		return false
	})
	if !ok {
		h.t.Fatalf("message %s: status %q (%s), want %q", id, last.Status, last.Detail, status)
	}
	return last
}

// assertNoText scans every file of the hub's database for each text: the hub stores
// metadata only (peers.md 10).
func (h *testHub) assertNoText(texts ...string) {
	h.t.Helper()
	ents, err := os.ReadDir(h.dir)
	if err != nil {
		h.t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(h.dir, e.Name()))
		if err != nil {
			continue
		}
		n++
		for _, txt := range texts {
			if bytes.Contains(b, []byte(txt)) {
				h.t.Errorf("hub file %s holds message text %q", e.Name(), txt)
			}
		}
	}
	if n == 0 {
		h.t.Fatal("no hub files to scan")
	}
}

func eventually(d time.Duration, f func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f()
}

// ---- machines ----

type sessionSpec struct {
	slot, label, account string
	pid                  int
	id, name             string
}

type session struct {
	sessionSpec
	inbox *fakeinbox.Server
}

type machine struct {
	t                          *testing.T
	h                          *testHub
	name, id, device           string
	base, home, state, runtime string
	procRoot                   string
	sessions                   []*session
	remote                     *peerlink.Remote
	lc                         *linkclient.Client
	sock                       string
	caller                     atomic.Int64 // the pid the agent sees connecting

	mu       sync.Mutex
	override peers.Matrix
	stopLink context.CancelFunc
	linkDone chan struct{}
}

func (m *machine) write(path, content string) {
	m.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) proc(pid, ppid int, comm, exe string, start int, env ...string) {
	d := filepath.Join(m.procRoot, fmt.Sprint(pid))
	m.write(filepath.Join(d, "stat"), fmt.Sprintf("%d (%s) S %d %s%d 0 0\n", pid, comm, ppid, strings.Repeat("0 ", 17), start))
	m.write(filepath.Join(d, "comm"), comm+"\n")
	m.write(filepath.Join(d, "cmdline"), comm+"\x00")
	m.write(filepath.Join(d, "environ"), strings.Join(env, "\x00")+"\x00")
	if exe != "" {
		if err := os.Symlink(exe, filepath.Join(d, "exe")); err != nil {
			m.t.Fatal(err)
		}
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// newMachine pairs a machine with the hub (device keys, owner-certified certificate, pins),
// creates its sessions (fake processes, registry files, fake inboxes, hub session rows) and
// starts its agent. The link is not connected yet: connect does that.
func (h *testHub) newMachine(name, id string, specs ...sessionSpec) *machine {
	t := h.t
	t.Helper()
	base, err := os.MkdirTemp("", "sye") // short: Unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	m := &machine{t: t, h: h, name: name, id: id, base: base, home: filepath.Join(base, "h"), state: filepath.Join(base, "s"),
		runtime: filepath.Join(base, "r"), procRoot: filepath.Join(base, "p"), sock: filepath.Join(base, "a.sock")}
	for _, d := range []string{m.home, m.state, filepath.Join(m.runtime, peers.SocksDirName), m.procRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// Pairing: a machine device with keys, certified by the owner; the hub and owner pins.
	c, err := auth.Create(h.ctx, h.st, auth.CreateRequest{Kind: "machine", Name: name, Machine: id})
	if err != nil {
		t.Fatal(err)
	}
	m.device = c.Device.ID
	dk, err := keys.NewDeviceKeys(h.clk.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := dk.Public()
	if err := h.st.Write(h.ctx, func(tx *store.Tx) error {
		_, err := h.ks.RegisterTx(tx, c.Device, pub, keys.RegQR)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := h.st.R().QueryRowContext(h.ctx, `SELECT cert FROM device_key WHERE device_id = ?`, m.device).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	ownerPub := h.owner.Public().(ed25519.PublicKey)
	if err := h.ks.Certify(h.ctx, m.device, keys.Fingerprint(ownerPub), keys.SignDoc(keys.TypeDeviceCert, raw, keys.ByOwner, h.owner).Sig); err != nil {
		t.Fatal(err)
	}
	dk.Device = m.device
	if err := keys.WriteDeviceKeys(m.state, dk); err != nil {
		t.Fatal(err)
	}
	if err := keys.SetPin(keys.Path(m.state, keys.HubPinFile), keys.NewPin(h.ks.Key.Pub, keys.PinQR, h.clk.Now())); err != nil {
		t.Fatal(err)
	}
	if err := keys.SetPin(keys.Path(m.state, keys.OwnerPinFile), keys.NewPin(ownerPub, keys.PinLocal, h.clk.Now())); err != nil {
		t.Fatal(err)
	}

	// Sessions: a claude process with a bash child (the Bash tool sy peers send runs in), the
	// registry file, a bypass-mode session file and a fake inbox; the same session at the hub.
	for i, sp := range specs {
		slot := filepath.Join(m.home, sp.slot)
		m.write(filepath.Join(slot, "settings.json"), "{}")
		m.write(filepath.Join(slot, ".claude.json"), mustJSON(map[string]any{"oauthAccount": map[string]string{
			"accountUuid": sp.account, "organizationUuid": "org-1", "emailAddress": sp.label + "@example.test"}}))
		start := 4000 + i*100
		m.proc(sp.pid, 1, "claude", "/home/u/.local/share/claude/versions/2.1.300", start, "HOME="+m.home, "CLAUDE_CONFIG_DIR="+slot)
		m.proc(sp.pid+1, sp.pid, "bash", "/usr/bin/bash", start+1, "CLAUDECODE=1")
		sock := filepath.Join(m.runtime, peers.SocksDirName, fmt.Sprint(sp.pid)+".sock")
		cwd := filepath.Join(m.home, "src", sp.name)
		m.write(filepath.Join(slot, "sessions", fmt.Sprint(sp.pid)+".json"), mustJSON(map[string]any{
			"pid": sp.pid, "sessionId": sp.id, "cwd": cwd, "procStart": fmt.Sprint(start), "messagingSocketPath": sock,
			"name": sp.name, "status": "idle", "kind": "interactive", "peerProtocol": 1}))
		m.write(filepath.Join(peers.SessionsDir(m.state), sp.id+".json"), mustJSON(peers.SessionFile{V: 1, Session: sp.id, Mode: "bypassPermissions"}))
		in, err := fakeinbox.Listen(fakeinbox.Config{Path: sock, SessionID: sp.id, Mode: peers.ModeBypass})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(in.Close)
		m.sessions = append(m.sessions, &session{sessionSpec: sp, inbox: in})
		now := h.clk.Now().UnixMilli()
		if err := h.st.Write(h.ctx, func(tx *store.Tx) error {
			seat, err := tx.UpsertSeat(store.SeatKey{Account: sp.account, Org: "org-1", Email: sp.label + "@example.test"}, now)
			if err != nil {
				return err
			}
			if err := tx.SeatLook(seat, sp.label, "", nil); err != nil {
				return err
			}
			slotID, err := tx.EnsureSlot(id, "~/"+sp.slot, now)
			if err != nil {
				return err
			}
			if err := tx.InsertSession(store.SessionRow{ID: sp.id, SeatID: seat, MachineID: id, SlotID: slotID, Cwd: cwd,
				Project: "~/src/" + sp.name, Name: sp.name, StartedAt: now, LastEventAt: now}); err != nil {
				return err
			}
			return tx.PutFold(sp.id, store.FoldRow{State: "working", Since: now, UpdatedAt: now, LastEventAt: now})
		}); err != nil {
			t.Fatal(err)
		}
	}

	quiet := log.New(io.Discard, "", 0)
	m.remote = peerlink.New(m.env(), quiet)
	m.remote.Fresh = -1
	m.lc = linkclient.New(linkclient.Config{State: m.state, URL: h.url(), Peers: m.remote, Now: h.clk.Now, Log: quiet,
		Backoff: []time.Duration{20 * time.Millisecond}})
	m.remote.Link = m.lc

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = agent.Run(ctx, agent.Config{Socket: m.sock, Remote: m.remote, Now: h.clk.Now,
			Load: func() (peers.Env, bool, error) { return m.env(), true, nil },
			PeerCred: func(*net.UnixConn) (int, int, error) {
				return int(m.caller.Load()), os.Getuid(), nil
			}})
	}()
	t.Cleanup(func() { cancel(); <-done; m.disconnect() })
	if !eventually(5*time.Second, func() bool { _, err := os.Stat(m.sock); return err == nil }) {
		t.Fatal("agent socket did not appear")
	}
	return m
}

func (m *machine) env() peers.Env {
	labels := map[string]string{}
	for _, s := range m.sessions {
		labels["~/"+s.slot] = s.label
	}
	m.mu.Lock()
	ov := m.override
	m.mu.Unlock()
	return peers.Env{Home: m.home, State: m.state, Runtime: m.runtime, Machine: identity.Machine{ID: m.id, Name: m.name},
		Proc: peers.LinuxProc{Root: m.procRoot}, FS: identity.OSFS{}, Now: m.h.clk.Now, SlotLabels: labels, Matrix: ov}
}

// setMatrix makes this machine's sender use m instead of its accepted matrix (a machine with
// a wrong matrix); nil restores the accepted one. The receiving side keeps its own.
func (m *machine) setMatrix(x peers.Matrix) {
	m.mu.Lock()
	m.override = x
	m.mu.Unlock()
}

// connect starts the link and waits until the hub sees it and the matrix version (at
// least) is accepted.
func (m *machine) connect(matrixVersion int) {
	m.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	m.stopLink, m.linkDone = cancel, done
	m.mu.Unlock()
	go func() { defer close(done); _ = m.lc.Run(ctx) }()
	if !eventually(10*time.Second, func() bool { return m.h.lk.Connected(m.device) }) {
		m.t.Fatalf("%s: link did not connect", m.name)
	}
	if !eventually(10*time.Second, func() bool {
		s, err := keys.LoadAcceptedMatrix(m.state)
		return err == nil && s != nil && s.Body.Version >= matrixVersion
	}) {
		m.t.Fatalf("%s: matrix version %d not accepted", m.name, matrixVersion)
	}
}

// disconnect stops the link and waits until the hub has noticed.
func (m *machine) disconnect() {
	m.mu.Lock()
	cancel, done := m.stopLink, m.linkDone
	m.stopLink, m.linkDone = nil, nil
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	eventually(5*time.Second, func() bool { return !m.h.lk.Connected(m.device) })
}

func (m *machine) session(name string) *session {
	for _, s := range m.sessions {
		if s.name == name {
			return s
		}
	}
	m.t.Fatalf("no session %s", name)
	return nil
}

// send is `sy peers send` from the session `from` through this machine's agent.
func (m *machine) send(from, to, text string, ttl time.Duration, wait int) agent.Reply {
	m.t.Helper()
	s := m.session(from)
	m.caller.Store(int64(s.pid + 1))
	if wait == 0 {
		wait = 10
	}
	rep, err := agent.Call(m.sock, agent.Request{Op: agent.OpSend, ClaudePID: s.pid, ConfigDir: "~/" + s.slot, To: to, Text: text,
		TTLs: int(ttl / time.Second), WaitS: wait}, 90*time.Second)
	if err != nil {
		m.t.Fatalf("send: %v", err)
	}
	return rep
}

// audit is this machine's <state>/peers/audit.jsonl as lines.
func (m *machine) audit() []map[string]any {
	b, _ := os.ReadFile(filepath.Join(m.state, "peers", "audit.jsonl"))
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var v map[string]any
		if json.Unmarshal([]byte(l), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func (m *machine) waitAudit(msg, status string) {
	m.t.Helper()
	if !eventually(10*time.Second, func() bool {
		for _, l := range m.audit() {
			if l["msg"] == msg && l["status"] == status {
				return true
			}
		}
		return false
	}) {
		m.t.Fatalf("%s: no audit line %s %s in %v", m.name, msg, status, m.audit())
	}
}

// ---- the standard world ----

const (
	laptopID = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4L"
	workerID = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4W"
	// matrixV is the owner-signed matrix newHub stores (the baseline is 1).
	matrixV = 2
)

type world struct {
	h              *testHub
	laptop, worker *machine
}

// newWorld: laptop runs api (work); worker runs worker (personal) and side (other). Both
// links are connected.
func newWorld(t *testing.T, maxTurns int) *world {
	h := newHub(t, maxTurns)
	w := &world{h: h,
		laptop: h.newMachine("laptop", laptopID, sessionSpec{slot: ".claude-2", label: "work", account: accWork, pid: 500, id: "sess-api", name: "api"}),
		worker: h.newMachine("worker", workerID,
			sessionSpec{slot: ".claude-5", label: "personal", account: accPersonal, pid: 600, id: "sess-worker", name: "worker"},
			sessionSpec{slot: ".claude-7", label: "other", account: accOther, pid: 700, id: "sess-side", name: "side"}),
	}
	w.laptop.connect(matrixV)
	w.worker.connect(matrixV)
	return w
}

// allowAll is a matrix that allows every pair: a sender whose matrix is wrong.
func allowAll() peers.Matrix {
	return peers.NewDocMatrix(keys.MatrixBody{V: 1, Version: 99, SameAccount: true, MaxTurns: 500,
		Allow: [][]string{{accWork, accPersonal}, {accWork, accOther}, {accPersonal, accWork}, {accOther, accWork}}})
}
