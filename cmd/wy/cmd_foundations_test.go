package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPathsCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WY_HOME", home)
	t.Setenv("WY_STATE_DIR", home+"/st")
	var out, errb bytes.Buffer
	if code := dispatch([]string{"paths"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := []string{
		"config  " + home + "/config  (WY_HOME)",
		"state   " + home + "/st  (WY_STATE_DIR)",
		"data    " + home + "/data  (WY_HOME)",
	}
	for _, w := range want {
		if !strings.Contains(out.String(), w) {
			t.Errorf("missing %q in\n%s", w, out.String())
		}
	}
}

func TestConfigGetSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WY_HOME", home)
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := dispatch(append([]string{"config"}, args...), &out, &errb)
		return code, out.String()
	}
	if code, out := run("get", "outbox.max_mb"); code != 0 || out != "50\n" {
		t.Fatalf("default get: %d %q", code, out)
	}
	if code, _ := run("set", "outbox.max_mb", "80"); code != 0 {
		t.Fatalf("set: exit %d", code)
	}
	if code, out := run("get", "outbox.max_mb"); code != 0 || out != "80\n" {
		t.Fatalf("get after set: %d %q", code, out)
	}
	run("set", "slots.extra", "~/a", "~/b")
	if _, out := run("get", "slots.extra"); out != "[\"~/a\",\"~/b\"]\n" {
		t.Errorf("list: %q", out)
	}
	if code, _ := run("set", "nope", "1"); code != 1 {
		t.Errorf("unknown key set: exit %d", code)
	}
	if code, _ := run("get", "nope"); code != 1 {
		t.Errorf("unknown key get: exit %d", code)
	}
	if code, _ := run("get"); code != 2 {
		t.Errorf("usage: exit %d", code)
	}
	if code, _ := run("frob"); code != 2 {
		t.Errorf("bad subcommand: exit %d", code)
	}
}
