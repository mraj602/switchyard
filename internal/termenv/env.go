// Package termenv is the injected environment of the phase 7 terminal packages
// (internal/board, internal/termctl, internal/warp): the home folder, the per-OS data
// folders, environment lookups and the OS name. Those packages never read the process
// environment or call os.UserHomeDir themselves; they take an Env, so every test can run
// against a temp home laid out like Linux, macOS or Windows (docs/spec/terminal.md T12,
// docs/phases/7.md hard rule 5). lint_test.go enforces it.
//
// This file is the only place in those packages that may read the real process
// environment.
//
// Phase 7 wave 0. Depends on: nothing.
package termenv

import (
	"os"
	"runtime"
)

// Env is what a terminal package may know about the machine it runs on. The zero value is
// not useful; build one with FromOS (the real process) or Fake (tests).
type Env struct {
	// Home is the person's home folder (an absolute path).
	Home string
	// XDGDataHome is $XDG_DATA_HOME as set ("" when unset); see DataHome.
	XDGDataHome string
	// AppData and LocalAppData are %APPDATA% and %LOCALAPPDATA% on Windows ("" elsewhere,
	// or when unset).
	AppData      string
	LocalAppData string
	// Getenv looks up any other variable (TERM_PROGRAM, WARP_FOCUS_URL, NO_COLOR, ...).
	// nil behaves as an empty environment.
	Getenv func(string) string
	// GOOS is runtime.GOOS of the process, or the OS a test pretends to be.
	GOOS string
}

// FromOS returns the Env of this process. A missing home folder leaves Home empty; callers
// treat that as "no per-person folders" and write nothing.
func FromOS() Env {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return Env{
		Home:         home,
		XDGDataHome:  os.Getenv("XDG_DATA_HOME"),
		AppData:      os.Getenv("APPDATA"),
		LocalAppData: os.Getenv("LOCALAPPDATA"),
		Getenv:       os.Getenv,
		GOOS:         runtime.GOOS,
	}
}

// Lookup returns the value of the environment variable k, "" when unset or when Getenv is
// nil.
func (e Env) Lookup(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}
