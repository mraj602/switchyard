package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "sy ") {
		t.Fatalf("unexpected output %q", out.String())
	}
	out.Reset()
	if code := dispatch([]string{"version", "-short"}, &out, &errb); code != 0 || strings.TrimSpace(out.String()) == "" {
		t.Fatalf("short: exit %d, out %q", code, out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch([]string{"nope"}, &out, &errb); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

func TestNoArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch(nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "Usage") {
		t.Fatalf("want usage and exit 2, got %d %q", code, errb.String())
	}
}

// `sy hook` must never fail a Claude Code session, even with empty stdin.
func TestHookExitsZero(t *testing.T) {
	t.Setenv("SY_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := dispatch([]string{"hook", "Stop"}, &out, &errb); code != 0 {
		t.Fatalf("hook must exit 0, got %d", code)
	}
}
