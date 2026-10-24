package service

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/model"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/service -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

type call struct{ name, args string }

type rec struct {
	calls []call
	out   map[string]string // "name args" prefix -> output
	fail  map[string]bool
}

func (r *rec) Run(name string, args ...string) ([]byte, error) {
	a := strings.Join(args, " ")
	r.calls = append(r.calls, call{name, a})
	key := name + " " + a
	for p, o := range r.out {
		if strings.HasPrefix(key, p) {
			return []byte(o), nil
		}
	}
	for p := range r.fail {
		if strings.HasPrefix(key, p) {
			return []byte("failed"), errors.New("exit 1")
		}
	}
	return nil, nil
}

func (r *rec) lines() []string {
	var out []string
	for _, c := range r.calls {
		out = append(out, c.name+" "+c.args)
	}
	return out
}

func envFor(goos, home string, r Runner) Env {
	return Env{
		GOOS: goos, Home: home, UID: 1000, User: "alice",
		SyPath: "/home/alice/.local/bin/wy", StateDir: filepath.Join(home, "state"),
		Getenv: func(string) string { return "" }, Runner: r,
	}
}

var spec = model.ServiceSpec{Name: "watchyard-hub", Listen: "127.0.0.1:8787"}

func TestGolden(t *testing.T) {
	sp, _ := normalize(spec)
	e := envFor("linux", "/home/alice", nil)
	golden(t, "watchyard-hub.service.golden", SystemdUnit(e, sp))
	golden(t, "com.watchyard.hub.plist.golden", LaunchdPlist(envFor("darwin", "/Users/alice", nil), sp))
	w := envFor("windows", `C:\Users\alice`, nil)
	w.SyPath = `C:\Users\alice\.local\bin\wy.exe`
	w.StateDir = `C:\Users\alice\AppData\Local\watchyard`
	golden(t, "watchyard-hub.task.xml.golden", WindowsTaskXML(w, sp))
	e.Getenv = func(k string) string {
		if k == "WY_STATE_DIR" {
			return "/data/wy state"
		}
		return ""
	}
	golden(t, "watchyard-hub.service.overrides.golden", SystemdUnit(e, sp))
}

// The generated unit must be the one in deploy/systemd except for ExecStart (absolute path)
// and comments.
func TestMatchesDeployUnit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "systemd", "watchyard-hub.service"))
	if err != nil {
		t.Fatal(err)
	}
	norm := func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if strings.HasPrefix(l, "#") || strings.HasPrefix(l, "ExecStart=") {
				continue
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n")
	}
	sp, _ := normalize(spec)
	got := SystemdUnit(envFor("linux", "/home/alice", nil), sp)
	if norm(string(b)) != norm(got) {
		t.Fatalf("unit drifted from deploy/systemd:\n%s\n--- vs ---\n%s", norm(got), norm(string(b)))
	}
	if !strings.Contains(string(b), "ExecStart=%h/.local/bin/wy hub serve --listen 127.0.0.1:8787") ||
		!strings.Contains(got, "ExecStart=/home/alice/.local/bin/wy hub serve --listen 127.0.0.1:8787") {
		t.Fatal("ExecStart")
	}
}

func TestSystemdInstallUninstall(t *testing.T) {
	home := t.TempDir()
	r := &rec{out: map[string]string{"loginctl": "Linger=no\n", "systemctl --user is-active": "active\n"}}
	var out bytes.Buffer
	e := envFor("linux", home, r)
	e.Out = &out
	s := New(e)
	ctx := context.Background()
	if err := s.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "watchyard-hub.service")
	b, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(b), "ExecStart=/home/alice/.local/bin/wy hub serve --listen 127.0.0.1:8787") {
		t.Fatalf("%v %s", err, b)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now watchyard-hub.service",
		"loginctl show-user alice --property=Linger",
	}
	if got := r.lines(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v", got)
	}
	if !strings.Contains(out.String(), "enable-linger alice") {
		t.Fatalf("linger hint missing: %q", out.String())
	}
	st, err := s.Status(ctx, "watchyard-hub")
	if err != nil || !st.Installed || !st.Active || !st.LingerOff {
		t.Fatalf("%+v %v", st, err)
	}
	r.calls = nil
	if err := s.Uninstall(ctx, "watchyard-hub"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); err == nil {
		t.Fatal("unit not removed")
	}
	want = []string{"systemctl --user disable --now watchyard-hub.service", "systemctl --user daemon-reload"}
	if got := r.lines(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v", got)
	}
	if err := s.Uninstall(ctx, "watchyard-hub"); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	st, _ = s.Status(ctx, "watchyard-hub")
	if st.Installed || st.Active {
		t.Fatalf("%+v", st)
	}
}

func TestSystemdFailureKeepsFile(t *testing.T) {
	home := t.TempDir()
	r := &rec{fail: map[string]bool{"systemctl --user enable": true}}
	err := New(envFor("linux", home, r)).Install(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "enable --now watchyard-hub.service") {
		t.Fatalf("got %v", err)
	}
	if _, e := os.Stat(filepath.Join(home, ".config", "systemd", "user", "watchyard-hub.service")); e != nil {
		t.Fatal("file must stay")
	}
}

func TestXDGConfigHome(t *testing.T) {
	home, xdg := t.TempDir(), t.TempDir()
	r := &rec{}
	e := envFor("linux", home, r)
	e.Getenv = func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return xdg
		}
		return ""
	}
	if err := New(e).Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(xdg, "systemd", "user", "watchyard-hub.service")); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchdInstallUninstall(t *testing.T) {
	home := t.TempDir()
	r := &rec{out: map[string]string{"launchctl print": "state = running\n"}}
	s := New(envFor("darwin", home, r))
	ctx := context.Background()
	if err := s.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "Library", "LaunchAgents", "com.watchyard.hub.plist")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	want := []string{"launchctl bootout gui/1000/com.watchyard.hub", "launchctl bootstrap gui/1000 " + p}
	if got := r.lines(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v", got)
	}
	if st, _ := s.Status(ctx, "watchyard-hub"); !st.Installed || !st.Active || st.LingerOff {
		t.Fatalf("%+v", st)
	}
	r.calls = nil
	if err := s.Uninstall(ctx, "watchyard-hub"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("plist not removed")
	}
	if got := r.lines(); len(got) != 1 || got[0] != "launchctl bootout gui/1000/com.watchyard.hub" {
		t.Fatalf("calls %v", got)
	}
}

func TestWindowsInstallUninstall(t *testing.T) {
	home := t.TempDir()
	r := &rec{out: map[string]string{"schtasks /Query": "Watchyard Hub   Running\n"}}
	e := envFor("windows", home, r)
	s := New(e)
	ctx := context.Background()
	if err := s.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}
	xml := filepath.Join(e.StateDir, "watchyard-hub.task.xml")
	b, err := os.ReadFile(xml)
	if err != nil || len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("task XML must be UTF-16LE with BOM: %v", err)
	}
	want := []string{`schtasks /Create /TN Watchyard Hub /XML ` + xml + ` /F`, `schtasks /Run /TN Watchyard Hub`}
	if got := r.lines(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v", got)
	}
	if st, _ := s.Status(ctx, "watchyard-hub"); !st.Installed || !st.Active {
		t.Fatalf("%+v", st)
	}
	r.calls = nil
	if err := s.Uninstall(ctx, "watchyard-hub"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(xml); err == nil {
		t.Fatal("xml not removed")
	}
	if len(r.calls) != 2 {
		t.Fatalf("calls %v", r.lines())
	}
}

func TestValidation(t *testing.T) {
	s := New(envFor("linux", t.TempDir(), &rec{}))
	if err := s.Install(context.Background(), model.ServiceSpec{Name: "../evil"}); err == nil {
		t.Fatal("bad name accepted")
	}
	e := envFor("linux", t.TempDir(), &rec{})
	e.SyPath = "wy"
	if err := New(e).Install(context.Background(), spec); err == nil {
		t.Fatal("relative wy accepted")
	}
	if err := New(envFor("plan9", t.TempDir(), &rec{})).Install(context.Background(), spec); err == nil {
		t.Fatal("unsupported OS accepted")
	}
	// defaults
	sp, err := normalize(model.ServiceSpec{})
	if err != nil || sp.Name != DefaultName || strings.Join(sp.Args, " ") != "hub serve --listen 127.0.0.1:8787" {
		t.Fatalf("%+v %v", sp, err)
	}
}

// Regression: a value with a newline (here an WY_* override from the environment) must not
// end the Environment= line and inject a directive into the unit.
func TestSystemdQuoteControlChars(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/wy":                "/usr/bin/wy",
		"/opt/my tools/wy":           `"/opt/my tools/wy"`,
		"/x\nExecStartPre=/bin/evil": `"/x\x0aExecStartPre=/bin/evil"`,
		"a\rb\tc\x00d\x7f":           `"a\x0db\x09c\x00d\x7f"`,
		`a\b"%$`:                     `"a\\b\"%%$$"`,
	} {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %q, want %q", in, got, want)
		}
	}
	// Environment= does no "$" expansion: a "$" there stays single (systemd.exec(5)).
	if got := systemdEnvQuote(`WY_STATE_DIR=/d/$x %y`); got != `"WY_STATE_DIR=/d/$x %%y"` {
		t.Errorf("systemdEnvQuote = %q", got)
	}
	sp, _ := normalize(spec)
	e := envFor("linux", "/home/alice", nil)
	e.Getenv = func(k string) string {
		if k == "WY_STATE_DIR" {
			return "/data\nExecStartPre=/bin/evil"
		}
		return ""
	}
	e.SyPath = "/home/alice/bin/wy\r\nUser=root"
	for _, l := range strings.Split(SystemdUnit(e, sp), "\n") {
		if strings.HasPrefix(l, "ExecStartPre") || strings.HasPrefix(l, "User=") || strings.Contains(l, "\r") {
			t.Errorf("injected line %q", l)
		}
	}
}
