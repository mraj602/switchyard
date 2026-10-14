package keys

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestAcceptedMatrixFile(t *testing.T) {
	state := t.TempDir()
	hubPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	ownerPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, 32))
	now := time.Now()
	hubFP := Fingerprint(hubPriv.Public().(ed25519.PublicKey))

	doc := func(version int, by string, mut func(*MatrixBody)) Doc {
		b := BaselineMatrix(hubFP, now)
		b.Version = version
		if mut != nil {
			mut(&b)
		}
		raw, _ := json.Marshal(b)
		d := NewDoc(TypeMatrix, raw)
		key := hubPriv
		if by == ByOwner {
			key = ownerPriv
		}
		if err := d.Sign(by, key); err != nil {
			t.Fatal(err)
		}
		return d
	}
	widen := func(b *MatrixBody) { b.Allow = [][]string{{"a", "b"}} }

	// No pins, no file: nothing, and saving is refused.
	if s, err := LoadAcceptedMatrix(state); s != nil || err != nil {
		t.Fatalf("empty state: %v %v", s, err)
	}
	if _, _, err := SaveAcceptedMatrix(state, doc(1, ByHub, nil), true); err == nil {
		t.Fatal("saved without a hub pin")
	}
	if err := SetPin(Path(state, HubPinFile), NewPin(hubPriv.Public().(ed25519.PublicKey), PinLocal, now)); err != nil {
		t.Fatal(err)
	}
	if err := SetPin(Path(state, OwnerPinFile), NewPin(ownerPriv.Public().(ed25519.PublicKey), PinLocal, now)); err != nil {
		t.Fatal(err)
	}

	// Version 1 (hub-signed baseline) is taken; the file is private.
	if _, ok, err := SaveAcceptedMatrix(state, doc(1, ByHub, nil), false); !ok || err != nil {
		t.Fatalf("v1: %v %v", ok, err)
	}
	if fi, err := os.Stat(Path(state, MatrixFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("matrix.json mode: %v %v", fi, err)
	}
	// A lower or equal version is ignored, not an error.
	if _, ok, err := SaveAcceptedMatrix(state, doc(1, ByHub, widen), true); ok || err != nil {
		t.Fatalf("same version: %v %v", ok, err)
	}
	// hub_may_widen = false: a hub-signed widening is refused and nothing changes.
	if _, ok, err := SaveAcceptedMatrix(state, doc(2, ByHub, widen), false); ok || !errors.Is(err, ErrHubWidening) {
		t.Fatalf("hub widening, hub_may_widen=false: %v %v", ok, err)
	}
	if s, _ := LoadAcceptedMatrix(state); s.Body.Version != 1 {
		t.Fatalf("stored v%d", s.Body.Version)
	}
	// ... an owner-signed widening and a hub-signed narrowing are always taken. A version may
	// be skipped (the machine missed some).
	if _, ok, err := SaveAcceptedMatrix(state, doc(3, ByOwner, widen), false); !ok || err != nil {
		t.Fatalf("owner widening: %v %v", ok, err)
	}
	if _, ok, err := SaveAcceptedMatrix(state, doc(5, ByHub, func(b *MatrixBody) { widen(b); b.Paused = true }), false); !ok || err != nil {
		t.Fatalf("hub narrowing (pause): %v %v", ok, err)
	}
	s, err := LoadAcceptedMatrix(state)
	if err != nil || s.Body.Version != 5 || !s.Body.Paused || s.Signer != ByHub {
		t.Fatalf("stored %+v %v", s, err)
	}
	// A document of another hub and a forged one are refused.
	other := BaselineMatrix("someone-else", now)
	other.Version = 9
	raw, _ := json.Marshal(other)
	d := NewDoc(TypeMatrix, raw)
	_ = d.Sign(ByHub, hubPriv)
	if _, _, err := SaveAcceptedMatrix(state, d, true); !errors.Is(err, ErrWrongHub) {
		t.Fatalf("another hub's document: %v", err)
	}
	forged := doc(9, ByOwner, widen)
	forged.Sigs[0].Sig = doc(9, ByHub, widen).Sigs[0].Sig
	if _, _, err := SaveAcceptedMatrix(state, forged, true); err == nil {
		t.Fatal("forged signature accepted")
	}
	// A file edited by hand no longer verifies: it loads as an error, so nothing is allowed.
	if err := os.WriteFile(Path(state, MatrixFile), bytes.ReplaceAll(mustRead(t, Path(state, MatrixFile)), []byte(`"type":"matrix"`), []byte(`"type":"revocations"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadAcceptedMatrix(state); s != nil || err == nil {
		t.Fatalf("tampered file: %v %v", s, err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
