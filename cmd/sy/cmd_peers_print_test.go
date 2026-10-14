package main

import (
	"bytes"
	"testing"

	"github.com/mraj602/switchyard/internal/keys"
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
matrix v4 · signed by owner K7Q2-M4XA-K7Q2-M4XA-K7Q2-M4XA · same account: on · max turns: 30 · not paused
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
}
