package peerlink

import (
	"slices"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/peers"
)

// Names reads only the cached answer: nil before one, then the hub's naming of this machine's
// own sessions (mine) and the others', with the canonical label (the account uuid's first 8
// characters for a seat with no label). Sessions carry the hub's aliases.
func TestNamesFromTheCachedAnswer(t *testing.T) {
	r := New(peers.Env{Machine: identity.Machine{ID: "m_A", Name: "laptop"}}, nil)
	if r.Names() != nil {
		t.Fatal("names before any answer")
	}
	dev := "dev_w"
	r.cache.reply = &peers.PeersReply{V: 1,
		Sessions: []peers.WireSession{{Session: "s-w", Handle: "x@3f1c2a9b.worker", Name: "x", Account: "3f1c2a9b-1111", MachineID: "m_W",
			Machine: "worker", Aliases: []string{"x@claude-1.worker"}, Device: &dev}},
		Mine: []peers.WireSession{{Session: "s-l", Handle: "p@claude-1.laptop", Name: "p", Account: "acc", AccountLabel: "claude-1",
			MachineID: "m_A", Machine: "laptop", Aliases: []string{"p@claude.laptop"}}},
	}
	n := r.Names()
	if w := n["s-w"]; w.Mine || w.Handle != "x@3f1c2a9b.worker" || w.Label != "3f1c2a9b" || !slices.Equal(w.Aliases, []string{"x@claude-1.worker"}) {
		t.Errorf("other machine: %+v", w)
	}
	if l := n["s-l"]; !l.Mine || l.Handle != "p@claude-1.laptop" || l.Label != "claude-1" || l.Name != "p" {
		t.Errorf("mine: %+v", l)
	}
	r.cache.at, r.Fresh = time.Now(), time.Hour // the answer is fresh: Sessions does not ask
	ss, err := r.Sessions()
	if err != nil || len(ss) != 1 || ss[0].Handle != "x@3f1c2a9b.worker" || ss[0].AccountLabel != "3f1c2a9b" ||
		!slices.Equal(ss[0].Aliases, []string{"x@claude-1.worker"}) {
		t.Errorf("sessions: %+v %v", ss, err)
	}
}
