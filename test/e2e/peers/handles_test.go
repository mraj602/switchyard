//go:build fakeinbox

package peers

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/peers"
)

var replyLine = regexp.MustCompile(`(?m)^Reply with: sy peers send (\S+) "…"$`)

// replyHandle is the handle of the "Reply with" line of the newest message in a session's
// inbox (n frames in all).
func replyHandle(t *testing.T, s *session, n int) string {
	t.Helper()
	got := onlyContent(t, s.inbox, n)
	m := replyLine.FindStringSubmatch(got[n-1].Wrapper.Body)
	if m == nil {
		t.Fatalf("no reply line in %q", got[n-1].Wrapper.Body)
	}
	return m[1]
}

func listed(es []peers.ListEntry, handle string) (peers.ListEntry, bool) {
	for _, e := range es {
		if e.Handle == handle {
			return e, true
		}
	}
	return peers.ListEntry{}, false
}

// The bug from a real test: one Anthropic account in ~/.claude on the laptop (shown there as
// "claude") and in ~/.claude-1 on the worker ("claude-1"), whose seat at the hub is labelled
// "claude-1". Each machine used to make handles with its own label, so the laptop listed
// personal-82@claude.laptop while the worker listed personal-82@claude-1.mraj602, a send
// from the worker by the laptop's form failed, and a reply copied from the header failed
// whenever the labels differed. Now both sides list the hub's canonical handle, every form
// resolves, and the reply line of a delivered message resolves at its receiver.
func TestHandlesPortableAcrossMachines(t *testing.T) {
	h := newHub(t, 30)
	h.ownerMatrix(func(b *keys.MatrixBody) { b.SameAccount = true })
	laptop := h.newMachine("mraj602", laptopID,
		sessionSpec{slot: ".claude", seat: "claude-1", account: accPersonal, pid: 500, id: "sess-p82", name: "personal-82"},
		sessionSpec{slot: ".claude-2", label: "work", account: accWork, pid: 520, id: "sess-api", name: "api"})
	worker := h.newMachine("worker", workerID,
		sessionSpec{slot: ".claude-1", seat: "claude-1", account: accPersonal, pid: 600, id: "sess-helper", name: "helper"})
	laptop.connect(matrixV + 1)
	worker.connect(matrixV + 1)

	const p82, helper = "personal-82@claude-1.mraj602", "helper@claude-1.worker"

	// Both sides list the same handle for each session, with the other labels as aliases.
	onWorker := worker.list("helper")
	if e, ok := listed(onWorker, p82); !ok || !slices.Contains(e.Aliases, "personal-82@claude.laptop") || e.AccountLabel != "claude-1" {
		t.Errorf("worker lists %+v", onWorker)
	}
	onLaptop := laptop.list("api")
	if e, ok := listed(onLaptop, p82); !ok || !slices.Contains(e.Aliases, "personal-82@claude.laptop") {
		t.Errorf("laptop lists %+v", onLaptop)
	}
	if e, ok := listed(onLaptop, helper); !ok || !slices.Contains(e.Aliases, "helper@claude.worker") {
		t.Errorf("laptop lists %+v", onLaptop)
	}
	if e, ok := listed(laptop.list("personal-82"), helper); !ok {
		t.Errorf("personal-82 does not see %s: %+v", helper, e)
	}
	for _, es := range [][]peers.ListEntry{onWorker, onLaptop} {
		for _, e := range es {
			if strings.Contains(e.Handle, "@claude.") {
				t.Errorf("a local label in a handle: %s", e.Handle)
			}
		}
	}

	// The worker sends by the laptop's old form (the report's failing command), then by the
	// canonical handle; the laptop answers with the reply line it was given.
	n := 0
	for _, to := range []string{"personal-82@claude.laptop", p82, "personal-82@claude"} {
		rep := worker.send("helper", to, "from the worker to "+to, 0, 0)
		delivered(t, rep, rep.Status)
		if rep.To != p82 {
			t.Errorf("send to %s delivered to %s", to, rep.To)
		}
		n++
		got := replyHandle(t, laptop.session("personal-82"), n)
		if got != helper {
			t.Errorf("reply line names %s, want %s", got, helper)
		}
		back := laptop.send("personal-82", got, "back to "+got, 0, 0)
		delivered(t, back, back.Status)
		if back.To != helper || back.Thread != rep.Thread {
			t.Errorf("reply to %s: to %s thread %s, want %s", got, back.To, back.Thread, rep.Thread)
		}
	}
	onlyContent(t, worker.session("helper").inbox, n)

	// The laptop sends by its own label for the account (an alias only it knows) and by the
	// canonical handle; the worker's reply line names the laptop session canonically and
	// resolves there.
	m := n
	for _, to := range []string{"helper@claude.worker", helper} {
		rep := laptop.send("personal-82", to, "from the laptop to "+to, 0, 0)
		delivered(t, rep, rep.Status)
		m++
		got := replyHandle(t, worker.session("helper"), m)
		if got != p82 {
			t.Errorf("reply line names %s, want %s", got, p82)
		}
		back := worker.send("helper", got, "back to "+got, 0, 0)
		delivered(t, back, back.Status)
		if back.To != p82 {
			t.Errorf("reply to %s: %s", got, back.To)
		}
	}

	// On the laptop itself, its local form still reaches its own session, and the header
	// names the sender canonically there too.
	loc := laptop.send("api", "personal-82@claude.laptop", "same machine", 0, 0)
	delivered(t, loc, loc.Status)
	if loc.To != p82 {
		t.Errorf("same machine: %s", loc.To)
	}
	n += 2 + 1 // two replies from the worker, then this one
	if got := replyHandle(t, laptop.session("personal-82"), n); got != "api@work.mraj602" {
		t.Errorf("same-machine reply line: %s", got)
	}
}
