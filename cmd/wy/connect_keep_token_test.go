package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
)

func hubConfig(t *testing.T, deps connectDeps) config.Config {
	t.Helper()
	f, err := config.Load(deps.dirs.Config.Path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Typed()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConnectCodeStdinDigitsAndSecret(t *testing.T) {
	for _, tc := range []struct{ in, field string }{
		{"482-913\n", "code"},
		{"Q2xhdWRlIGlzIG5vdCBoZX\n", "secret"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			deps, _, _ := sandbox(t)
			var got map[string]any
			srv := claimHub(t, 200, &got)
			deps.stdin = strings.NewReader(tc.in)
			var out, errb bytes.Buffer
			if err := connect(context.Background(), deps, []string{srv.URL, "--code-stdin", "--no-timer"}, &out, &errb); err != nil {
				t.Fatal(err, errb.String())
			}
			want := strings.TrimSpace(tc.in)
			if got[tc.field] != want || len(got) == 0 {
				t.Fatalf("claim body %v, want %s=%s", got, tc.field, want)
			}
			other := map[string]string{"code": "secret", "secret": "code"}[tc.field]
			if _, ok := got[other]; ok {
				t.Fatalf("claim body has both: %v", got)
			}
			if tok, _ := client.ReadToken(deps.dirs.State.Path); tok != goodToken {
				t.Fatal("token not stored")
			}
			raw, _ := os.ReadFile(filepath.Join(deps.dirs.Config.Path, config.FileName))
			if strings.Contains(string(raw), "reach") || strings.Contains(string(raw), "dial") {
				t.Fatalf("a plain connect wrote reach/dial:\n%s", raw)
			}
		})
	}
}

func TestConnectKeepToken(t *testing.T) {
	deps, _, _ := sandbox(t)
	hub := newCmdHub(t)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(hub.srv.URL, "http://"))
	// The new URL names a host that does not resolve: only the machine's own hub.dial (the
	// forward) reaches the hub, as it does on a forward machine after a repoint.
	newURL := "http://hub.invalid:" + port
	st := deps.dirs.State.Path
	os.MkdirAll(st, 0o700)
	f, _ := config.Load(deps.dirs.Config.Path)
	f.Set("hub.url", []string{"https://old.example"})
	f.Set("hub.reach", []string{"forward"})
	f.Set("hub.dial", []string{"127.0.0.1:" + port})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	other := "wy_m_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	atomicfile.Write(filepath.Join(st, client.TokenFile), []byte(other+"\n"), 0o600)
	var out, errb bytes.Buffer
	err := connect(context.Background(), deps, []string{newURL, "--keep-token", "--no-timer", "--insecure-lan"}, &out, &errb)
	if err == nil {
		t.Fatal("a hub that does not know the token was accepted")
	}
	if c := hubConfig(t, deps); c.Hub.URL != "https://old.example" {
		t.Fatalf("hub.url changed to %q after a failed --keep-token", c.Hub.URL)
	}
	if tok, _ := client.ReadToken(st); tok != other {
		t.Fatal("token file changed")
	}

	atomicfile.Write(filepath.Join(st, client.TokenFile), []byte(goodToken+"\n"), 0o600)
	before, _ := os.ReadFile(filepath.Join(st, client.TokenFile))
	out.Reset()
	if err := connect(context.Background(), deps, []string{newURL, "--keep-token", "--no-timer", "--insecure-lan"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	c := hubConfig(t, deps)
	if c.Hub.URL != newURL || c.Hub.Reach != "forward" || c.Hub.Dial != "127.0.0.1:"+port {
		t.Fatalf("config after keep-token: %+v", c.Hub)
	}
	after, _ := os.ReadFile(filepath.Join(st, client.TokenFile))
	if !bytes.Equal(before, after) {
		t.Fatal("token rewritten")
	}
	// --dial with --keep-token is saved, not dropped (a repoint of a forward machine).
	f2, _ := config.Load(deps.dirs.Config.Path)
	f2.Set("hub.dial", []string{""})
	f2.Set("hub.reach", []string{"direct"})
	f2.Save()
	if err := connect(context.Background(), deps, []string{"http://127.0.0.1:" + port, "--keep-token", "--no-timer", "--insecure-lan", "--dial", "127.0.0.1:" + port}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	if c := hubConfig(t, deps); c.Hub.Dial != "127.0.0.1:"+port || c.Hub.Reach != "forward" {
		t.Fatalf("--keep-token --dial not saved: %+v", c.Hub)
	}
	// --keep-token excludes the other ways.
	if err := connect(context.Background(), deps, []string{newURL, "--keep-token", "--token", goodToken}, &out, &errb); err == nil {
		t.Fatal("--keep-token with --token accepted")
	}
}

func TestConnectDialAndReach(t *testing.T) {
	deps, _, _ := sandbox(t)
	hub := newCmdHub(t)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(hub.srv.URL, "http://"))
	// The URL names a host that does not resolve; --dial reaches the hub on loopback.
	url := "http://hub.invalid:" + port
	var out, errb bytes.Buffer
	err := connect(context.Background(), deps, []string{url, "--token", goodToken, "--dial", "127.0.0.1:" + port, "--insecure-lan", "--no-timer"}, &out, &errb)
	if err != nil {
		t.Fatal(err, errb.String())
	}
	c := hubConfig(t, deps)
	if c.Hub.URL != url || c.Hub.Dial != "127.0.0.1:"+port || c.Hub.Reach != config.ReachForward {
		t.Fatalf("hub %+v", c.Hub)
	}
	for _, bad := range [][]string{
		{url, "--token", goodToken, "--dial", "10.0.0.1:80"},
		{url, "--token", goodToken, "--reach", "pull"},
	} {
		if err := connect(context.Background(), deps, bad, &out, &errb); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
