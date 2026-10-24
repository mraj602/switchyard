package timer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/brand"
)

// rename.md 2.3: Install and Uninstall take away the timer of the name before a rename, so
// the two never both run `sy tick`.
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
