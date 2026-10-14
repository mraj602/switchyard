// Package clientenv gathers what every short-lived client command (`sy hook`,
// `sy statusline`) needs before it writes a record: the folders, the config, the machine,
// the current slot and its cached login. It never runs `claude`.
//
// Phase 1. Depends on: paths, config, identity, record, version.
package clientenv

import (
	"os"
	"path/filepath"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/record"
	"github.com/mraj602/switchyard/internal/version"
)

// Env is the resolved context of one client invocation.
type Env struct {
	Dirs    paths.Dirs
	Config  config.Config
	Machine identity.Machine
	Slot    identity.Slot
	Login   identity.Login
	Store   *identity.Store
	Getenv  func(string) string
	// ConfigErr is why config.toml could not be fully read (nil when it could). The caller
	// logs it; the Config is then the most private reading of what was there.
	ConfigErr error
}

// Load resolves everything. A broken config.toml is not fatal: keys that decode are used,
// the rest take their defaults except privacy keys, which take their most private value;
// a file that cannot be read at all gives config.MostPrivate. The cause is in ConfigErr. A
// machine id that cannot be read or created is, because a record without one is useless.
func Load(getenv func(string) string) (*Env, error) {
	pe := paths.FromOS()
	pe.Getenv = getenv
	dirs, err := paths.Resolve(pe)
	if err != nil {
		return nil, err
	}
	e := &Env{Dirs: dirs, Getenv: getenv, Config: config.Defaults()}
	if f, err := config.Load(dirs.Config.Path); err != nil {
		e.Config, e.ConfigErr = config.MostPrivate(), err
	} else {
		e.Config, e.ConfigErr = f.Typed()
	}
	e.Machine, err = identity.LoadOrCreateMachine(dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return nil, err
	}
	home := pe.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	e.Slot = identity.Locator{Home: home, FS: identity.OSFS{}}.Current(getenv)
	e.Store = &identity.Store{StateDir: dirs.State.Path, FS: identity.OSFS{}}
	e.Login, err = e.Store.Login(e.Slot)
	if err != nil {
		e.Login = identity.Login{} // an unreadable state file leaves the seat out
	}
	return e, nil
}

// OutboxDir is <state>/outbox.
func (e *Env) OutboxDir() string { return filepath.Join(e.Dirs.State.Path, "outbox") }

// Meta is the envelope data common to the records of this slot.
func (e *Env) Meta(term *record.Term) record.Meta {
	m := record.Meta{Machine: e.Machine.ID, Slot: e.Slot.Name, Term: term, Client: "sy/" + version.Version}
	if e.Login.HasSeat {
		s := e.Login.Seat
		m.Seat = &record.Seat{Account: s.Account, Org: s.Org, Email: s.Email, Src: s.Src}
	}
	return m
}

// StateDir resolves only the state folder, for the error log of a command that failed
// before Load could finish.
func StateDir(getenv func(string) string) (string, error) {
	pe := paths.FromOS()
	pe.Getenv = getenv
	d, err := paths.Resolve(pe)
	return d.State.Path, err
}
