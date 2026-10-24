package doctor

// The setup checks of docs/spec/setup.md 9.4 (the full doctor). They run only on a machine
// where `wy setup` has run (a ledger or a backup folder exists), so a machine that never ran it sees
// no setup lines.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/share"
)

// wyPathRe finds the absolute wy path baked into a generated hook command:
// `[ -x '/home/u/.local/bin/wy' ] && ...`.
var wyPathRe = regexp.MustCompile(`\[ -x '([^']+)' \]`)

func checkSetup(rep *Report, o Options, cfg config.Config, slots []identity.Slot) {
	st := o.Dirs.State.Path
	if st == "" {
		return
	}
	_, lerr := os.Stat(ledger.Path(st))
	_, berr := os.Stat(backup.Root(st))
	if lerr != nil && berr != nil {
		return // wy setup has never run here
	}

	// An interrupted run (7.6) is a failure until it is undone. A set in progress while
	// setup.lock is held belongs to a command still running.
	if ids, err := backup.InProgress(st); err == nil && len(ids) > 0 && !setupRunning(st) {
		rep.add(Check{Name: "setup", Level: Fail, Detail: "an earlier wy setup run was interrupted: " + strings.Join(ids, ", "),
			Fix: "wy setup --undo " + ids[0] + " (or wy setup --yes, which undoes it first)"})
	}
	led, err := ledger.Read(st)
	if err != nil {
		rep.add(Check{Name: "setup", Level: Warn, Detail: "cannot read the setup ledger: " + err.Error(),
			Fix: "wy setup --yes rewrites it"})
		return
	}

	// The plugin: marketplace folder present, wy at the baked path, one watchyard plugin.
	if led.Plugin != nil && !led.Plugin.Adopted && led.Plugin.Marketplace != "" {
		hooks := filepath.Join(led.Plugin.Marketplace, "plugins", "watchyard", "hooks", "hooks.json")
		b, err := os.ReadFile(hooks)
		switch {
		case err != nil:
			rep.add(Check{Name: "plugin", Level: Fail, Detail: "the wy-local marketplace folder is missing (" + led.Plugin.Marketplace + ")",
				Fix: "wy setup --yes writes it again"})
		default:
			if m := wyPathRe.FindSubmatch(b); m != nil {
				p := string(m[1])
				if _, err := os.Stat(p); err != nil {
					rep.add(Check{Name: "plugin", Level: Fail, Detail: "hooks call " + p + ", which does not exist",
						Fix: "reinstall wy there, or run wy setup --yes from the new wy"})
				} else {
					rep.add(Check{Name: "plugin", Level: OK, Detail: "watchyard@wy-local hooks call " + p})
				}
				look := o.LookPath
				if look == nil {
					look = lookPath
				}
				if _, err := look("wy"); err != nil {
					rep.add(Check{Name: "wy on PATH", Level: Warn, Detail: "wy is not on PATH (hooks still work: they use " + p + ")",
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
			if en && (k == "watchyard" || strings.HasPrefix(k, "watchyard@")) {
				on = append(on, k)
			}
		}
		if len(on) > 1 {
			rep.add(Check{Name: "plugin", Slot: s.Name, Level: Warn,
				Detail: fmt.Sprintf("%d watchyard plugins are enabled (%s): every event is reported twice", len(on), strings.Join(on, ", ")),
				Fix:    "wy setup --yes disables all but watchyard@wy-local"})
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
					Fix: "restore the target, or wy setup --undo the run that made the link"})
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
			Fix: "if you meant it, remove the servers in every folder by hand; otherwise add them back in " + strings.Join(g.By, ", ") + " (wy tick then resumes)"})
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
					Fix: "make the copies equal by hand; wy tick resumes syncing"})
			}
		}
	}

	// Hub token and timer while a hub is configured.
	if cfg.Hub.URL != "" {
		if _, err := os.Stat(filepath.Join(st, "token")); err != nil {
			rep.add(Check{Name: "hub", Level: Fail, Detail: "hub.url is " + cfg.Hub.URL + " but there is no device token",
				Fix: "wy connect " + cfg.Hub.URL + " --token-stdin"})
		}
	}
	if led.Timer {
		if !timerPresent(o) {
			rep.add(Check{Name: "timer", Level: Warn, Detail: "the flush timer wy installed is gone",
				Fix: "wy connect <url> --token-stdin installs it again"})
		}
	}
	if lerr == nil {
		rep.add(Check{Name: "setup", Level: OK, Detail: "ledger " + ledger.Path(st)})
	}
}

// timerPresent looks for the timer's unit file (no service manager is asked).
// setupRunning is true while another wy command holds setup.lock.
func setupRunning(stateDir string) bool {
	unlock, err := apply.Lock(stateDir)
	if err != nil {
		return errors.Is(err, apply.ErrLocked)
	}
	unlock()
	return false
}

func timerPresent(o Options) bool {
	// The names before a rename count too: `wy update` moves that timer (rename.md 2.3).
	for _, slug := range []string{brand.Slug, brand.OldSlug()} {
		for _, p := range []string{
			filepath.Join(o.Home, ".config", "systemd", "user", slug+"-flush.timer"),
			filepath.Join(o.Home, "Library", "LaunchAgents", "com."+slug+".flush.plist"),
			filepath.Join(o.Dirs.State.Path, slug+"-flush.task.xml"),
		} {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}
	return false
}
