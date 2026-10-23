package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

// Off a terminal the first owner link goes to a 0600 file and never into the log.
func TestDeliverFirstOwnerLink(t *testing.T) {
	const block = "Owner link:\n\n  http://127.0.0.1:8790/#/owner?s=SECRET\n"
	file := filepath.Join(t.TempDir(), FirstOwnerLinkFile)
	var logged strings.Builder
	logf := func(f string, a ...any) { fmt.Fprintf(&logged, f+"\n", a...) }
	var out strings.Builder
	deliverFirstOwnerLink(logf, &out, false, file, block)
	if strings.Contains(out.String()+logged.String(), "SECRET") {
		t.Fatalf("the link reached the log: %q %q", out.String(), logged.String())
	}
	if !strings.Contains(logged.String(), file) {
		t.Errorf("the log does not name the file: %q", logged.String())
	}
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", st, err)
	}
	if b, _ := os.ReadFile(file); string(b) != block {
		t.Errorf("file holds %q", b)
	}
	// a stale wider file is replaced, and the mode is 0600 again
	os.WriteFile(file, []byte("old"), 0o644)
	deliverFirstOwnerLink(logf, &out, false, file, block)
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("stale file kept mode %v", st.Mode())
	}
	// a terminal gets the block itself and no file
	os.Remove(file)
	out.Reset()
	deliverFirstOwnerLink(logf, &out, true, file, block)
	if out.String() != block {
		t.Errorf("terminal: %q", out.String())
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("a terminal start wrote a file")
	}
}
