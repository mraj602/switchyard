package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// installed puts the unit file where Status looks for it, as `sy setup --hub here` would.
func installed(t *testing.T, goos string) (*Service, *rec) {
	t.Helper()
	home := t.TempDir()
	r := &rec{}
	e := envFor(goos, home, r)
	if goos != "windows" { // Windows asks schtasks, which the recorder answers with success
		p := UnitPath(e, spec)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return New(e), r
}

// The start and stop commands per OS, with a recording runner: nothing real runs.
func TestStartStopCommands(t *testing.T) {
	cases := []struct {
		goos        string
		start, stop []string
	}{
		{"linux",
			[]string{"systemctl --user is-active switchyard-hub.service", "loginctl show-user alice --property=Linger", "systemctl --user start switchyard-hub.service"},
			[]string{"systemctl --user is-active switchyard-hub.service", "loginctl show-user alice --property=Linger", "systemctl --user stop switchyard-hub.service"}},
		{"darwin",
			[]string{"launchctl print gui/1000/com.switchyard.hub", "launchctl kickstart gui/1000/com.switchyard.hub"},
			[]string{"launchctl print gui/1000/com.switchyard.hub", "launchctl bootout gui/1000/com.switchyard.hub"}},
		{"windows",
			[]string{`schtasks /Query /TN Switchyard Hub`, `schtasks /Run /TN Switchyard Hub`},
			[]string{`schtasks /Query /TN Switchyard Hub`, `schtasks /End /TN Switchyard Hub`}},
	}
	for _, c := range cases {
		for _, start := range []bool{true, false} {
			svc, r := installed(t, c.goos)
			var err error
			want := c.stop
			if start {
				err, want = svc.Start(context.Background(), ""), c.start
			} else {
				err = svc.Stop(context.Background(), "")
			}
			if err != nil {
				t.Fatalf("%s start=%v: %v", c.goos, start, err)
			}
			if got := r.lines(); !reflect.DeepEqual(got, want) {
				t.Errorf("%s start=%v:\n got  %q\n want %q", c.goos, start, got, want)
			}
		}
	}
}

// A stopped LaunchAgent was booted out: Start loads it again, then kickstarts it.
func TestStartDarwinBootstrapsWhenNotLoaded(t *testing.T) {
	svc, r := installed(t, "darwin")
	r.fail = map[string]bool{"launchctl kickstart": true}
	// The second kickstart succeeds: fail only the first call.
	calls := 0
	svc.Env.Runner = runnerFunc(func(name string, args ...string) ([]byte, error) {
		out, err := r.Run(name, args...)
		if name == "launchctl" && len(args) > 0 && args[0] == "kickstart" {
			calls++
			if calls == 2 {
				return nil, nil
			}
		}
		return out, err
	})
	if err := svc.Start(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	plist := UnitPath(svc.Env, spec)
	want := []string{"launchctl print gui/1000/com.switchyard.hub", "launchctl kickstart gui/1000/com.switchyard.hub",
		"launchctl bootstrap gui/1000 " + plist, "launchctl kickstart gui/1000/com.switchyard.hub"}
	if got := r.lines(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

type runnerFunc func(name string, args ...string) ([]byte, error)

func (f runnerFunc) Run(name string, args ...string) ([]byte, error) { return f(name, args...) }

func TestStartStopNotInstalled(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		r := &rec{}
		svc := New(envFor(goos, t.TempDir(), r))
		for _, fn := range []func(context.Context, string) error{svc.Start, svc.Stop} {
			if err := fn(context.Background(), ""); !errors.Is(err, ErrNotInstalled) {
				t.Errorf("%s: err = %v, want ErrNotInstalled", goos, err)
			}
		}
		for _, c := range r.calls {
			if strings.Contains(c.args, "start") || strings.Contains(c.args, "stop") || strings.Contains(c.args, "kickstart") || strings.Contains(c.args, "bootout") {
				t.Errorf("%s: ran %s %s although nothing is installed", goos, c.name, c.args)
			}
		}
	}
}

func TestStartFailureIsReported(t *testing.T) {
	svc, r := installed(t, "linux")
	r.fail = map[string]bool{"systemctl --user start": true}
	err := svc.Start(context.Background(), "")
	if err == nil || errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnitName(t *testing.T) {
	for goos, want := range map[string]string{"linux": "switchyard-hub.service", "darwin": "com.switchyard.hub", "windows": "Switchyard Hub"} {
		if got := UnitName(goos, ""); got != want {
			t.Errorf("%s: %q, want %q", goos, got, want)
		}
	}
}
