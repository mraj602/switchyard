//go:build fakeinbox

package peers

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/agent"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/peers"
)

// list is `sy peers list --json` from the session `from` through this machine's agent.
func (m *machine) list(from string) []peers.ListEntry {
	m.t.Helper()
	s := m.session(from)
	m.caller.Store(int64(s.pid + 1))
	rep, err := agent.Call(m.sock, agent.Request{Op: agent.OpList, ClaudePID: s.pid, ConfigDir: "~/" + s.slot}, 30*time.Second)
	if err != nil {
		m.t.Fatalf("list: %v", err)
	}
	if rf := rep.Refusal(); rf != nil {
		m.t.Fatalf("list refused: %v", rf)
	}
	return rep.Entries
}

// waitOwnerPin waits until this machine pinned the owner key (the link's welcome carries it).
func (m *machine) waitOwnerPin() {
	m.t.Helper()
	if !eventually(10*time.Second, func() bool {
		_, err := keys.ReadPin(keys.Path(m.state, keys.OwnerPinFile))
		return err == nil
	}) {
		m.t.Fatalf("%s did not pin the owner key at login", m.name)
	}
}

func entryFor(t *testing.T, es []peers.ListEntry, handle string) peers.ListEntry {
	t.Helper()
	for _, e := range es {
		if e.Handle == handle {
			return e
		}
	}
	t.Fatalf("%s not listed in %+v", handle, es)
	return peers.ListEntry{}
}

// The owner never ran `sy keys init`, so no machine is owner-certified (what `sy doctor`
// warns about as "not owner-certified"). Then:
//   - same-machine messages across accounts work (they need no link keys);
//   - other machines' sessions are listed with `can` from the matrix, but marked blocked
//     with the reason, and a send to one is refused at once, without reaching the hub, with
//     a line that says what the owner has to do;
//   - after the owner's steps (owner key made and confirmed, every machine certified, the
//     other machine picking the owner key up when its agent logs in again) the same send is
//     delivered and the reply comes back.
func TestUncertifiedMachines(t *testing.T) {
	h := newHubNoOwner(t, peers.NoTurnLimit)
	w := newWorldOn(h)

	// Same machine, two accounts: works.
	loc := w.worker.send("side", toWorker, "same machine without certificates", 0, 0)
	delivered(t, loc, loc.Status)
	onlyContent(t, w.worker.session("builder").inbox, 1)

	// Listed, but blocked: this machine pins no owner key (there is none).
	e := entryFor(t, w.laptop.list("api"), toWorker)
	if e.Can != "start" || e.Blocked != peers.BlockedNoOwnerPin || !peers.FixedByCerts(e.Blocked) {
		t.Fatalf("entry %+v, want can start, blocked %q", e, peers.BlockedNoOwnerPin)
	}
	rf := w.laptop.send("api", toWorker, "across machines", 0, 0).Refusal()
	refusedWith(t, rf, peers.CodeRejected)
	line := peers.Describe(rf)
	for _, want := range []string{toWorker, peers.BlockedNoOwnerPin, "sy keys init", "sy keys certify --all", "sy agent enable", "Do not retry"} {
		if !strings.Contains(line, want) {
			t.Errorf("refusal line lacks %q:\n%s", want, line)
		}
	}
	if code := peers.ExitCode(rf.Code); code != peers.ExitNotSent {
		t.Errorf("exit %d", code)
	}
	if n := len(h.rows()); n != 1 { // only the worker's same-machine peer_log
		t.Errorf("hub rows %d, want 1 (nothing relayed)", n)
	}

	// The owner key is made on the laptop (sy keys init pins it there) and confirmed; the
	// laptop alone is certified. The laptop now sees the worker's keys as the blocker, the
	// worker (no owner pin yet) its own pin.
	h.confirmOwner()
	h.certify(w.laptop)
	if err := keys.SetPin(keys.Path(w.laptop.state, keys.OwnerPinFile), keys.NewPin(h.owner.Public().(ed25519.PublicKey), keys.PinLocal, h.clk.Now())); err != nil {
		t.Fatal(err)
	}
	if e := entryFor(t, w.laptop.list("api"), toWorker); e.Blocked != "gpu-box"+"'s link keys are not owner-certified" {
		t.Fatalf("after certifying the laptop only: %+v", e)
	}
	if e := entryFor(t, w.worker.list("builder"), toAPI); e.Blocked != peers.BlockedNoOwnerPin {
		t.Fatalf("the worker before it pins the owner key: %+v", e)
	}

	// The worker is certified and picks the owner key up when its link logs in again (the
	// welcome carries it; sy agent enable does the same).
	h.certify(w.worker)
	// Before it has the owner key the worker cannot check the laptop's certificate, so it
	// refuses the message; the laptop's line says why and what to do.
	rf = w.laptop.send("api", toWorker, "worker not pinned yet", 0, 0).Refusal()
	refusedWith(t, rf, peers.CodeRejected)
	if !strings.Contains(rf.Detail, "uncertified") || !strings.Contains(rf.Detail, "sy agent enable") {
		t.Errorf("receiver's refusal: %q", rf.Detail)
	}
	w.worker.disconnect()
	w.worker.connect(matrixV)
	w.worker.waitOwnerPin()
	if e := entryFor(t, w.laptop.list("api"), toWorker); e.Blocked != "" {
		t.Fatalf("still blocked: %+v", e)
	}
	rep := w.laptop.send("api", toWorker, "now certified", 0, 0)
	delivered(t, rep, rep.Status)
	back := w.worker.send("builder", toAPI, "and back", 0, 0)
	delivered(t, back, back.Status)
	onlyContent(t, w.worker.session("builder").inbox, 2)
	onlyContent(t, w.laptop.session("api").inbox, 1)
}

// A sender whose own certificate has no owner signature is refused by every receiver: its
// agent says so before sending (the hub's directory carries the sender's own certificate).
func TestSenderNotCertified(t *testing.T) {
	h := newHubNoOwner(t, peers.NoTurnLimit)
	w := newWorldOn(h)
	h.confirmOwner()
	h.certify(w.worker)
	for _, m := range []*machine{w.laptop, w.worker} {
		m.disconnect()
		m.connect(matrixV)
		m.waitOwnerPin()
	}
	e := entryFor(t, w.laptop.list("api"), toWorker)
	if e.Blocked != peers.BlockedSelfUncert {
		t.Fatalf("entry %+v, want blocked %q", e, peers.BlockedSelfUncert)
	}
	rf := w.laptop.send("api", toWorker, "from an uncertified laptop", 0, 0).Refusal()
	refusedWith(t, rf, peers.CodeRejected)
	if !strings.Contains(rf.Detail, peers.BlockedSelfUncert) || !strings.Contains(rf.Detail, "sy keys certify --all") {
		t.Errorf("detail %q", rf.Detail)
	}
	// The worker (certified) sees the laptop as the blocker.
	if e := entryFor(t, w.worker.list("builder"), toAPI); e.Blocked != "laptop's link keys are not owner-certified" {
		t.Fatalf("worker's view %+v", e)
	}
}
