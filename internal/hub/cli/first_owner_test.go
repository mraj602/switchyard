package cli

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// pairing.md 4.1: while the hub never had an owner session, the first-start block has a
// working owner link, a QR, the board URL and the next step; once someone is owner, nothing.
func TestFirstOwnerLink(t *testing.T) {
	db := pairEnv(t)
	ctx := context.Background()
	var out bytes.Buffer
	printed, err := FirstOwnerLinkDB(ctx, db, "http://127.0.0.1:8790", &out, false)
	if err != nil || !printed {
		t.Fatalf("printed %v, %v", printed, err)
	}
	s := out.String()
	for _, w := range []string{"http://127.0.0.1:8790/#/owner?s=", "Board: http://127.0.0.1:8790/", FirstOwnerNext, "█"} {
		if !strings.Contains(s, w) {
			t.Errorf("block lacks %q:\n%s", w, s)
		}
	}
	if strings.Contains(s, "\x1b[") {
		t.Error("colour codes in a block meant for a log")
	}
	_, rest, _ := strings.Cut(s, "/#/owner?s=")
	secret, _, _ := strings.Cut(rest, "\n")
	st, err := store.Open(ctx, db, store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := pair.NewService(st, "").OwnerSignIn(ctx, "1.1.1.1", strings.TrimSpace(secret), ""); err != nil {
		t.Fatalf("the first link does not sign in: %v", err)
	}
	block, err := FirstOwnerLink(ctx, st, nil, "http://127.0.0.1:8790", "serve", false)
	if err != nil || block != "" {
		t.Errorf("a hub with an owner still printed a first link: %q %v", block, err)
	}
}

func TestServeURL(t *testing.T) {
	db := pairEnv(t)
	st, err := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, c := range []struct{ pub, addr, want string }{
		{"https://hub.example.ts.net", "127.0.0.1:8787", "https://hub.example.ts.net"},
		{"", "127.0.0.1:8790", "http://127.0.0.1:8790"},
		{"", "0.0.0.0:8790", "http://127.0.0.1:8790"},
	} {
		addr, _ := net.ResolveTCPAddr("tcp", c.addr)
		if got := serveURL(context.Background(), st, c.pub, addr); got != c.want {
			t.Errorf("%q %s: %q, want %q", c.pub, c.addr, got, c.want)
		}
	}
}
