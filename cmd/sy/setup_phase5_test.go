package main

import (
	"net"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
)

func TestSetupPhase5HubFlags(t *testing.T) {
	s := newSetupHome(t, detecttest.Laptop(t))
	for _, args := range [][]string{
		{"setup", "--dry-run", "--hub", "later", "--hub-dial", "127.0.0.1:47830"},
		{"setup", "--dry-run", "--hub", "join:http://127.0.0.1:1", "--reach", "pull"},
		{"setup", "--dry-run", "--hub", "join:http://127.0.0.1:1", "--hub-dial", "10.1.2.3:80"},
		{"setup", "--dry-run", "--hub", "join:http://127.0.0.1:1", "--code-stdin", "--code", "482-913"},
		{"setup", "--dry-run", "--hub", "pull", "--code-stdin"},
	} {
		if out, errOut, code := s.sy(t, "", args...); code != 2 {
			t.Errorf("sy %v: exit %d, want 2\n%s%s", args, code, out, errOut)
		}
	}

	// Forward mode through --hub-dial, the secret on stdin. A dry run never reads stdin, so
	// it never claims; the plan shows the dial and the reach.
	var got map[string]any
	srv := claimHub(t, 200, &got)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	url := "http://hub.invalid:" + port
	secret := "Q2xhdWRlIGlzIG5vdCBoZX"
	args := []string{"--hub", "join:" + url, "--hub-dial", "127.0.0.1:" + port, "--code-stdin", "--insecure-lan", "--no-timer"}
	out, errOut, code := s.sy(t, secret+"\n", append([]string{"setup", "--dry-run", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("dry run exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, `"dial": "127.0.0.1:`+port+`"`) || !strings.Contains(out, `"reach": "forward"`) {
		t.Fatalf("plan JSON without dial/reach:\n%s", out)
	}
	text, _, _ := s.sy(t, "", append([]string{"setup", "--dry-run"}, args...)...)
	if !strings.Contains(text, "through 127.0.0.1:"+port+" (reach forward") {
		t.Fatalf("plan text without the forward:\n%s", text)
	}
	if strings.Contains(out+errOut+text, secret) {
		t.Fatal("the secret was printed")
	}
	srv.Close()
	if got != nil {
		t.Fatalf("dry run claimed: %v", got)
	}

	srv = claimHub(t, 200, &got)
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	url = "http://hub.invalid:" + port
	args = []string{"--hub", "join:" + url, "--hub-dial", "127.0.0.1:" + port, "--code-stdin", "--insecure-lan", "--no-timer"}
	out, errOut, code = s.sy(t, secret+"\n", append([]string{"setup", "--yes"}, args...)...)
	srv.Close()
	if code != 0 {
		t.Fatalf("apply exit %d\n%s%s", code, out, errOut)
	}
	if got["secret"] != secret || got["code"] != nil || got["expect_kind"] != "machine" {
		t.Fatalf("claim body %v", got)
	}
	if strings.Contains(out+errOut, secret) {
		t.Fatal("the secret was printed")
	}
	for k, want := range map[string]string{"hub.url": url, "hub.dial": "127.0.0.1:" + port, "hub.reach": "forward"} {
		if v, _, _ := s.sy(t, "", "config", "get", k); strings.TrimSpace(v) != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}

func TestSetupHubPull(t *testing.T) {
	s := newSetupHome(t, detecttest.Laptop(t))
	out, errOut, code := s.sy(t, "", "setup", "--dry-run", "--hub", "pull")
	if code != 0 || !strings.Contains(out, "pulled over SSH") {
		t.Fatalf("dry run exit %d\n%s%s", code, out, errOut)
	}
	out, errOut, code = s.sy(t, "", "setup", "--yes", "--json", "--hub", "pull", "--no-timer")
	if code != 0 {
		t.Fatalf("apply exit %d\n%s%s", code, out, errOut)
	}
	if v, _, _ := s.sy(t, "", "config", "get", "hub.reach"); strings.TrimSpace(v) != "pull" {
		t.Fatalf("hub.reach = %q", v)
	}
	if v, _, _ := s.sy(t, "", "config", "get", "hub.url"); strings.TrimSpace(v) != "" {
		t.Fatalf("hub.url = %q", v)
	}
}
