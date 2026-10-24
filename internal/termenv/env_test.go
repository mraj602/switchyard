package termenv

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestFakeLayouts(t *testing.T) {
	home := t.TempDir()

	lin := Fake(home, "linux", nil)
	if lin.DataHome() != filepath.Join(home, ".local", "share") && runtime.GOOS != "windows" {
		t.Errorf("linux DataHome = %q", lin.DataHome())
	}
	if lin.AppData != "" || lin.Lookup("HOME") != "" {
		t.Errorf("linux layout leaked a variable: %+v", lin)
	}
	xdg := Fake(home, "linux", map[string]string{"XDG_DATA_HOME": "/srv/data", "TERM_PROGRAM": "WarpTerminal"})
	if xdg.DataHome() != "/srv/data" || xdg.Lookup("TERM_PROGRAM") != "WarpTerminal" {
		t.Errorf("XDG_DATA_HOME or Lookup ignored: %q %q", xdg.DataHome(), xdg.Lookup("TERM_PROGRAM"))
	}
	if rel := Fake(home, "linux", map[string]string{"XDG_DATA_HOME": "rel/dir"}); rel.DataHome() == "rel/dir" {
		t.Error("a relative XDG_DATA_HOME must be ignored (XDG Base Directory rule)")
	}

	win := Fake(`C:\Users\u`, "windows", nil)
	if win.AppData != `C:\Users\u\AppData\Roaming` || win.LocalAppData != `C:\Users\u\AppData\Local` {
		if runtime.GOOS != "windows" {
			t.Errorf("windows layout: %q %q", win.AppData, win.LocalAppData)
		}
	}
	if got := win.Join(win.AppData, "warp", "Warp", "data", "tab_configs"); runtime.GOOS != "windows" &&
		got != `C:\Users\u\AppData\Roaming\warp\Warp\data\tab_configs` {
		t.Errorf("windows Join = %q", got)
	}
	if got := Fake("/Users/u", "darwin", nil).Join("/Users/u", ".warp", "tab_configs"); runtime.GOOS != "windows" &&
		got != "/Users/u/.warp/tab_configs" {
		t.Errorf("darwin Join = %q", got)
	}

	var zero Env
	if zero.Lookup("PATH") != "" {
		t.Error("a nil Getenv must read as an empty environment")
	}
}

func TestFromOSHasTheProcessOS(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/wy-termenv-test")
	e := FromOS()
	if e.GOOS != runtime.GOOS || e.Getenv == nil || e.XDGDataHome != "/tmp/wy-termenv-test" {
		t.Fatalf("%+v", e)
	}
}
