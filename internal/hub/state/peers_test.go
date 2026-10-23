package state

import (
	"encoding/json"
	"testing"
	"time"
)

// states.md 3.1: Session.Peers follows the turn and never changes the state.
func TestFoldPeers(t *testing.T) {
	q := "Done.\n\nShould I go on?"
	h := func(name string, mod func(*Hook)) *Hook {
		x := &Hook{Name: name}
		if mod != nil {
			mod(x)
		}
		return x
	}
	peerPrompt := func(x *Hook) { x.PeerPrompt = true }
	question := func(x *Hook) { x.LastMessage, x.HasLastMessage = q, true }
	wait := func(x *Hook) { question(x); x.PeerWait = true }
	cases := []struct {
		hook  *Hook
		state State
		peers string
	}{
		{h("SessionStart", func(x *Hook) { x.Source = "startup" }), Done, ""},
		{h("UserPromptSubmit", peerPrompt), Working, PeersMessage},
		{h("PermissionRequest", nil), NeedsPermission, PeersMessage},
		{h("Stop", question), Asked, PeersMessage},
		{h("Stop", wait), Asked, PeersMessage}, // a peer turn stays a peer turn
		{h("UserPromptSubmit", nil), Working, ""},
		{h("Stop", question), Asked, ""},
		{h("UserPromptSubmit", nil), Working, ""},
		{h("Stop", wait), Asked, PeersWait},
		{h("Stop", func(x *Hook) { wait(x); x.StopHookActive = true }), Asked, PeersWait},
		{h("SessionStart", func(x *Hook) { x.Source = "compact" }), Asked, PeersWait},
		{h("SessionStart", func(x *Hook) { x.Source = "resume" }), Done, ""},
	}
	var s Session
	for i, c := range cases {
		s.Apply(Input{ID: "r", TS: t0.Add(time.Duration(i) * time.Second), Hook: c.hook})
		if s.State != c.state || s.Peers != c.peers {
			t.Fatalf("step %d (%s): %s peers=%q, want %s %q", i, c.hook.Name, s.State, s.Peers, c.state, c.peers)
		}
	}
}

func TestPeerFacts(t *testing.T) {
	var x Hook
	x.PeerFacts(map[string]json.RawMessage{"peer_prompt": json.RawMessage("true"), "peer_wait": json.RawMessage(`"true"`)})
	if !x.PeerPrompt || x.PeerWait {
		t.Errorf("%+v: only a JSON true counts", x)
	}
	x.PeerFacts(nil)
	if x.PeerPrompt || x.PeerWait {
		t.Errorf("%+v", x)
	}
}
