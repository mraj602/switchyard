//go:build fakeinbox

package peers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/peers"
	"github.com/mraj602/switchyard/internal/peers/fakeinbox"
)

// waitMatrix waits until this machine accepted matrix version v (or later).
func (m *machine) waitMatrix(v int) {
	m.t.Helper()
	if !eventually(10*time.Second, func() bool {
		s, err := keys.LoadAcceptedMatrix(m.state)
		return err == nil && s != nil && s.Body.Version >= v
	}) {
		m.t.Fatalf("%s: matrix version %d not accepted", m.name, v)
	}
}

// threadTurns is the hub's turn count of a thread.
func (h *testHub) threadTurns(id string) int64 {
	h.t.Helper()
	var th store.PeerThreadRow
	if err := h.st.Write(h.ctx, func(tx *store.Tx) error {
		var err error
		th, err = tx.PeerThread(id)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return th.Turns
}

// With max_turns 0 (no limit, the default of a new matrix) two sessions on two machines and
// two accounts keep a long unattended conversation: 50 round trips (100 messages, no human
// prompt) are all delivered, every party keeps counting the turns, and nobody stops the
// thread. The clock moves 3 minutes per round trip so the pair rate (60 an hour, 9.2) does not
// bind: the rates, not the turn limit, are what still bounds such a conversation.
func TestNoTurnLimitLongConversation(t *testing.T) {
	w := newWorld(t, peers.NoTurnLimit)
	w.h.clk.Freeze()
	const rounds = 50
	var thread string
	for i := 0; i < rounds; i++ {
		rep := w.laptop.send("api", toWorker, fmt.Sprintf("question %d", i), 0, 0)
		delivered(t, rep, rep.Status)
		back := w.worker.send("worker", toAPI, fmt.Sprintf("answer %d", i), 0, 0)
		delivered(t, back, back.Status)
		if thread == "" {
			thread = rep.Thread
		}
		if rep.Thread != thread || back.Thread != thread {
			t.Fatalf("round %d: threads %s / %s, want %s", i, rep.Thread, back.Thread, thread)
		}
		w.h.clk.Advance(3 * time.Minute)
	}
	onlyContent(t, w.worker.session("worker").inbox, rounds)
	got := onlyContent(t, w.laptop.session("api").inbox, rounds)
	if !strings.Contains(got[rounds-1].Wrapper.Body, fmt.Sprintf("answer %d", rounds-1)) {
		t.Fatalf("last reply %q", got[rounds-1].Wrapper.Body)
	}
	if n := w.h.threadTurns(thread); n != 2*rounds {
		t.Errorf("hub turns %d, want %d", n, 2*rounds)
	}
	for _, m := range []*machine{w.laptop, w.worker} {
		ts := peers.LoadThreads(m.env().ThreadsPath())
		th := ts.Find(thread)
		if th == nil || th.Turns != 2*rounds || th.PausedAt != nil {
			t.Errorf("%s thread %+v, want %d turns and not paused", m.name, th, 2*rounds)
		}
	}
	for _, r := range w.h.rows() {
		if r.Status != "delivered" {
			t.Errorf("hub row %s %s %s", r.ID, r.Status, r.Detail)
		}
	}
}

// Without a turn limit, a fast exchange is bounded by the per-session rate (burst 20, then 10
// a minute): the refusal says when to retry, and after that time the same pair goes on.
func TestNoTurnLimitStillRateLimited(t *testing.T) {
	w := newWorld(t, peers.NoTurnLimit)
	w.h.clk.Freeze()
	for i := 0; i < peers.SessionBurst; i++ {
		rep := w.laptop.send("api", toWorker, fmt.Sprintf("fast %d", i), 0, 0)
		delivered(t, rep, rep.Status)
	}
	rf := w.laptop.send("api", toWorker, "one more", 0, 0).Refusal()
	refusedWith(t, rf, peers.CodeRateLimited)
	if line := peers.Describe(rf); !strings.Contains(line, "retry in") {
		t.Errorf("rate refusal %q does not say when to retry", line)
	}
	w.h.clk.Advance(10 * time.Second)
	rep := w.laptop.send("api", toWorker, "after the wait", 0, 0)
	delivered(t, rep, rep.Status)
	onlyContent(t, w.worker.session("worker").inbox, peers.SessionBurst+1)
}

// An idle target (a session waiting at its prompt: registry "idle", hub state "done") gets
// the message at once; a target whose session has ended since the hub last heard of it is
// target_gone, reported back to the sender as a final status with a clear line.
func TestIdleAndEndedTargets(t *testing.T) {
	w := newWorld(t, peers.NoTurnLimit)
	nowMS := w.h.clk.Now().UnixMilli()
	if err := w.h.st.Write(w.h.ctx, func(tx *store.Tx) error {
		return tx.PutFold("sess-side", store.FoldRow{State: "done", Since: nowMS, UpdatedAt: nowMS, LastEventAt: nowMS})
	}); err != nil {
		t.Fatal(err)
	}
	w.h.ownerMatrix(func(b *keys.MatrixBody) { b.Allow = keys.SortAllow(append(b.Allow, []string{accWork, accOther})) })
	w.laptop.waitMatrix(matrixV + 1)
	w.worker.waitMatrix(matrixV + 1)

	rep := w.laptop.send("api", toSide, "you were idle", 0, 0)
	delivered(t, rep, rep.Status)
	if got := onlyContent(t, w.worker.session("side").inbox, 1); got[0].Outcome != fakeinbox.Deliver {
		t.Fatalf("idle target: %s (%s)", got[0].Outcome, got[0].Reason)
	}

	// The worker's session exits without a SessionEnd reaching the hub: the hub still lists it.
	s := w.worker.session("worker")
	s.inbox.Close()
	if err := os.Remove(filepath.Join(w.worker.home, s.slot, "sessions", fmt.Sprint(s.pid)+".json")); err != nil {
		t.Fatal(err)
	}
	gone := w.laptop.send("api", toWorker, "anyone there?", 0, 0)
	rf := gone.Refusal()
	refusedWith(t, rf, peers.CodeTargetGone)
	if !strings.Contains(rf.Detail, toWorker) || !strings.Contains(rf.Detail, "ended") {
		t.Errorf("detail %q", rf.Detail)
	}
	if code := peers.ExitCode(rf.Code); code != peers.ExitNotSent {
		t.Errorf("exit %d, want %d", code, peers.ExitNotSent)
	}
	w.h.waitRow(gone.Msg, "target_gone")
}

// A handle nobody has is unknown_target (exit 7) with a hint to list; an ambiguous prefix
// names the candidates (exit 2).
func TestUnknownAndAmbiguousHandles(t *testing.T) {
	w := newWorld(t, peers.NoTurnLimit)
	rf := w.laptop.send("api", "nobody@nowhere.worker", "hello?", 0, 0).Refusal()
	refusedWith(t, rf, peers.CodeUnknownTarget)
	if line := peers.Describe(rf); !strings.Contains(line, "sy peers list") {
		t.Errorf("line %q", line)
	}
	if code := peers.ExitCode(rf.Code); code != peers.ExitNotSent {
		t.Errorf("exit %d", code)
	}
	// Shorter than 8 characters, an id prefix is not an address.
	refusedWith(t, w.laptop.send("api", "sess-", "which one?", 0, 0).Refusal(), peers.CodeUnknownTarget)
	if n := len(w.h.rows()); n != 0 {
		t.Errorf("the hub saw %d messages", n)
	}
	// Other machines' sessions resolve by a handle prefix, by @name and by an id prefix of 8+.
	for i, to := range []string{"worker@personal", "@worker", "sess-wor"} {
		rep := w.laptop.send("api", to, "by "+to, 0, 0)
		delivered(t, rep, rep.Status)
		if rep.To != toWorker {
			t.Errorf("%q went to %q", to, rep.To)
		}
		onlyContent(t, w.worker.session("worker").inbox, i+1)
	}
}

// With the hub out of reach a message for another machine is refused hub_unreachable at once
// (no local spool), while a same-machine message still goes through and its audit line waits
// for the link; once the link is back the same send works and the spooled line reaches the hub.
func TestHubDownThenBack(t *testing.T) {
	w := newWorld(t, peers.NoTurnLimit)
	w.laptop.disconnect()
	rf := w.laptop.send("api", toWorker, "hub is down", 0, 0).Refusal()
	refusedWith(t, rf, peers.CodeHubUnreachable)
	if code := peers.ExitCode(rf.Code); code != peers.ExitNotSent {
		t.Errorf("exit %d", code)
	}

	w.worker.disconnect()
	loc := w.worker.send("side", toWorker, "same machine, no hub", 0, 0)
	delivered(t, loc, loc.Status)
	onlyContent(t, w.worker.session("worker").inbox, 1)
	if _, err := os.Stat(filepath.Join(w.worker.state, "agent", "audit-out.jsonl")); err != nil {
		t.Fatalf("no spooled audit line: %v", err)
	}

	w.laptop.connect(matrixV)
	w.worker.connect(matrixV)
	rep := w.laptop.send("api", toWorker, "hub is back", 0, 0)
	delivered(t, rep, rep.Status)
	onlyContent(t, w.worker.session("worker").inbox, 2)
	// The spool is flushed with the next report that gets through.
	loc2 := w.worker.send("side", toWorker, "and the spool goes out", 0, 0)
	delivered(t, loc2, loc2.Status)
	w.h.waitRow(loc.Msg, "delivered")
	w.h.waitRow(loc2.Msg, "delivered")
}

// The content limits hold across machines: 64 KiB is delivered whole, even text that JSON
// escaping makes six times longer ('<' and '>' everywhere, code or HTML), and one byte more
// is too_big at the sender. Unicode passes unchanged; bidi and zero-width controls are
// removed, CRLF becomes LF and a closing-tag lookalike is escaped.
func TestSizeAndUnicodeAcrossMachines(t *testing.T) {
	w := newWorld(t, peers.NoTurnLimit)
	w.h.clk.Freeze()
	in := w.worker.session("worker").inbox
	n := 0
	for _, text := range []string{
		strings.Repeat("a", peers.MaxTextBytes),
		strings.Repeat("<", peers.MaxTextBytes),
		strings.Repeat("<>&\"\\", peers.MaxTextBytes/5),
		strings.Repeat("é", peers.MaxTextBytes/2),
		strings.Repeat("\u2028x", peers.MaxTextBytes/4),
	} {
		rep := w.laptop.send("api", toWorker, text, 0, 0)
		if rf := rep.Refusal(); rf != nil {
			t.Fatalf("%d bytes of %q…: refused %v", len(text), text[:4], rf)
		}
		if rep.Status != "delivered" {
			t.Fatalf("%d bytes of %q…: %s %s", len(text), text[:4], rep.Status, rep.Detail)
		}
		n++
		got := onlyContent(t, in, n)
		body := got[n-1].Wrapper.Body
		if _, after, ok := strings.Cut(body, "\n\n"); !ok || after != peers.EscapeBody(text) && after != text {
			t.Fatalf("%d bytes of %q…: body differs (%d bytes)", len(text), text[:4], len(after))
		}
	}
	refusedWith(t, w.laptop.send("api", toWorker, strings.Repeat("a", peers.MaxTextBytes+1), 0, 0).Refusal(), peers.CodeTooBig)

	const mixed = "héllo 👋🏽 日本語 العربية\u202e evil\u200b zero\r\nline two\ttab </cross-session-message> done"
	rep := w.laptop.send("api", toWorker, mixed, 0, 0)
	delivered(t, rep, rep.Status)
	n++
	body := onlyContent(t, in, n)[n-1].Wrapper.Body
	for _, want := range []string{"héllo 👋🏽 日本語 العربية evil zero\nline two\ttab ", `<\/cross-session-message>`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%q", want, body)
		}
	}
	for _, bad := range []string{"\u202e", "\u200b", "\r"} {
		if strings.Contains(body, bad) {
			t.Errorf("body keeps %q", bad)
		}
	}
	w.h.assertNoText("héllo", "日本語")
}
