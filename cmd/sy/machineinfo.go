package main

// machine_info (record.md 4.8, machines.md 6.3) from the command layer: `sy tick` (on change
// and every 6 hours), `sy connect`, `sy update` (post-update step) and `sy machine rename`.
// internal/machineinfo builds the record; this file only supplies the folders, the config
// and the timer facts (by unit files, as `sy doctor` sees them).

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/machineinfo"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/timer"
)

// machineInfoOptions is machineinfo.Options for this process.
func machineInfoOptions(d paths.Dirs, home string, cfg config.Config) machineinfo.Options {
	return machineinfo.Options{StateDir: d.State.Path, Config: cfg, Timer: func() machineinfo.Timer {
		return timerFacts(runtime.GOOS, home, d.State.Path)
	}}
}

// timerFacts reports whether the flush timer's unit file is there and, on Linux, whether
// systemd keeps the user's units running after logout (linger).
func timerFacts(goos, home, stateDir string) machineinfo.Timer {
	te := timer.Env{GOOS: goos, Home: home, StateDir: stateDir, Getenv: os.Getenv}
	var t machineinfo.Timer
	switch goos {
	case "linux":
		t.Active = exists(filepath.Join(timer.SystemdDir(te), timer.SystemdName+".timer"))
		if u, err := user.Current(); err == nil && u.Username != "" {
			l := exists(filepath.Join("/var/lib/systemd/linger", u.Username))
			t.Linger = &l
		}
	case "darwin":
		t.Active = exists(timer.LaunchAgentPath(te))
	case "windows":
		t.Active = exists(filepath.Join(stateDir, timer.WindowsXMLFile))
	}
	return t
}

// sendMachineInfo queues a machine_info now (force) or when due, reading config.toml itself.
// Errors are returned for callers that show them; most ignore them (it is a report).
func sendMachineInfo(d paths.Dirs, home string, force bool) (bool, error) {
	cfg := config.Defaults()
	if f, err := config.Load(d.Config.Path); err == nil {
		if c, err := f.Typed(); err == nil {
			cfg = c
		}
	}
	return machineinfo.Send(machineInfoOptions(d, home, cfg), force)
}
