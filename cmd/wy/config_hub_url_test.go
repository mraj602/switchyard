package main

import (
	"bytes"
	"strings"
	"testing"
)

// `wy config set hub.url` runs the same URL check as `wy connect`.
func TestConfigSetHubURLIsChecked(t *testing.T) {
	t.Setenv("WY_HOME", t.TempDir())
	for _, v := range []string{"WY_CONFIG_DIR", "WY_STATE_DIR", "WY_DATA_DIR"} {
		t.Setenv(v, "")
	}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := dispatch(append([]string{"config"}, args...), &out, &errb)
		return code, out.String(), errb.String()
	}
	get := func() string { _, out, _ := run("get", "hub.url"); return strings.TrimSpace(out) }

	if code, _, errb := run("set", "hub.url", "http://hub.example.com:8787"); code == 0 || !strings.Contains(errb, "insecure-lan") {
		t.Fatalf("plain http to a public host was accepted: %d %q", code, errb)
	}
	if got := get(); got != "" {
		t.Fatalf("refused URL was stored: %q", got)
	}
	if code, _, _ := run("set", "hub.url", "not a url"); code == 0 {
		t.Fatal("garbage accepted")
	}
	if code, _, errb := run("set", "hub.url", "https://hub.example.com/path/"); code == 0 {
		t.Fatalf("path accepted: %q", errb)
	}
	// Loopback http, https and clearing are fine; the URL is normalised.
	if code, _, errb := run("set", "hub.url", "http://127.0.0.1:8787/"); code != 0 {
		t.Fatalf("loopback: %d %q", code, errb)
	}
	if got := get(); got != "http://127.0.0.1:8787" {
		t.Fatalf("stored %q", got)
	}
	if code, _, _ := run("set", "hub.url", ""); code != 0 {
		t.Fatal("clearing refused")
	}
	// --insecure-lan allows it and records hub.insecure_lan.
	if code, _, errb := run("set", "hub.url", "http://hub.example.com:8787", "--insecure-lan"); code != 0 {
		t.Fatalf("--insecure-lan: %d %q", code, errb)
	}
	if _, out, _ := run("get", "hub.insecure_lan"); strings.TrimSpace(out) != "true" {
		t.Errorf("insecure_lan not recorded: %q", out)
	}
	// Once recorded, later sets need no flag.
	if code, _, errb := run("set", "hub.url", "http://other.example.com:1"); code != 0 {
		t.Fatalf("after recording: %d %q", code, errb)
	}
}
