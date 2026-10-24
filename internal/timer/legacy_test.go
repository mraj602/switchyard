package timer

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/brand"
)

// rename.md 2.3: Install and Uninstall take away the timer of the name before a rename, so
// the two never both run `wy tick`.
func TestInstallReplacesLegacyTimer(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			e, r, out := fakeHome(t, goos)
			var files []string
			switch goos {
			case "linux":
				d := SystemdDir(e)
				files = []string{filepath.Join(d, "oldname-flush.timer"), filepath.Join(d, "oldname-flush.service")}
			case "darwin":
				files = []string{filepath.Join(e.Home, "Library", "LaunchAgents", "com.oldname.flush.plist")}
			case "windows":
				files = []string{filepath.Join(e.StateDir, "oldname-flush.task.xml")}
			}
			for _, f := range files {
				if err := writeFile(f, "old"); err != nil {
					t.Fatal(err)
				}
			}
			if want := files[len(files)-1]; LegacyUnit(e) != want {
				t.Fatalf("LegacyUnit = %q", LegacyUnit(e))
			}
			res, err := Install(e)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(res.Removed, ",") != strings.Join(files, ",") {
				t.Errorf("removed %v, want %v", res.Removed, files)
			}
			for _, f := range files {
				if _, err := os.Stat(f); !os.IsNotExist(err) {
					t.Errorf("%s left behind", f)
				}
			}
			calls := strings.Join(r.calls, "\n")
			switch goos {
			case "linux":
				if !strings.HasPrefix(calls, "systemctl --user disable --now oldname-flush.timer\nsystemctl --user daemon-reload\n") {
					t.Errorf("calls:\n%s", calls)
				}
			case "darwin":
				if !strings.HasPrefix(calls, "launchctl bootout gui/1000/com.oldname.flush\n") {
					t.Errorf("calls:\n%s", calls)
				}
			case "windows":
				if !strings.Contains(out.String(), `schtasks /Delete /TN "Oldname Flush" /F`) {
					t.Errorf("output:\n%s", out)
				}
			}
			if LegacyUnit(e) != "" {
				t.Error("LegacyUnit after the move")
			}
		})
	}
}

func TestNoLegacyWithoutRename(t *testing.T) {
	defer brand.SetLegacyForTest(brand.Slug, brand.Name)()
	e, r, _ := fakeHome(t, "linux")
	if err := writeFile(filepath.Join(SystemdDir(e), SystemdName+".timer"), "x"); err != nil {
		t.Fatal(err)
	}
	if LegacyUnit(e) != "" {
		t.Fatal("the current timer is not a legacy one")
	}
	if _, err := Uninstall(e); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.Join(r.calls, "\n"), "systemctl --user disable --now "+SystemdName+".timer") {
		t.Errorf("calls: %v", r.calls)
	}
}

// stateRunner answers is-enabled / is-active by their last argument's suffix.
type stateRunner struct {
	calls           []string
	enabled, active string
}

func (r *stateRunner) Run(name string, args ...string) ([]byte, error) {
	c := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, c)
	switch {
	case strings.Contains(c, " is-enabled "):
		return []byte(r.enabled), errors.New("exit 1")
	case strings.Contains(c, " is-active "):
		return []byte(r.active), errors.New("exit 3")
	}
	return nil, nil
}

// The post-update move (KeepLegacyState) leaves the new timer as the old one was.
func TestInstallKeepsLegacyTimerState(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	for _, c := range []struct {
		enabled, active string
		want            string // the call after the last daemon-reload, "" for none
	}{
		{"enabled\n", "active\n", "systemctl --user enable --now " + SystemdName + ".timer"},
		{"enabled\n", "inactive\n", "systemctl --user enable " + SystemdName + ".timer"},
		{"disabled\n", "inactive\n", ""},
	} {
		t.Run(c.enabled+c.active, func(t *testing.T) {
			e, _, _ := fakeHome(t, "linux")
			r := &stateRunner{enabled: c.enabled, active: c.active}
			e.Runner, e.Out, e.User = r, &bytes.Buffer{}, ""
			e.KeepLegacyState = true
			d := SystemdDir(e)
			for _, f := range []string{"oldname-flush.timer", "oldname-flush.service"} {
				if err := writeFile(filepath.Join(d, f), "old"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Install(e); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(d, SystemdName+".timer")); err != nil {
				t.Fatalf("new timer missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(d, "oldname-flush.timer")); !os.IsNotExist(err) {
				t.Error("old timer left behind")
			}
			last := r.calls[len(r.calls)-1]
			if c.want == "" {
				if last != "systemctl --user daemon-reload" {
					t.Errorf("calls:\n%s", strings.Join(r.calls, "\n"))
				}
			} else if last != c.want {
				t.Errorf("last call %q, want %q", last, c.want)
			}
		})
	}
}
