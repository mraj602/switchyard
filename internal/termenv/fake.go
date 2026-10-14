package termenv

import (
	"path"
	"path/filepath"
	"runtime"
)

// goosNow is the OS this binary was built for.
const goosNow = runtime.GOOS

// Fake returns an Env for tests: home is a temp folder (t.TempDir()), goos the OS layout to
// pretend ("linux", "darwin" or "windows"), and vars the only environment variables that
// exist. XDG_DATA_HOME, APPDATA and LOCALAPPDATA are taken from vars when present; on a
// Windows layout APPDATA and LOCALAPPDATA default to <home>\AppData\Roaming and \Local.
//
// Paths are joined with the separator of the pretended OS, so a Windows layout can be
// tested on Linux (filepath is used when goos is the real OS, so real files work there).
func Fake(home, goos string, vars map[string]string) Env {
	env := map[string]string{}
	for k, v := range vars {
		env[k] = v
	}
	e := Env{
		Home:         home,
		XDGDataHome:  env["XDG_DATA_HOME"],
		AppData:      env["APPDATA"],
		LocalAppData: env["LOCALAPPDATA"],
		GOOS:         goos,
		Getenv:       func(k string) string { return env[k] },
	}
	if goos == "windows" {
		if e.AppData == "" {
			e.AppData = e.Join(home, "AppData", "Roaming")
		}
		if e.LocalAppData == "" {
			e.LocalAppData = e.Join(home, "AppData", "Local")
		}
	}
	return e
}

// Join joins path elements with the separator of e.GOOS. On the OS this binary runs on it
// is filepath.Join; for a pretended Windows layout on Unix it joins with `\`, and for a
// pretended Unix layout on Windows with `/`.
func (e Env) Join(elem ...string) string {
	if e.GOOS == "" || e.GOOS == goosNow {
		return filepath.Join(elem...)
	}
	if e.GOOS == "windows" {
		out := ""
		for i, s := range elem {
			if s == "" {
				continue
			}
			if i > 0 && out != "" && out[len(out)-1] != '\\' {
				out += `\`
			}
			out += s
		}
		return out
	}
	return path.Join(elem...)
}

// DataHome is the XDG data folder: $XDG_DATA_HOME when it is set and absolute, else
// <home>/.local/share (the XDG Base Directory rule). It is meaningful on Linux and BSD.
func (e Env) DataHome() string {
	if e.XDGDataHome != "" && (filepath.IsAbs(e.XDGDataHome) || path.IsAbs(e.XDGDataHome)) {
		return e.XDGDataHome
	}
	return e.Join(e.Home, ".local", "share")
}
