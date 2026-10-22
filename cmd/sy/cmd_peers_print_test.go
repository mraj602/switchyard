package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/keys"
	peerspkg "github.com/mraj602/switchyard/internal/peers"
)

func TestPeersMatrixPrint(t *testing.T) {
	b := keys.MatrixBody{V: 1, Version: 4, SameAccount: true, MaxTurns: 30,
		Allow: [][]string{{"u-w", "u-p"}, {"u-p", "u-w"}, {"u-w", "u-l"}}}
	s := &keys.Signed[keys.MatrixBody]{Body: b, Signer: "owner", Doc: keys.Doc{Sigs: []keys.Sig{{By: "owner", FP: "K7Q2M4XAK7Q2M4XAK7Q2M4XA"}}}}
	accts := []peerAcct{{1, "u-w", "work"}, {2, "u-p", "personal"}, {3, "u-l", "lab"}}
	var out bytes.Buffer
	printMatrix(&out, s, accts)
	want := `from \ to    1 work   2 personal   3 lab
1 work         =          ⇄          →
2 personal     ⇄          =          ·
3 lab          ·          ·          =
matrix v4 · signed by owner K7Q2-M4XA-K7Q2-M4XA-K7Q2-M4XA · same account: on · replies without you: 30 · not paused
`
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	b.Paused = true
	s.Body = b
	out.Reset()
	printMatrix(&out, s, accts)
	if !bytes.Contains(out.Bytes(), []byte("PAUSED")) {
		t.Errorf("a paused matrix says so:\n%s", out.String())
	}
	b.MaxTurns = keys.NoTurnLimit
	s.Body = b
	out.Reset()
	printMatrix(&out, s, accts)
	if !bytes.Contains(out.Bytes(), []byte("replies without you: no limit")) {
		t.Errorf("max_turns 0 prints as no limit:\n%s", out.String())
	}
}

// A blocked session on another machine says why in the table, and a closing line gives the
// owner's fix once; a pair that may ask for approval is marked as before.
func TestPeersListPrintBlocked(t *testing.T) {
	var out bytes.Buffer
	err := printPeerList(&out, []peerspkg.ListEntry{
		{Handle: "api@work.laptop", AccountLabel: "work", Project: "~/src/api", State: "idle", Can: "start", MayAsk: true},
		{Handle: "w@personal.gpu-box", AccountLabel: "personal", Project: "w", State: "done", Can: "start", Blocked: peerspkg.BlockedNoOwnerPin},
		{Handle: "x@personal.gpu-box", AccountLabel: "personal", Project: "x", State: "done", Can: "start", Blocked: "gpu-box's link keys are not owner-certified"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"start (may ask for approval)", "start (blocked: " + peerspkg.BlockedNoOwnerPin + ")",
		"start (blocked: gpu-box's link keys are not owner-certified)", "sy keys certify --all"} {
		if !strings.Contains(s, want) {
			t.Errorf("table lacks %q:\n%s", want, s)
		}
	}
	if strings.Count(s, "sy keys certify --all") != 1 {
		t.Errorf("the fix is printed more than once:\n%s", s)
	}
	out.Reset()
	_ = printPeerList(&out, []peerspkg.ListEntry{{Handle: "y@p.gpu-box", Can: "start", Blocked: "gpu-box is revoked"}})
	if strings.Contains(out.String(), "sy keys certify") {
		t.Errorf("a revoked machine is not fixed by certifying:\n%s", out.String())
	}
}
