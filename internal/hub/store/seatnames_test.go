package store

import "testing"

// The board's seatName (webui components/seat.js) and push titles agree (push.md 5.5).
func TestSeatDisplayNames(t *testing.T) {
	got := SeatDisplayNames([]SeatIdent{
		{ID: "a", Email: "alex@acme.dev"},
		{ID: "b", Email: "alex@gmail.com"},
		{ID: "c", Email: "mayank.raj@tohands.in"},
		{ID: "d", Email: "mayankraj@cyphersage.in"},
		{ID: "e", Label: "work", Email: "sam@acme.dev"},
		{ID: "f", Email: "sam@other.org"},
		{ID: "g"},
	})
	want := map[string]string{"a": "alex · acme", "b": "alex · gmail", "c": "mayank.raj · tohands",
		"d": "mayankraj · cyphersage", "e": "work", "f": "sam", "g": "g"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
}
