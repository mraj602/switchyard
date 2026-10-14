package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/record"
	"github.com/mraj602/switchyard/internal/scan"
	"github.com/mraj602/switchyard/internal/version"
)

// slotEnv is what the scan, agents and status commands share: folders, config, machine,
// slots and the outbox of this machine.
type slotEnv struct {
	home    string
	dirs    paths.Dirs
	cfg     config.Config
	machine identity.Machine
	slots   []identity.Slot
	all     []identity.Slot // every discovered slot; slots is narrowed by --slot
	store   *identity.Store
	outbox  *outbox.Outbox
	client  string
}

func loadSlotEnv(slotFlag string) (*slotEnv, error) {
	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return nil, err
	}
	if pe.Home == "" {
		return nil, errors.New("cannot find the home folder")
	}
	f, err := config.Load(d.Config.Path)
	if err != nil {
		return nil, err
	}
	cfg, err := f.Typed()
	if err != nil {
		return nil, err
	}
	loc := identity.Locator{Home: pe.Home, FS: identity.OSFS{}}
	slots, err := loc.Discover(cfg.Slots.Extra, os.Getenv)
	if err != nil {
		return nil, err
	}
	all := slots
	if slotFlag != "" {
		if slots, err = selectSlot(slots, slotFlag); err != nil {
			return nil, err
		}
	}
	m, err := identity.LoadOrCreateMachine(d.State.Path, identity.MachineOptions{})
	if err != nil {
		return nil, err
	}
	ob := outbox.New(filepath.Join(d.State.Path, "outbox"))
	ob.NoFsync = !cfg.Outbox.Fsync
	return &slotEnv{home: pe.Home, dirs: d, cfg: cfg, machine: m, slots: slots, all: all,
		store: &identity.Store{StateDir: d.State.Path, FS: identity.OSFS{}}, outbox: ob,
		client: "sy/" + version.Version}, nil
}

// selectSlot picks the slot named by --slot: "~/.claude-2", its absolute folder, its folder
// name (".claude-2" or "claude-2").
func selectSlot(slots []identity.Slot, want string) ([]identity.Slot, error) {
	w := strings.TrimSuffix(filepath.ToSlash(want), "/")
	for _, s := range slots {
		base := filepath.Base(s.Dir)
		if w == s.Name || w == filepath.ToSlash(s.Dir) || w == base || w == strings.TrimPrefix(base, ".") {
			return []identity.Slot{s}, nil
		}
	}
	names := make([]string, len(slots))
	for i, s := range slots {
		names[i] = s.Name
	}
	return nil, fmt.Errorf("no such folder %q (found: %s)", want, strings.Join(names, ", "))
}

// seatOf is the login of a slot as a record seat, nil when logged out or unreadable.
func (e *slotEnv) seatOf(s identity.Slot) *record.Seat {
	l, err := e.store.Login(s)
	if err != nil || !l.HasSeat {
		return nil
	}
	return &record.Seat{Account: l.Seat.Account, Org: l.Seat.Org, Email: l.Seat.Email, Src: l.Seat.Src}
}

// runScan is `sy scan [--history] [--force] [--slot X]`.
func runScan(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	history := fs.Bool("history", false, "also send each folder's stats-cache.json once (history_import)")
	force := fs.Bool("force", false, "with --history: send it again even if it was sent before")
	slot := fs.String("slot", "", "scan only this folder (~/.claude-2, .claude-2)")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "sy scan: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	if *force && !*history {
		fmt.Fprintln(stderr, "sy scan: --force only means something with --history")
		return errUsage
	}
	e, err := loadSlotEnv(*slot)
	if err != nil {
		return err
	}
	sum, err := scan.Run(scan.RunOptions{
		Home: e.home, StateDir: e.dirs.State.Path, Slots: scan.FromIdentity(e.slots), AllSlots: scan.FromIdentity(e.all),
		Outbox: e.outbox, Machine: e.machine.ID, Client: e.client,
		SeatOf: func(folder string) *record.Seat {
			for _, s := range e.slots {
				if s.Name == folder {
					return e.seatOf(s)
				}
			}
			return nil
		},
		History: *history, Force: *force,
	})
	if errors.Is(err, scan.ErrLocked) {
		fmt.Fprintln(stdout, "another sy scan is already running; nothing to do")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "scanned %d transcript files (%s): %d token rows, %d records queued in the outbox\n",
		sum.Files, humanBytes(sum.Bytes), sum.Rows, sum.Records)
	if *history {
		fmt.Fprintf(stdout, "history: sent for %s; already sent for %s\n", orNone(sum.HistorySent), orNone(sum.HistorySkipped))
	}
	return nil
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "no folder"
	}
	return strings.Join(s, ", ")
}

// runAgents is `sy agents [--slot X]`.
func runAgents(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agents", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slot := fs.String("slot", "", "only this folder (~/.claude-2, .claude-2)")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "sy agents: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	e, err := loadSlotEnv(*slot)
	if err != nil {
		return err
	}
	lists, err := scan.Agents(context.Background(), scan.AgentsOptions{
		Slots: e.slots, Environ: os.Environ(), Outbox: e.outbox, Machine: e.machine.ID, Client: e.client,
		SeatOf: e.seatOf,
	})
	for _, l := range lists {
		line := fmt.Sprintf("%-14s %d session(s), source %s, complete=%v", l.Slot, l.Sessions, l.Source, l.Complete)
		if l.Error != "" {
			line += " (" + l.Error + ")"
		}
		fmt.Fprintln(stdout, line)
	}
	return err
}
