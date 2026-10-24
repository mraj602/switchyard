// Package paths resolves the config, state and data folders for each OS, including the
// WY_CONFIG_DIR / WY_STATE_DIR / WY_DATA_DIR / WY_HOME overrides. No other package
// builds paths from $HOME.
//
// Contract: docs/spec/paths.md.
//
// Depends on: brand.
package paths

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
)

// app is the folder name used under each XDG base folder; appWin the one under
// %LOCALAPPDATA%.
const (
	app    = brand.Slug
	appWin = brand.Name
)

// Source says where a folder came from.
type Source string

const (
	SourceDirEnv  Source = "env"     // WY_CONFIG_DIR / WY_STATE_DIR / WY_DATA_DIR (Dir.Var names it)
	SourceWYHome  Source = "WY_HOME" // $WY_HOME/<role>
	SourceXDG     Source = "xdg"     // $XDG_*_HOME/watchyard (Dir.Var names it)
	SourceDefault Source = "default" // the per-OS default
)

// Dir is one resolved folder.
type Dir struct {
	Path   string
	Source Source
	Var    string // the environment variable behind Source, when there is one
}

// Describe renders the source for humans, e.g. "WY_STATE_DIR", "XDG_STATE_HOME" or "default".
func (d Dir) Describe() string {
	switch d.Source {
	case SourceDirEnv, SourceXDG, SourceWYHome:
		return d.Var
	}
	if d.Var != "" {
		return d.Var // Windows: LOCALAPPDATA
	}
	return "default"
}

// Dirs is the result of Resolve.
type Dirs struct {
	Config, State, Data Dir
}

// Env is everything Resolve looks at. All fields are injectable so tests are pure.
type Env struct {
	GOOS   string
	Home   string              // user home folder; may be empty if every folder is overridden
	Getenv func(string) string // environment lookup; nil means an empty environment
}

// FromOS returns the Env of the running process. Home is left empty if it cannot be
// found; Resolve only fails on that when it actually needs the home folder.
func FromOS() Env {
	home, _ := os.UserHomeDir()
	return Env{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv}
}

// Resolve computes the three folders. Precedence per folder: WY_<ROLE>_DIR, then WY_HOME,
// then the per-OS table of paths.md section 1.
func Resolve(e Env) (Dirs, error) {
	get := e.Getenv
	if get == nil {
		get = func(string) string { return "" }
	}
	win := e.GOOS == "windows"
	var d Dirs
	roles := []struct {
		name, dirVar, xdgVar, xdgDefault string
		out                              *Dir
	}{
		{"config", "WY_CONFIG_DIR", "XDG_CONFIG_HOME", ".config", &d.Config},
		{"state", "WY_STATE_DIR", "XDG_STATE_HOME", ".local/state", &d.State},
		{"data", "WY_DATA_DIR", "XDG_DATA_HOME", ".local/share", &d.Data},
	}
	for _, r := range roles {
		dir, err := resolveOne(e, get, win, r.name, r.dirVar, r.xdgVar, r.xdgDefault)
		if err != nil {
			return Dirs{}, fmt.Errorf("%s folder: %w", r.name, err)
		}
		*r.out = dir
	}
	return d, nil
}

func resolveOne(e Env, get func(string) string, win bool, role, dirVar, xdgVar, xdgDefault string) (Dir, error) {
	if v := get(dirVar); v != "" {
		if !isAbs(win, v) {
			return Dir{}, fmt.Errorf("%s must be an absolute path, got %q", dirVar, v)
		}
		return Dir{Path: clean(win, v), Source: SourceDirEnv, Var: dirVar}, nil
	}
	if v := get("WY_HOME"); v != "" {
		if !isAbs(win, v) {
			return Dir{}, fmt.Errorf("WY_HOME must be an absolute path, got %q", v)
		}
		return Dir{Path: join(win, v, role), Source: SourceWYHome, Var: "WY_HOME"}, nil
	}
	if win {
		if base := get("LOCALAPPDATA"); base != "" && isAbs(true, base) {
			return Dir{Path: join(true, base, appWin, role), Source: SourceDefault, Var: "LOCALAPPDATA"}, nil
		}
		if e.Home == "" {
			return Dir{}, errors.New("LOCALAPPDATA is not set and the home folder is unknown")
		}
		return Dir{Path: join(true, e.Home, "AppData", "Local", appWin, role), Source: SourceDefault}, nil
	}
	// Linux, WSL, BSD, macOS: XDG layout; XDG variables count only when absolute.
	if v := get(xdgVar); v != "" && isAbs(false, v) {
		return Dir{Path: join(false, v, app), Source: SourceXDG, Var: xdgVar}, nil
	}
	if e.Home == "" {
		return Dir{}, errors.New("home folder is unknown; set " + dirVar + " or WY_HOME")
	}
	return Dir{Path: join(false, e.Home, xdgDefault, app), Source: SourceDefault}, nil
}

// The helpers below pick the path flavour from GOOS (not from the build) so Windows
// rules can be tested on any machine.

func sep(win bool) string {
	if win {
		return `\`
	}
	return "/"
}

func isAbs(win bool, p string) bool {
	if !win {
		return strings.HasPrefix(p, "/")
	}
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") {
		return true
	}
	return len(p) >= 3 && isLetter(p[0]) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func clean(win bool, p string) string {
	if win {
		p = strings.ReplaceAll(p, "/", `\`)
	}
	return trimTrailing(win, p)
}

func trimTrailing(win bool, p string) string {
	s := sep(win)
	for len(p) > 1 && strings.HasSuffix(p, s) && !(win && len(p) == 3 && p[1] == ':') {
		p = p[:len(p)-1]
	}
	return p
}

// join appends elements (which may contain "/") to an absolute base.
func join(win bool, base string, elems ...string) string {
	s := sep(win)
	out := clean(win, base)
	for _, e := range elems {
		for _, part := range strings.Split(e, "/") {
			if part == "" {
				continue
			}
			if !strings.HasSuffix(out, s) {
				out += s
			}
			out += part
		}
	}
	return out
}
