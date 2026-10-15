package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServeHint(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"127.0.0.1:8787", "tailscale serve --bg 8787"},
		{"127.0.0.1:19100", "tailscale serve --bg 19100"},
		{"[::1]:9000", "tailscale serve --bg 9000"},
		{"localhost:9001", "tailscale serve --bg 9001"},
		{"0.0.0.0:9002", "tailscale serve --bg 9002"},
		{":9003", "tailscale serve --bg 9003"},
		{"10.0.0.5:9004", "tailscale serve --bg http://10.0.0.5:9004"},
		{"http://127.0.0.1:19200", "tailscale serve --bg 19200"},
		{"http://localhost", "tailscale serve --bg 80"},
		{"", "tailscale serve --bg 8787"},
		{"garbage", "tailscale serve --bg 8787"},
	} {
		if got := ServeHint(c.in); got != c.want {
			t.Errorf("ServeHint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"http://box.lan:19200", "tailscale serve --bg 19200"},
		{"http://hub.example", "tailscale serve --bg 80"},
		{"", "tailscale serve --bg 8787"},
	} {
		if got := ServeHintFor(c.in); got != c.want {
			t.Errorf("ServeHintFor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInstalledListen(t *testing.T) {
	home := t.TempDir()
	e := Env{GOOS: "linux", Home: home, Getenv: func(string) string { return "" }}
	if got := InstalledListen(e); got != DefaultListen {
		t.Fatalf("no unit: %q", got)
	}
	unit := SystemdUnitPath(e, DefaultName)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[Service]\nExecStart=/home/u/.local/bin/sy hub serve --listen 127.0.0.1:19100 --no-pull\n"
	if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := InstalledListen(e); got != "127.0.0.1:19100" {
		t.Fatalf("unit: %q", got)
	}
	for _, args := range [][]string{{"hub", "serve", "--listen=1.2.3.4:5"}, {"hub", "serve", "--addr", "1.2.3.4:5"}} {
		if got := ListenOf(args); got != "1.2.3.4:5" {
			t.Errorf("ListenOf(%v) = %q", args, got)
		}
	}
}
