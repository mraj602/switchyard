// Package ledger reads and writes `<state>/setup/ledger.json`, the record of what wy has
// installed now (setup.md 9.3), and updates it from a finished backup manifest: Update
// after an apply, Revert after an undo. `wy uninstall` and `wy doctor` plan from it.
//
// Depends on: internal/atomicfile, internal/setup/model.
package ledger

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// Path returns <state>/setup/ledger.json.
func Path(stateDir string) string { return filepath.Join(stateDir, "setup", "ledger.json") }

// Read loads the ledger; a missing file is an empty ledger (V 1), not an error.
func Read(stateDir string) (model.Ledger, error) {
	b, err := os.ReadFile(Path(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return model.Ledger{V: 1}, nil
	}
	if err != nil {
		return model.Ledger{}, err
	}
	var l model.Ledger
	if err := json.Unmarshal(b, &l); err != nil {
		return model.Ledger{}, err
	}
	if l.V == 0 {
		l.V = 1
	}
	return l, nil
}

// Write stores the ledger atomically (folder 0700, file 0600).
func Write(stateDir string, l model.Ledger) error {
	l.V = 1
	if err := os.MkdirAll(filepath.Dir(Path(stateDir)), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(&l, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(Path(stateDir), append(b, '\n'), 0o600)
}

const (
	pluginID  = brand.Slug + "@" + brand.Marketplace
	envKey    = "/env/CLAUDE_CODE_PLUGIN_CACHE_DIR"
	envObj    = "/env"
	statusPtr = "/statusLine"
)

// Update returns prev with the effect of the finished entries of m, joined to the plan's
// actions by action id (the manifest holds paths and hashes; the plan holds argv, step and
// the redacted-in-output values the ledger needs, such as the original status line command).
// Entries not in phase "done" are ignored.
func Update(prev model.Ledger, p model.Plan, m model.Manifest) model.Ledger {
	l := clone(prev)
	l.V = 1
	acts := map[string]model.Action{}
	for _, a := range p.Actions {
		acts[a.ID] = a
	}
	for _, e := range m.Entries {
		if e.Phase != model.PhaseDone {
			continue
		}
		a, ok := acts[e.Action]
		if !ok {
			continue
		}
		l = updateTerminal(l, a, e) // the ledger's terminal block (terminal.go)
		switch a.Op {
		case model.OpJSONSet, model.OpJSONDelete:
			updateJSON(&l, a)
		case model.OpWriteFile:
			if a.Step == model.StepLaunchers {
				l.Launchers = upsertLauncher(l.Launchers, model.LedgerLauncher{Path: a.Path, SHA256: a.ContentSHA256})
			}
		case model.OpWriteTree:
			if a.Step == model.StepPlugin {
				if l.Plugin == nil {
					l.Plugin = &model.LedgerPlugin{ID: pluginID}
				}
				l.Plugin.Marketplace = a.Path
			}
		case model.OpRun:
			updateRun(&l, a)
		case model.OpMCPSync:
			l.MCPSync = addAll(l.MCPSync, a.Files)
		case model.OpLink, model.OpReplaceWithLink:
			if a.Kind == model.LinkCopy {
				l.Copies = addAll(l.Copies, []string{a.Path})
			}
		case model.OpService:
			l.HubService = &model.LedgerService{Name: a.Name, Unit: a.Unit}
		case model.OpConnect:
			if e.Name == "timer" {
				l.Timer = true
			}
		}
	}
	return l
}

func setsPluginCacheDir(v json.RawMessage) bool {
	var o map[string]json.RawMessage
	if json.Unmarshal(v, &o) != nil {
		return false
	}
	_, ok := o["CLAUDE_CODE_PLUGIN_CACHE_DIR"]
	return ok
}

func updateJSON(l *model.Ledger, a model.Action) {
	switch {
	case a.Pointer == envKey && a.Op == model.OpJSONSet:
		l.EnvKeys = addAll(l.EnvKeys, []string{a.File})
	case a.Pointer == envObj && a.Op == model.OpJSONSet && setsPluginCacheDir(a.Value):
		// A settings file without an env block gets the whole object (the planner sets
		// /env, not /env/<key>): the key is still ours, so uninstall must remove it.
		l.EnvKeys = addAll(l.EnvKeys, []string{a.File})
	case (a.Pointer == statusPtr+"/command" || a.Pointer == statusPtr) && a.Op == model.OpJSONSet:
		if l.StatusLine == nil {
			l.StatusLine = map[string]model.LedgerStatus{}
		}
		if _, have := l.StatusLine[a.File]; have {
			return // already ours: keep the first record (idempotent)
		}
		var orig string
		if len(a.Before) > 0 && json.Unmarshal(a.Before, &orig) == nil && a.Pointer != statusPtr {
			l.StatusLine[a.File] = model.LedgerStatus{Original: orig}
		} else if len(a.Before) == 0 {
			l.StatusLine[a.File] = model.LedgerStatus{Created: true}
		} else if a.Pointer == statusPtr {
			// whole object replaced: keep its command if it had one
			var o struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(a.Before, &o)
			l.StatusLine[a.File] = model.LedgerStatus{Original: o.Command}
		}
	}
}

func updateRun(l *model.Ledger, a model.Action) {
	if a.Step != model.StepPlugin {
		return
	}
	isPlugin := false
	for _, s := range a.Argv {
		if s == pluginID || s == brand.Marketplace || strings.HasSuffix(s, "/marketplace") {
			isPlugin = true
		}
	}
	if !isPlugin {
		return
	}
	if l.Plugin == nil {
		l.Plugin = &model.LedgerPlugin{ID: pluginID}
	}
	l.Plugin.Roots = addAll(l.Plugin.Roots, a.Watch)
	for _, t := range a.Touches {
		if filepath.Base(t) == "settings.json" {
			l.Plugin.Targets = addAll(l.Plugin.Targets, []string{t})
		}
	}
}

// Revert returns prev minus what the reverted entries of m had added. It works from the
// manifest alone (an undo has no plan): paths, recorded keys and the entry's op.
func Revert(prev model.Ledger, m model.Manifest) model.Ledger {
	l := clone(prev)
	for _, e := range m.Entries {
		if e.Phase != model.PhaseReverted {
			continue
		}
		var terminal bool
		if l, terminal = revertTerminal(l, e); terminal {
			continue // a terminal entry: terminal.go owns its effect on the ledger
		}
		for _, pe := range e.Paths {
			switch e.Op {
			case model.OpJSONSet, model.OpJSONDelete:
				for _, k := range pe.Keys {
					switch {
					case k.Pointer == envKey || k.Pointer == envObj:
						l.EnvKeys = remove(l.EnvKeys, pe.Path)
					case k.Pointer == statusPtr || strings.HasPrefix(k.Pointer, statusPtr+"/"):
						delete(l.StatusLine, pe.Path)
					}
				}
			case model.OpWriteFile:
				var keep []model.LedgerLauncher
				for _, x := range l.Launchers {
					if x.Path != pe.Path {
						keep = append(keep, x)
					}
				}
				l.Launchers = keep
			case model.OpMCPSync:
				l.MCPSync = remove(l.MCPSync, pe.Path)
			case model.OpLink, model.OpReplaceWithLink:
				l.Copies = remove(l.Copies, pe.Path)
			case model.OpWriteTree:
				if l.Plugin != nil && l.Plugin.Marketplace == pe.Path {
					l.Plugin.Marketplace = ""
				}
			}
		}
		switch e.Op {
		case model.OpRun:
			for _, c := range e.Created {
				if strings.Contains(c, brand.Marketplace) {
					l.Plugin = nil
					break
				}
			}
			for _, pe := range e.Paths {
				if filepath.Base(pe.Path) == "installed_plugins.json" && l.Plugin != nil {
					l.Plugin = nil
				}
			}
		case model.OpService:
			if l.HubService != nil && l.HubService.Name == e.Name {
				l.HubService = nil
			}
		case model.OpConnect:
			if e.Name == "timer" {
				l.Timer = false
			}
		}
	}
	if l.Plugin != nil && l.Plugin.Marketplace == "" && len(l.Plugin.Roots) == 0 && len(l.Plugin.Targets) == 0 && !l.Plugin.Adopted {
		l.Plugin = nil
	}
	return l
}

func clone(l model.Ledger) model.Ledger {
	b, _ := json.Marshal(l)
	var out model.Ledger
	_ = json.Unmarshal(b, &out)
	return out
}

func upsertLauncher(xs []model.LedgerLauncher, n model.LedgerLauncher) []model.LedgerLauncher {
	for i := range xs {
		if xs[i].Path == n.Path {
			xs[i] = n
			return xs
		}
	}
	return append(xs, n)
}

func addAll(xs, add []string) []string {
	set := map[string]bool{}
	for _, x := range xs {
		set[x] = true
	}
	for _, a := range add {
		if !set[a] {
			xs = append(xs, a)
			set[a] = true
		}
	}
	sort.Strings(xs)
	return xs
}

func remove(xs []string, x string) []string {
	var out []string
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
