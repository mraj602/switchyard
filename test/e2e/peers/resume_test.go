//go:build fakeinbox

package peers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/peers"
)

// resumeViaAPI is the board's "Resume thread": POST /v1/peers/threads/{id}/resume on the
// hub's real API with an owner session (pepper in a temp file, never the real state).
func (h *testHub) resumeViaAPI(thread string) (int, map[string]any) {
	h.t.Helper()
	if api.PairPepperPath == "" {
		api.PairPepperPath = filepath.Join(h.dir, "pair_pepper")
		h.t.Cleanup(func() { api.PairPepperPath = "" })
	}
	srv := api.New(api.Config{Store: h.st, Keys: h.ks, Link: h.lk, PeerThreads: h.relay, Now: h.clk.Now, PingEvery: time.Hour})
	hs := httptest.NewServer(srv.Handler())
	defer func() { srv.Close(); hs.Close() }()
	pepper, err := pair.NewService(h.st, api.PairPepperPath).Pepper()
	if err != nil {
		h.t.Fatal(err)
	}
	owner, err := auth.Create(h.ctx, h.st, auth.CreateRequest{Kind: "browser", Name: "owner", Owner: true,
		CreatedBy: "owner-link:pc_test", Pepper: pepper})
	if err != nil {
		h.t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", hs.URL+"/v1/peers/threads/"+thread+"/resume", nil)
	req.Header.Set("Authorization", "Bearer "+owner.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

// waitResume waits for this machine's audit line of a relayed thread resume.
func (m *machine) waitResume(thread, status string) {
	m.t.Helper()
	if !eventually(10*time.Second, func() bool {
		for _, l := range m.audit() {
			if l["thread"] == thread && l["status"] == status {
				return true
			}
		}
		return false
	}) {
		m.t.Fatalf("%s: no %s line for thread %s in %v", m.name, status, thread, m.audit())
	}
}

// stopThread runs a thread into the loop guard (max_turns 2) and returns its id.
func stopThread(t *testing.T, w *world) string {
	t.Helper()
	rep := w.laptop.send("api", toWorker, "turn one", 0, 0)
	delivered(t, rep, rep.Status)
	back := w.worker.send("worker", toAPI, "turn two", 0, 0)
	delivered(t, back, back.Status)
	refusedWith(t, w.laptop.send("api", toWorker, "turn three", 0, 0).Refusal(), peers.CodeLoopGuard)
	rows := w.h.rows()
	if len(rows) == 0 || rows[0].Thread == "" {
		t.Fatal("no thread at the hub")
	}
	return rows[0].Thread
}

// "Resume thread" (peers.md 6.3): the loop guard stops a thread; the owner resumes it through
// the hub's endpoint; both agents (hub_may_widen on) reset their own count, and the next
// message is delivered.
func TestThreadResumeAcrossMachines(t *testing.T) {
	h := newHub(t, 2)
	h.mayWiden = map[string]bool{"laptop": true, "worker": true}
	w := newWorldOn(h)
	thread := stopThread(t, w)

	// The worker is offline: its resume waits in its link queue. The laptop's own guard
	// stopped turn three, so the hub counted two turns and was not paused itself; the resume
	// resets its count all the same.
	w.worker.disconnect()
	st, m := h.resumeViaAPI(thread)
	if st != 200 || m["machines"] != float64(2) {
		t.Fatalf("resume: %d %v", st, m)
	}
	if err := h.st.Write(h.ctx, func(tx *store.Tx) error {
		th, err := tx.PeerThread(thread)
		if err == nil && (th.Turns != 0 || th.PausedAt != 0) {
			t.Errorf("hub thread after resume: %+v", th)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.laptop.waitResume(thread, peers.ResumeResumed)
	w.worker.connect(matrixV)
	w.worker.waitResume(thread, peers.ResumeResumed)

	rep := w.laptop.send("api", toWorker, "turn three, after the resume", 0, 0)
	delivered(t, rep, rep.Status)
	onlyContent(t, w.worker.session("worker").inbox, 2)
	// The guard counts again from the resume: max_turns more, then a stop.
	back := w.worker.send("worker", toAPI, "turn four", 0, 0)
	delivered(t, back, back.Status)
	refusedWith(t, w.laptop.send("api", toWorker, "turn five", 0, 0).Refusal(), peers.CodeLoopGuard)
}

// A machine with hub_may_widen off does not take the hub's resume: the hub and the other
// machine lift their stop, but that machine refuses the next message with loop_guard until a
// human prompt. Whoever holds the hub key cannot restart a thread there.
func TestThreadResumeRefusedByStrictMachine(t *testing.T) {
	h := newHub(t, 2)
	h.mayWiden = map[string]bool{"laptop": true} // the worker keeps hub_may_widen = false
	w := newWorldOn(h)
	thread := stopThread(t, w)

	if st, m := h.resumeViaAPI(thread); st != 200 || m["machines"] != float64(2) {
		t.Fatalf("resume: %d %v", st, m)
	}
	w.laptop.waitResume(thread, peers.ResumeResumed)
	w.worker.waitResume(thread, peers.ResumeRefused)

	rf := w.laptop.send("api", toWorker, "turn three, after the resume", 0, 0).Refusal()
	refusedWith(t, rf, peers.CodeRejected)
	if !strings.Contains(rf.Detail, peers.CodeLoopGuard) {
		t.Errorf("detail %q", rf.Detail)
	}
	onlyContent(t, w.worker.session("worker").inbox, 1)
}
