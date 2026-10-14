package doctor

// The setup checks of docs/spec/setup.md 9.4 (the full doctor). They run only on a machine
// where `sy setup` has run (a ledger or a backup folder exists), so a phase 1 machine sees
// the same report as before.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/setup/apply"
	"github.com/mraj602/switchyard/internal/setup/backup"
	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/share"
)

// syPathRe finds the absolute sy path baked into a generated hook command:
// `[ -x '/home/u/.local/bin/sy' ] && ...`.
var syPathRe = regexp.MustCompile(`\[ -x '([^']+)' \]`)

func checkSetup(rep *Report, o Options, cfg config.Config, slots []identity.Slot) {
	st := o.Dirs.State.Path
	if st == "" {
		return
	}
	_, lerr := os.Stat(ledger.Path(st))
	_, berr := os.Stat(backup.Root(st))
	if lerr != nil && berr != nil {
		return // sy setup has never run here
	}

	// An interrupted run (7.6) is a failure until it is undone. A set in progress while
	// setup.lock is held belongs to a command still running.
	if ids, err := backup.InProgress(st); err == nil && len(ids) > 0 && !setupRunning(st) {
		rep.add(Check{Name: "setup", Level: Fail, Detail: "an earlier sy setup run was interrupted: " + strings.Join(ids, ", "),
			Fix: "sy setup --undo " + ids[0] + " (or sy setup --yes, which undoes it first)"})
	}
	led, err := ledger.Read(st)
	if err != nil {
		rep.add(Check{Name: "setup", Level: Warn, Detail: "cannot read the setup ledger: " + err.Error(),
			Fix: "sy setup --yes rewrites it"})
		return
	}

	// The plugin: marketplace folder present, sy at the baked path, one switchyard plugin.
	if led.Plugin != nil && !led.Plugin.Adopted && led.Plugin.Marketplace != "" {
		hooks := filepath.Join(led.Plugin.Marketplace, "plugins", "switchyard", "hooks", "hooks.json")
		b, err := os.ReadFile(hooks)
		switch {
		case err != nil:
			rep.add(Check{Name: "plugin", Level: Fail, Detail: "the sy-local marketplace folder is missing (" + led.Plugin.Marketplace + ")",
				Fix: "sy setup --yes writes it again"})
		default:
			if m := syPathRe.FindSubmatch(b); m != nil {
				p := string(m[1])
				if _, err := os.Stat(p); err != nil {
					rep.add(Check{Name: "plugin", Level: Fail, Detail: "hooks call " + p + ", which does not exist",
						Fix: "reinstall sy there, or run sy setup --yes from the new sy"})
				} else {
					rep.add(Check{Name: "plugin", Level: OK, Detail: "switchyard@sy-local hooks call " + p})
				}
				look := o.LookPath
				if look == nil {
					look = lookPath
				}
				if _, err := look("sy"); err != nil {
					rep.add(Check{Name: "sy on PATH", Level: Warn, Detail: "sy is not on PATH (hooks still work: they use " + p + ")",
						Fix: "add " + filepath.Dir(p) + " to PATH in your shell profile"})
				}
			}
		}
	}
	for _, s := range slots {
		set, err := readSettings(o.FS, filepath.Join(s.Dir, "settings.json"))
		if err != nil || !set.exists {
			continue
		}
		var on []string
		for k, en := range set.plugins {
			if en && (k == "switchyard" || strings.HasPrefix(k, "switchyard@")) {
				on = append(on, k)
			}
		}
		if len(on) > 1 {
			rep.add(Check{Name: "plugin", Slot: s.Name, Level: Warn,
				Detail: fmt.Sprintf("%d switchyard plugins are enabled (%s): every event is reported twice", len(on), strings.Join(on, ", ")),
				Fix:    "sy setup --yes disables all but switchyard@sy-local"})
		}
	}

	// Shared items still links; dangling links fail.
	for _, s := range slots {
		for _, it := range model.ShareItems {
			p := filepath.Join(s.Dir, it.Name)
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if _, err := os.Stat(p); err != nil {
				raw, _ := os.Readlink(p)
				rep.add(Check{Name: "sharing", Slot: s.Name, Level: Fail, Detail: it.Name + " links to " + raw + ", which does not exist",
					Fix: "restore the target, or sy setup --undo the run that made the link"})
			}
		}
	}

	// MCP conflicts and Windows copies in conflict.
	if cs, err := share.ReadConflicts(st); err == nil && len(cs) > 0 {
		names := map[string]bool{}
		var list []string
		for _, c := range cs {
			if !names[c.Name] {
				names[c.Name] = true
				list = append(list, c.Name)
			}
		}
		rep.add(Check{Name: "mcp", Level: Warn, Detail: fmt.Sprintf("%d MCP merge conflict(s), the primary's version won: %s", len(cs), strings.Join(list, ", ")),
			Fix: "check those servers in each folder; the log is " + filepath.Join(st, "share", "mcp-conflicts.jsonl")})
	}
	if g, err := share.ReadMCPGuard(st); err == nil && g != nil {
		rep.add(Check{Name: "mcp", Level: Warn, Detail: fmt.Sprintf("MCP sync paused since %s: it would have removed every server (%s) because %s no longer list them",
			g.Time, strings.Join(g.Removed, ", "), strings.Join(g.By, ", ")),
			Fix: "if you meant it, remove the servers in every folder by hand; otherwise add them back in " + strings.Join(g.By, ", ") + " (sy tick then resumes)"})
	}
	if rm, err := share.ReadMCPRemovals(st); err == nil && len(rm) > 0 {
		last := rm
		if len(last) > 5 {
			last = last[len(last)-5:]
		}
		var list []string
		for _, r := range last {
			list = append(list, r.Name+" (gone from "+r.By+", "+r.Time+")")
		}
		rep.add(Check{Name: "mcp", Level: OK, Detail: "MCP servers removed everywhere by the sync, latest: " + strings.Join(list, "; "),
			Fix: "the log is " + filepath.Join(st, "share", "mcp-removed.jsonl")})
	}
	if groups, err := share.LoadCopies(st); err == nil {
		for _, g := range groups {
			if g.Conflict {
				rep.add(Check{Name: "copies", Level: Warn, Detail: g.Name + ": two synced copies changed since the last sync; nothing copied",
					Fix: "make the copies equal by hand; sy tick resumes syncing"})
			}
		}
	}

	// Hub, token, timer, prototype hooks while a hub is live.
	if cfg.Hub.URL != "" {
		if _, err := os.Stat(filepath.Join(st, "token")); err != nil {
			rep.add(Check{Name: "hub", Level: Fail, Detail: "hub.url is " + cfg.Hub.URL + " but there is no device token",
				Fix: "sy connect " + cfg.Hub.URL + " --token-stdin"})
		}
		for _, s := range slots {
			set, err := readSettings(o.FS, filepath.Join(s.Dir, "settings.json"))
			if err != nil || !set.exists {
				continue
			}
			n := 0
			for _, cmds := range set.hooks {
				for _, c := range cmds {
					if strings.Contains(c, "switchyard-hook") {
						n++
					}
				}
			}
			if n > 0 {
				rep.add(Check{Name: "prototype", Slot: s.Name, Level: Warn,
					Detail: fmt.Sprintf("%d prototype hooks still post to the Electron app while a hub is connected", n),
					Fix:    "sy setup --prototype remove"})
				break
			}
		}
	}
	if led.Timer {
		if !timerPresent(o) {
			rep.add(Check{Name: "timer", Level: Warn, Detail: "the flush timer sy installed is gone",
				Fix: "sy connect <url> --token-stdin installs it again"})
		}
	}
	if lerr == nil {
		rep.add(Check{Name: "setup", Level: OK, Detail: "ledger " + ledger.Path(st)})
	}
}

// timerPresent looks for the timer's unit file (no service manager is asked).
// setupRunning is true while another sy command holds setup.lock.
func setupRunning(stateDir string) bool {
	unlock, err := apply.Lock(stateDir)
	if err != nil {
		return errors.Is(err, apply.ErrLocked)
	}
	unlock()
	return false
}

func timerPresent(o Options) bool {
	for _, p := range []string{
		filepath.Join(o.Home, ".config", "systemd", "user", "switchyard-flush.timer"),
		filepath.Join(o.Home, "Library", "LaunchAgents", "com.switchyard.flush.plist"),
		filepath.Join(o.Dirs.State.Path, "switchyard-flush.task.xml"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
