package doctor

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mraj602/switchyard/internal/atomicfile"
	"github.com/mraj602/switchyard/internal/identity"
)

func lookPath(name string) (string, error) { return exec.LookPath(name) }

// RequiredEvents are the hook events the plugin installs (states.md section 2).
var RequiredEvents = []string{"SessionStart", "UserPromptSubmit", "PermissionRequest", "Notification",
	"PreToolUse", "Stop", "StopFailure", "SubagentStop", "SessionEnd", "PreCompact", "PermissionDenied"}

var (
	syHookRe       = regexp.MustCompile(`(^|[\s/\\"'])sy(\.exe)?["']?\s+hook(\s|$|["'])`)
	syStatuslineRe = regexp.MustCompile(`(^|[\s/\\"'])sy(\.exe)?["']?\s+statusline(\s|$|["'])`)
)

// settings is the part of a settings.json that doctor reads.
type settings struct {
	hooks    map[string][]string // event -> command lines
	status   string              // statusLine.command
	hasStat  bool
	plugins  map[string]bool // enabledPlugins
	exists   bool
	realPath string
}

// readSettings reads <slot>/settings.json read-only, following symlinks. A missing file is
// not an error (exists is false).
func readSettings(fsys identity.FS, path string) (settings, error) {
	var s settings
	f, err := fsys.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return s, err
	}
	s.exists = true
	if r, err := fsys.EvalSymlinks(path); err == nil {
		s.realPath = r
	}
	var doc struct {
		Hooks          map[string]json.RawMessage `json:"hooks"`
		StatusLine     json.RawMessage            `json:"statusLine"`
		EnabledPlugins map[string]json.RawMessage `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return s, errors.New("settings.json is not valid JSON: " + err.Error())
	}
	s.hooks = map[string][]string{}
	for ev, raw := range doc.Hooks {
		var groups []struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		}
		if json.Unmarshal(raw, &groups) != nil {
			continue
		}
		for _, g := range groups {
			for _, h := range g.Hooks {
				s.hooks[ev] = append(s.hooks[ev], h.Command)
			}
		}
	}
	var sl struct {
		Command string `json:"command"`
	}
	if len(doc.StatusLine) > 0 && json.Unmarshal(doc.StatusLine, &sl) == nil {
		s.status, s.hasStat = sl.Command, true
	}
	s.plugins = map[string]bool{}
	for k, raw := range doc.EnabledPlugins {
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			s.plugins[k] = b
		}
	}
	return s, nil
}

// switchyardPlugin returns the enabled plugin key that is the switchyard plugin, if any.
func (s settings) switchyardPlugin() string {
	var keys []string
	for k, on := range s.plugins {
		if on && (k == "switchyard" || strings.HasPrefix(k, "switchyard@")) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

func checkHooks(rep *Report, slot identity.Slot, s settings, err error) {
	c := Check{Name: "hooks", Slot: slot.Name}
	if err != nil {
		c.Level, c.Detail, c.Fix = Warn, "cannot read settings.json: "+err.Error(), "fix the file, then run sy doctor again"
		rep.add(c)
		return
	}
	if !s.exists {
		c.Level, c.Detail = Warn, "no settings.json in this folder"
		c.Fix = "install the switchyard plugin (plugin/switchyard) in this folder's settings.json, or share the one from ~/.claude by symlink"
		rep.add(c)
		return
	}
	if p := s.switchyardPlugin(); p != "" {
		c.Level, c.Detail = OK, "plugin "+p+" is enabled"
		rep.add(c)
		return
	}
	var have, missing []string
	for _, ev := range RequiredEvents {
		found := false
		for _, cmd := range s.hooks[ev] {
			if syHookRe.MatchString(cmd) {
				found = true
				break
			}
		}
		if found {
			have = append(have, ev)
		} else {
			missing = append(missing, ev)
		}
	}
	switch {
	case len(have) == 0:
		c.Level, c.Detail = Warn, "no `sy hook` command and no switchyard plugin in the effective settings"
		for k, on := range s.plugins {
			if !on && (k == "switchyard" || strings.HasPrefix(k, "switchyard@")) {
				c.Detail = "the switchyard plugin " + k + " is disabled"
			}
		}
		c.Fix = "enable the switchyard plugin in settings.json enabledPlugins (see plugin/switchyard/README.md)"
	case len(missing) > 0:
		c.Level, c.Detail = Warn, "`sy hook` is missing for: "+strings.Join(missing, ", ")
		c.Fix = "add those events (docs/spec/states.md section 2), or use the plugin, which has them all"
	default:
		c.Level, c.Detail = OK, "`sy hook` runs on all "+strconv.Itoa(len(RequiredEvents))+" events"
	}
	rep.add(c)
}

func checkStatusline(rep *Report, slot identity.Slot, s settings, err error) {
	c := Check{Name: "status line", Slot: slot.Name}
	switch {
	case err != nil:
		c.Level, c.Detail, c.Fix = Warn, "cannot read settings.json: "+err.Error(), "fix the file, then run sy doctor again"
	case !s.exists || !s.hasStat || strings.TrimSpace(s.status) == "":
		c.Level, c.Detail = Warn, "no status line command"
		c.Fix = "set statusLine.command to: sy statusline --wrap '<your old command>' (context % and limits come from it)"
	case syStatuslineRe.MatchString(s.status):
		c.Level, c.Detail = OK, "wrapped by sy statusline"
	default:
		c.Level, c.Detail = Warn, "status line does not run sy statusline"
		c.Fix = "set statusLine.command to: sy statusline --wrap '" + oneLine(s.status) + "'"
	}
	rep.add(c)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return strings.ReplaceAll(s, "'", `'\''`)
}

// symlinkRecord is what <state>/doctor.json keeps per slot.
type symlinkRecord struct {
	Symlink bool   `json:"symlink"`
	Target  string `json:"target,omitempty"`
}

type doctorFile struct {
	V        int                      `json:"v"`
	Settings map[string]symlinkRecord `json:"settings_symlinks"`
}

func checkSymlinks(rep *Report, o Options, slots []identity.Slot) {
	path := filepath.Join(o.Dirs.State.Path, "doctor.json")
	doc := doctorFile{V: 1, Settings: map[string]symlinkRecord{}}
	if b, err := os.ReadFile(path); err == nil {
		var x doctorFile
		if json.Unmarshal(b, &x) == nil && x.Settings != nil {
			doc = x
		}
	}
	changed := false
	for _, s := range slots {
		cur := lstatSettings(o.Home, filepath.Join(s.Dir, "settings.json"))
		c := Check{Name: "settings symlink", Slot: s.Name}
		old, had := doc.Settings[s.Name]
		switch {
		case !had || o.Rebaseline:
			doc.Settings[s.Name] = cur
			changed = true
			c.Level = OK
			c.Detail = "recorded: " + describe(cur)
		case old.Symlink && !cur.Symlink:
			c.Level, c.Detail = Warn, "settings.json was a symlink to "+old.Target+" and is not any more (something replaced it)"
			c.Fix = "restore the link, or run sy doctor --rebaseline if this is intended"
		case old.Symlink && cur.Target != old.Target:
			c.Level, c.Detail = Warn, "settings.json now links to "+cur.Target+" (was "+old.Target+")"
			c.Fix = "relink it, or run sy doctor --rebaseline if this is intended"
		default:
			c.Level, c.Detail = OK, describe(cur)
		}
		rep.add(c)
	}
	if changed {
		if err := os.MkdirAll(o.Dirs.State.Path, 0o700); err == nil {
			if b, err := json.MarshalIndent(doc, "", "  "); err == nil {
				_ = atomicfile.Write(path, append(b, '\n'), 0o600)
			}
		}
	}
}

func describe(r symlinkRecord) string {
	switch {
	case r.Symlink:
		return "symlink to " + r.Target
	case r.Target == "missing":
		return "no settings.json"
	}
	return "regular file"
}

// lstatSettings looks at the settings.json name itself (not what it points to).
func lstatSettings(home, path string) symlinkRecord {
	fi, err := os.Lstat(path)
	if err != nil {
		return symlinkRecord{Target: "missing"}
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return symlinkRecord{}
	}
	t, err := os.Readlink(path)
	if err != nil {
		return symlinkRecord{Symlink: true, Target: "unreadable"}
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(path), t)
	}
	return symlinkRecord{Symlink: true, Target: identity.Tilde(home, t)}
}
