package hubsrc

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func collect(t *testing.T, r io.Reader) []Event {
	t.Helper()
	p := NewParser(r)
	var out []Event
	for {
		ev, err := p.Next()
		if err != nil {
			return out
		}
		out = append(out, ev)
	}
}

func TestSSECorpus(t *testing.T) {
	cases := []struct {
		name, in string
		want     []Event
	}{
		{"lf", "id: 7\nevent: session\ndata: {\"a\":1}\n\n", []Event{{ID: "7", Type: "session", Data: `{"a":1}`}}},
		{"crlf", "id: 8\r\nevent: usage\r\ndata: x\r\n\r\n", []Event{{ID: "8", Type: "usage", Data: "x"}}},
		{"bare cr", "event: a\rdata: b\r\r", []Event{{Type: "a", Data: "b"}}},
		{"multi-line data", "data: one\ndata: two\ndata:\ndata: four\n\n", []Event{{Type: "message", Data: "one\ntwo\n\nfour"}}},
		{"retry", "retry: 3000\ndata: x\n\n", []Event{{Type: "message", Data: "x", Retry: 3000}}},
		{"bad retry ignored", "retry: soon\ndata: x\n\n", []Event{{Type: "message", Data: "x"}}},
		{"comment", ": ping\ndata: x\n\n", []Event{{Comment: true, Data: "ping"}, {Type: "message", Data: "x"}}},
		{"id without data", "id: 9\n\nid: 10\nevent: s\ndata: y\n\n", []Event{{ID: "10", Type: "s", Data: "y"}}},
		{"event type resets", "event: a\n\ndata: x\n\n", []Event{{Type: "message", Data: "x"}}},
		{"no space after colon", "data:x\n\n", []Event{{Type: "message", Data: "x"}}},
		{"one space only", "data:  x\n\n", []Event{{Type: "message", Data: " x"}}},
		{"unknown field", "foo: bar\ndata: x\n\n", []Event{{Type: "message", Data: "x"}}},
		{"empty data not dispatched", "data:\n\ndata: z\n\n", []Event{{Type: "message", Data: "z"}}},
		{"unterminated tail dropped", "data: a\n\ndata: b", []Event{{Type: "message", Data: "a"}}},
		{"id sticks", "id: 3\ndata: a\n\ndata: b\n\n", []Event{{ID: "3", Type: "message", Data: "a"}, {ID: "3", Type: "message", Data: "b"}}},
		{"hub style", "id: 4182\nevent: session\ndata: {\"session\":{\"id\":\"c41e\"},\"change\":\"state\"}\n\nevent: ping\ndata: {\"time\":\"x\"}\n\n",
			[]Event{{ID: "4182", Type: "session", Data: `{"session":{"id":"c41e"},"change":"state"}`}, {ID: "4182", Type: "ping", Data: `{"time":"x"}`}}},
	}
	for _, c := range cases {
		readers := map[string]io.Reader{
			"whole":    strings.NewReader(c.in),
			"one byte": iotest.OneByteReader(strings.NewReader(c.in)),
			"half":     iotest.HalfReader(strings.NewReader(c.in)),
		}
		for rn, r := range readers {
			got := collect(t, r)
			if len(got) != len(c.want) {
				t.Errorf("%s/%s: %d events %+v, want %d", c.name, rn, len(got), got, len(c.want))
				continue
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("%s/%s: event %d = %+v, want %+v", c.name, rn, i, got[i], c.want[i])
				}
			}
		}
	}
}

func TestSSELastIDWithoutData(t *testing.T) {
	p := NewParser(strings.NewReader("id: 41\n\n"))
	if _, err := p.Next(); err == nil {
		t.Fatal("an id without data dispatched an event")
	}
	if p.LastID() != "41" {
		t.Errorf("LastID = %q, want 41", p.LastID())
	}
}

func TestSSETooLarge(t *testing.T) {
	big := "data: " + strings.Repeat("x", MaxEventBytes+10) + "\n\n"
	if _, err := NewParser(strings.NewReader(big)).Next(); err != ErrTooLarge {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	var b strings.Builder
	line := "data: " + strings.Repeat("y", 1<<20) + "\n"
	for i := 0; i < 6; i++ {
		b.WriteString(line)
	}
	b.WriteString("\n")
	if _, err := NewParser(strings.NewReader(b.String())).Next(); err != ErrTooLarge {
		t.Fatalf("summed: %v", err)
	}
}
