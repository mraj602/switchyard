//go:build fakeinbox && peerstest

package peers_test

import (
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/peers"
)

// fakeRemote is a hub with one session on another machine that queues every message. names
// is what Names returns (the hub's naming of the last answer); the remote session takes
// account, aliases from it when set.
type fakeRemote struct {
	sent    int
	last    peers.Outgoing
	account string
	aliases []string
	names   map[string]peers.HubName
}

func (r *fakeRemote) Sessions() ([]peers.Session, error) {
	acct := r.account
	if acct == "" {
		acct = "acc-remote"
	}
	return []peers.Session{{Entry: peers.RegistryEntry{SessionID: "sess-r"}, Account: acct, AccountLabel: "remote",
		Handle: "desk@remote.desk", Aliases: r.aliases, Name: "desk", MachineName: "desk", Remote: true, Device: "dev_r",
		Mode: peers.ModePrompting}}, nil
}

func (r *fakeRemote) Send(o peers.Outgoing) (*peers.Pending, *peers.Refusal) {
	r.sent++
	r.last = o
	return &peers.Pending{Expires: time.Now().Add(time.Hour), Wait: func(time.Duration) (peers.Final, bool) { return peers.Final{}, false }}, nil
}

func (r *fakeRemote) Log(peers.LocalLog) {}

func (r *fakeRemote) Names() map[string]peers.HubName { return r.names }

// The loop guard of a thread to another machine: a human prompt resets the count once, and
// the count then runs on from there (it must not be reset again by every later send).
func TestRemoteThreadLoopGuardAfterHumanPrompt(t *testing.T) {
	w := newWorld(t, peers.ModeBypass, peers.ModeBypass)
	w.allow([][]string{{"acc-work", "acc-remote"}}, map[string]any{"max_turns": 2})
	h0 := w.now.Add(-time.Hour)
	w.sessionFile("sess-a", "bypassPermissions", &h0)
	e, c := w.asA()
	e.Remote = &fakeRemote{}
	send := func() *peers.Refusal { _, rf := e.Send(c, "desk", "hi"); return rf }
	for i := 0; i < 2; i++ {
		if rf := send(); rf != nil {
			t.Fatalf("send %d: %v", i+1, rf)
		}
	}
	if rf := send(); rf == nil || rf.Code != peers.CodeLoopGuard {
		t.Fatalf("third send: %v", rf)
	}
	// The human types in session A: two more messages, then the guard again.
	h1 := w.now.Add(-time.Minute)
	w.sessionFile("sess-a", "bypassPermissions", &h1)
	for i := 0; i < 2; i++ {
		if rf := send(); rf != nil {
			t.Fatalf("send %d after the prompt: %v", i+1, rf)
		}
	}
	if rf := send(); rf == nil || rf.Code != peers.CodeLoopGuard {
		t.Fatalf("third send after the prompt: %v", rf)
	}
	th := peers.LoadThreads(e.ThreadsPath()).FindOpen("sess-a", "sess-r")
	if th == nil || !th.HumanAt.Equal(h1) || th.Turns != 2 || th.LastFrom != "sess-a" {
		t.Errorf("thread: %+v", th)
	}
}
