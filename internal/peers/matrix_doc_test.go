package peers

import (
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/keys"
)

func TestDocMatrixAllowed(t *testing.T) {
	const W, P, L = "acc-work", "acc-personal", "acc-lab"
	body := func(mut func(*keys.MatrixBody)) keys.MatrixBody {
		b := keys.BaselineMatrix("hub", time.Unix(0, 0))
		b.Accounts = map[string]string{W: "work", P: "personal", L: "lab"}
		b.Allow = [][]string{{W, P}, {P, W}, {W, L}} // work <-> personal, work -> lab
		if mut != nil {
			mut(&b)
		}
		return b
	}
	open := func(a, b string) *Thread { // a opened it
		return &Thread{ID: "t_x", A: a, B: b, OpenedAt: time.Unix(1, 0)}
	}
	closed := func(a, b string) *Thread {
		th := open(a, b)
		c := time.Unix(2, 0)
		th.ClosedAt = &c
		return th
	}
	party := func(sess, acc string) Party { return Party{Session: sess, Account: acc} }

	tests := []struct {
		name   string
		b      keys.MatrixBody
		from   Party
		to     Party
		th     *Thread
		want   bool
		reason string
	}{
		{"none cell", body(nil), party("l", L), party("p", P), nil, false, CodeNotAllowed},
		{"one way, forward", body(nil), party("w", W), party("l", L), nil, true, ""},
		{"one way, backward start", body(nil), party("l", L), party("w", W), nil, false, CodeNotAllowed},
		{"both ways a to b", body(nil), party("w", W), party("p", P), nil, true, ""},
		{"both ways b to a", body(nil), party("p", P), party("w", W), nil, true, ""},
		{"unrelated pair", body(nil), party("l", L), party("p", P), nil, false, CodeNotAllowed},
		{"reply within a thread the target opened", body(nil), party("l", L), party("w", W), open("w", "l"), true, ""},
		{"no reply when the sender opened the thread", body(nil), party("l", L), party("w", W), open("l", "w"), false, CodeNotAllowed},
		{"no reply in a closed thread", body(nil), party("l", L), party("w", W), closed("w", "l"), false, CodeNotAllowed},
		{"reply stops when the opener's cell is narrowed", body(func(b *keys.MatrixBody) { b.Allow = [][]string{{P, W}} }),
			party("l", L), party("w", W), open("w", "l"), false, CodeNotAllowed},
		{"same account on", body(nil), party("a", W), party("b", W), nil, true, ""},
		{"same account off", body(func(b *keys.MatrixBody) { b.SameAccount = false }), party("a", W), party("b", W), nil, false, CodeNotAllowed},
		{"paused refuses the allowed pair", body(func(b *keys.MatrixBody) { b.Paused = true }), party("w", W), party("p", P), nil, false, CodePaused},
		{"paused refuses the diagonal", body(func(b *keys.MatrixBody) { b.Paused = true }), party("a", W), party("b", W), nil, false, CodePaused},
		{"paused refuses a reply", body(func(b *keys.MatrixBody) { b.Paused = true }), party("l", L), party("w", W), open("w", "l"), false, CodePaused},
		{"unknown sender account", body(nil), party("x", ""), party("w", W), nil, false, CodeNotAllowed},
		{"two unknown accounts are not the same account", body(nil), party("x", ""), party("y", ""), nil, false, CodeNotAllowed},
		{"version-1 baseline: nothing allowed", func() keys.MatrixBody { return keys.BaselineMatrix("hub", time.Unix(0, 0)) }(),
			party("w", W), party("p", P), nil, false, CodeNotAllowed},
		{"version-1 baseline: same account on", keys.BaselineMatrix("hub", time.Unix(0, 0)), party("a", W), party("b", W), nil, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDocMatrix(tc.b).Allowed(tc.from, tc.to, tc.th)
			if d.Allowed != tc.want || d.Reason != tc.reason {
				t.Fatalf("got %+v, want allowed=%v reason=%q", d, tc.want, tc.reason)
			}
		})
	}
}

func TestDocMatrixDetailAndMaxTurns(t *testing.T) {
	b := keys.BaselineMatrix("hub", time.Unix(0, 0))
	b.Accounts = map[string]string{"u1": "work", "u2": "personal"}
	m := NewDocMatrix(b)
	d := m.Allowed(Party{Account: "u1"}, Party{Account: "u2"}, nil)
	if d.Detail != "the matrix does not allow work → personal" {
		t.Errorf("detail %q (labels come from the document when the party has none)", d.Detail)
	}
	d = m.Allowed(Party{Account: "u1", Label: "w"}, Party{Account: "u2", Label: "p"}, nil)
	if d.Detail != "the matrix does not allow w → p" {
		t.Errorf("detail %q", d.Detail)
	}
	if m.MaxTurns() != NoTurnLimit {
		t.Errorf("baseline max turns %d, want no limit", m.MaxTurns())
	}
	b.MaxTurns = 7
	if NewDocMatrix(b).MaxTurns() != 7 {
		t.Error("max_turns of the document is used")
	}
	var _ Matrix = m
}
