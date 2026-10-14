package main

import (
	"os"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/machineadd"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/sshx"
	"github.com/mraj602/switchyard/internal/update"
)

// fleetDeps for `sy update --machines` (machines.md 7.4): the registry of `sy machine add`
// and the person's ssh (SY_SSH_COMMAND) in batch mode. The hub host is the registry machine
// whose SSH host is the host of machines.hub_admin.
func init() {
	fleetDeps = func() (update.Registry, update.Remote, error) {
		d, err := paths.Resolve(paths.FromOS())
		if err != nil {
			return nil, nil, err
		}
		cfg := config.Defaults()
		if f, err := config.Load(d.Config.Path); err == nil {
			if c, err := f.Typed(); err == nil {
				cfg = c
			}
		}
		r, err := sshx.FromEnv(os.Getenv)
		if err != nil {
			return nil, nil, err
		}
		r.Batch, r.AutoDefaults = true, true
		return machineadd.FleetRegistry{StateDir: d.State.Path, HubAdmin: cfg.Machines.HubAdmin}, machineadd.FleetRemote{SSH: r}, nil
	}
}
