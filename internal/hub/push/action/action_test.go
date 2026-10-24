package action

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

var key = bytes.Repeat([]byte{7}, 32)

func tok(now time.Time) Token {
	return Token{Alert: "al_01JX", Action: Snooze, Fire: 2, Target: "pt_abc", Expires: now.Add(Lifetime).Truncate(time.Second)}
}

func TestRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	want := tok(now)
	s := Encode(key, want)
	if !strings.HasPrefix(s, Version+".") || strings.Count(s, ".") != 2 {
		t.Fatalf("token shape %q", s)
	}
	if strings.ContainsAny(s, "+/=") {
		t.Fatalf("token not URL-safe: %q", s)
	}
	got, err := Verify(key, s, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.Alert != want.Alert || got.Action != want.Action || got.Fire != want.Fire || got.Target != want.Target ||
		!got.Expires.Equal(want.Expires) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	seen := want
	seen.Action = Seen
	if g, err := Decode(key, Encode(key, seen)); err != nil || g.Action != Seen {
		t.Fatalf("seen: %+v %v", g, err)
	}
}

func TestRejects(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	good := Encode(key, tok(now))
	parts := strings.Split(good, ".")
	other := bytes.Repeat([]byte{8}, 32)
	flip := []byte(parts[1])
	if flip[3] == 'A' {
		flip[3] = 'B'
	} else {
		flip[3] = 'A'
	}
	for _, c := range []struct {
		name string
		s    string
		k    []byte
		at   time.Time
		want error
	}{
		{"empty", "", key, now, model.ErrBadToken},
		{"garbage", "hello", key, now, model.ErrBadToken},
		{"wrong version", "a2." + parts[1] + "." + parts[2], key, now, model.ErrBadToken},
		{"bad base64", "a1.!!!." + parts[2], key, now, model.ErrBadToken},
		{"other key", good, other, now, model.ErrBadMAC},
		{"payload changed", "a1." + string(flip) + "." + parts[2], key, now, model.ErrBadMAC},
		{"mac cut", "a1." + parts[1] + "." + parts[2][:10], key, now, model.ErrBadMAC},
		{"expired", good, key, now.Add(Lifetime + time.Second), model.ErrExpired},
	} {
		_, err := Verify(c.k, c.s, c.at)
		if !errors.Is(err, c.want) {
			// A cut or flipped field may also fail to decode; both are refusals.
			if !(c.want == model.ErrBadMAC && errors.Is(err, model.ErrBadToken)) {
				t.Errorf("%s: err %v, want %v", c.name, err, c.want)
			}
		}
	}
}
