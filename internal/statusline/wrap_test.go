package statusline

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWrapBackgroundChildKeepsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	sb := newSandbox(t)
	got := plain(sb.run(fixture(t, "full.json"), "--wrap", "sleep 5 & echo bg"))
	if !strings.HasPrefix(got, "bg │ claude-2") {
		t.Errorf("background child dropped the output: %q", got)
	}
}

func TestWrapTimeoutShowsLastGoodOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	sb := newSandbox(t)
	in := fixture(t, "full.json")
	if got := plain(sb.run(in, "--wrap", "echo cached")); !strings.HasPrefix(got, "cached │ ") {
		t.Fatalf("first run: %q", got)
	}
	if got := plain(sb.run(in, "--wrap", "sleep 10; echo late")); !strings.HasPrefix(got, "cached │ claude-2") {
		t.Errorf("timeout should show the cached output: %q", got)
	}
}

func TestWrapTrailingBlankLinesAreTrimmed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	sb := newSandbox(t)
	out := sb.run(fixture(t, "full.json"), "--wrap", `printf 'top\n\n\n'`)
	if strings.Count(out, "\n") != 1 || !strings.HasPrefix(plain(out), "top │ claude-2") {
		t.Errorf("%q", out)
	}
}

func TestBadFlagStillPrintsWrappedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	sb := newSandbox(t)
	var out bytes.Buffer
	err := Run([]string{"--bogus", "--wrap", "echo hi"}, bytes.NewReader(fixture(t, "full.json")), &out, io.Discard, sb.getenv)
	if err == nil {
		t.Error("want the flag error returned")
	}
	if got := plain(out.String()); !strings.HasPrefix(got, "hi │ claude-2") {
		t.Errorf("bad flag blanked the line: %q", got)
	}
	out.Reset()
	_ = Run([]string{"--bogus"}, bytes.NewReader(nil), &out, io.Discard, sb.getenv)
	if strings.TrimSpace(plain(out.String())) == "" {
		t.Errorf("empty line: %q", out.String())
	}
}

func TestShowOffPrintsOnlyTheWrappedLineAndStillSamples(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	sb := newSandbox(t)
	cfg := filepath.Join(sb.home, "wy", "config", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[statusline]\nshow = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sb.run(fixture(t, "full.json"), "--wrap", "echo mine"); got != "mine\n" {
		t.Errorf("want the wrapped line alone, got %q", got)
	}
	if len(sb.samples()) != 1 {
		t.Errorf("show = false must still record the limit sample, got %d", len(sb.samples()))
	}
}
