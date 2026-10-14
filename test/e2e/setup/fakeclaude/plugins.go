package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const pluginHelp = `Usage: claude plugin [options] [command]

Manage Claude Code plugins

Commands:
  install|i [options] <plugin>  Install a plugin from available marketplaces
  uninstall|remove <plugin>     Uninstall an installed plugin
  enable <plugin>               Enable a disabled plugin
  disable <plugin>              Disable an enabled plugin
  update <plugin>               Update a plugin to the latest version
  list [options]                List installed plugins
  validate <path>               Validate a plugin or marketplace manifest
  marketplace                   Manage Claude Code marketplaces
`

const marketplaceHelp = `Usage: claude plugin marketplace [options] [command]

Manage Claude Code marketplaces

Commands:
  add [options] <source>  Add a marketplace from a URL, path, or GitHub repo
  list [options]          List all configured marketplaces
  remove|rm <name>        Remove a configured marketplace
  update [name]           Update marketplace(s) from their source
`

// pluginEnv is the state of the plugin root and settings of the running folder.
type pluginEnv struct {
	f        folder
	jsonOut  bool
	root     string // plugin root as Claude Code names it (through links; used for installPath)
	settings string // <folder>/settings.json
}

func (a *app) settingsEnv(f folder) *obj {
	o, err := readObj(filepath.Join(f.dir, "settings.json"))
	if err != nil {
		return newObj()
	}
	return o
}

// pluginRoot is CLAUDE_CODE_PLUGIN_CACHE_DIR in the process, else in settings env, else
// <folder>/plugins (setup.md 5.2).
func (a *app) pluginRoot(f folder) string {
	if v := a.env(pluginCacheEnv); v != "" {
		return v
	}
	if v := a.settingsEnv(f).sub("env").str(pluginCacheEnv); v != "" {
		return v
	}
	return filepath.Join(f.dir, "plugins")
}

func (a *app) plugin(f folder, args, pos []string) int {
	jsonOut := has(args, "--json")
	pe := &pluginEnv{f: f, jsonOut: jsonOut, root: a.pluginRoot(f), settings: filepath.Join(f.dir, "settings.json")}
	if len(pos) == 0 {
		a.printf("%s", pluginHelp)
		return 0
	}
	rest := pos[1:]
	switch pos[0] {
	case "marketplace", "market":
		if len(rest) == 0 {
			a.printf("%s", marketplaceHelp)
			return 0
		}
		return a.marketplace(pe, rest[0], rest[1:])
	case "install", "i":
		return a.install(pe, rest)
	case "uninstall", "remove", "rm":
		return a.uninstall(pe, rest)
	case "enable":
		return a.setEnabled(pe, rest, true)
	case "disable":
		return a.setEnabled(pe, rest, false)
	case "update":
		return a.updatePlugin(pe, rest)
	case "list", "ls":
		return a.list(pe)
	case "validate":
		return a.validate(pe, rest)
	}
	return a.fail(jsonOut, "error: unknown plugin command "+pos[0])
}

func (pe *pluginEnv) file(name string) string { return filepath.Join(pe.root, name) }

func (a *app) breakLink() bool { return a.env("FAKE_CLAUDE_BREAK_LINK") == "1" }

func (a *app) writeSettings(pe *pluginEnv, o *obj) error {
	return writeThrough(pe.settings, o.bytes(), 0o644, a.breakLink())
}

func (a *app) stamp() string { return a.now().UTC().Format("2006-01-02T15:04:05.000Z") }

// ---- outcome lines -----------------------------------------------------------------------------
//
// Shapes recorded from Claude Code 2.1.296 `plugin ... --json` (the contract test compares them):
//
//	plugin ok:    {"command":"install|enable|disable|uninstall","outcome":"ok","plugin":"p@m","pluginId":"p@m",
//	               "scope":"user","message":"Successfully enabled plugin: p (scope: user)"}  (+ keptData on uninstall;
//	               + installedVersion, availableVersion when install finds it already installed)
//	plugin fail:  {"command":"enable","outcome":"failed","plugin":"p@m","message":"Plugin \"p@m\" is already enabled",
//	               "failureCode":"already_in_goal_state","alreadyInGoalState":true}, exit 1, and on stderr
//	               "✘ Failed to enable plugin \"p@m\": <message>". install/uninstall failures also carry "scope".
//	               failureCodes seen: already_in_goal_state (enable, disable), not_installed (uninstall),
//	               not_found (install).
//	marketplace:  {"command":"marketplace-add|marketplace-remove","outcome":"ok","marketplace":"m","message":...};
//	               failure of remove: failureCode "not_configured", no alreadyInGoalState. Adding one that exists is
//	               ok ("Marketplace 'm' already on disk").

func (a *app) pluginOK(pe *pluginEnv, command, id, msg string, extra ...[2]any) int {
	if !pe.jsonOut {
		a.printf("\u2714 %s\n", msg)
		return 0
	}
	o := newObj()
	o.set("command", command)
	o.set("outcome", "ok")
	o.set("plugin", id)
	o.set("pluginId", id)
	o.set("scope", "user")
	for _, kv := range extra {
		o.set(kv[0].(string), kv[1])
	}
	o.set("message", msg)
	a.printf("%s\n", o.compact())
	return 0
}

func (a *app) pluginFail(pe *pluginEnv, command, id, code, msg string, scoped bool) int {
	if pe.jsonOut {
		o := newObj()
		o.set("command", command)
		o.set("outcome", "failed")
		o.set("plugin", id)
		if scoped {
			o.set("scope", "user")
		}
		o.set("message", msg)
		o.set("failureCode", code)
		if code == "already_in_goal_state" {
			o.set("alreadyInGoalState", true)
		}
		a.printf("%s\n", o.compact())
	}
	a.eprintf("\u2718 Failed to %s plugin %q: %s\n", command, id, msg)
	return 1
}

func (a *app) mktOK(pe *pluginEnv, command, name, msg string) int {
	if !pe.jsonOut {
		a.printf("\u2714 %s\n", msg)
		return 0
	}
	o := newObj()
	o.set("command", "marketplace-"+command)
	o.set("outcome", "ok")
	o.set("marketplace", name)
	o.set("message", msg)
	a.printf("%s\n", o.compact())
	return 0
}

func (a *app) mktFail(pe *pluginEnv, command, name, code, msg string) int {
	if pe.jsonOut {
		o := newObj()
		o.set("command", "marketplace-"+command)
		o.set("outcome", "failed")
		o.set("marketplace", name)
		o.set("message", msg)
		o.set("failureCode", code)
		a.printf("%s\n", o.compact())
	}
	a.eprintf("\u2718 Failed to %s marketplace: %s\n", command, msg)
	return 1
}

// ---- marketplaces ----------------------------------------------------------------------------

type marketFile struct {
	Name    string `json:"name"`
	Plugins []struct {
		Name    string          `json:"name"`
		Source  json.RawMessage `json:"source"`
		Version string          `json:"version"`
	} `json:"plugins"`
}

func readMarketFile(dir string) (marketFile, error) {
	var m marketFile
	b, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	if err != nil {
		return m, fmt.Errorf("Marketplace file not found at %s", filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Name == "" {
		return m, fmt.Errorf("Invalid marketplace.json in %s", dir)
	}
	return m, nil
}

func (a *app) marketplace(pe *pluginEnv, cmd string, rest []string) int {
	known, err := readObj(pe.file("known_marketplaces.json"))
	if err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	args := positional(rest)
	switch cmd {
	case "add":
		if len(args) != 1 {
			return a.fail(pe.jsonOut, "error: missing required argument 'source'")
		}
		dir, _ := filepath.Abs(args[0])
		m, err := readMarketFile(dir)
		if err != nil {
			return a.fail(pe.jsonOut, err.Error())
		}
		_ = os.MkdirAll(pe.file("marketplaces"), 0o755)
		_, existed := known.get(m.Name)
		src := map[string]any{"source": "directory", "path": dir}
		ent := newObj()
		ent.set("source", src)
		ent.set("installLocation", dir)
		ent.set("lastUpdated", a.stamp())
		known.set(m.Name, json.RawMessage(ent.compact()))
		if err := writeThrough(pe.file("known_marketplaces.json"), known.bytes(), 0o644, false); err != nil {
			return a.fail(pe.jsonOut, err.Error())
		}
		s := a.settingsEnv(pe.f)
		extra := s.sub("extraKnownMarketplaces")
		extra.set(m.Name, map[string]any{"source": src})
		s.set("extraKnownMarketplaces", json.RawMessage(extra.compact()))
		if err := a.writeSettings(pe, s); err != nil {
			return a.fail(pe.jsonOut, err.Error())
		}
		if existed {
			return a.mktOK(pe, "add", m.Name, fmt.Sprintf("Marketplace '%s' already on disk \u2014 declared in user settings", m.Name))
		}
		return a.mktOK(pe, "add", m.Name, "Successfully added marketplace: "+m.Name+" (declared in user settings)")
	case "remove", "rm":
		if len(args) != 1 {
			return a.fail(pe.jsonOut, "error: missing required argument 'name'")
		}
		if _, ok := known.get(args[0]); !ok {
			return a.mktFail(pe, "remove", args[0], "not_configured", fmt.Sprintf("Marketplace '%s' not found", args[0]))
		}
		known.del(args[0])
		if err := writeThrough(pe.file("known_marketplaces.json"), known.bytes(), 0o644, false); err != nil {
			return a.fail(pe.jsonOut, err.Error())
		}
		s := a.settingsEnv(pe.f)
		extra := s.sub("extraKnownMarketplaces")
		extra.del(args[0])
		s.set("extraKnownMarketplaces", json.RawMessage(extra.compact()))
		if en, ok := s.get("enabledPlugins"); ok {
			// removing a marketplace also drops its plugins from enabledPlugins
			eo, _ := parseObj(en)
			for _, k := range append([]string(nil), eo.keys...) {
				if strings.HasSuffix(k, "@"+args[0]) {
					eo.del(k)
				}
			}
			s.set("enabledPlugins", json.RawMessage(eo.compact()))
		}
		if err := a.writeSettings(pe, s); err != nil {
			return a.fail(pe.jsonOut, err.Error())
		}
		return a.mktOK(pe, "remove", args[0], "Successfully removed marketplace: "+args[0])
	case "update":
		names := args
		if len(names) == 0 {
			names = known.keys
		}
		for _, n := range names {
			if _, ok := known.get(n); !ok {
				return a.fail(pe.jsonOut, fmt.Sprintf("Marketplace '%s' not found", n))
			}
		}
		return a.ok(pe.jsonOut, "Successfully updated "+fmt.Sprint(len(names))+" marketplace(s)")
	case "list", "ls":
		type row struct {
			Name            string `json:"name"`
			Source          any    `json:"source"`
			Path            string `json:"path,omitempty"`
			InstallLocation string `json:"installLocation"`
		}
		var rows []row
		for _, k := range known.keys {
			e := known.sub(k)
			var src any
			path := ""
			if raw, ok := e.get("source"); ok {
				_ = json.Unmarshal(raw, &src)
				// the real list flattens a directory source: "source":"directory","path":"<dir>"
				if m, ok := src.(map[string]any); ok && m["source"] == "directory" {
					src, path = "directory", fmt.Sprint(m["path"])
				}
			}
			rows = append(rows, row{k, src, path, e.str("installLocation")})
		}
		if pe.jsonOut {
			if rows == nil {
				rows = []row{}
			}
			a.printf("%s\n", enc(rows))
			return 0
		}
		if len(rows) == 0 {
			a.printf("No marketplaces configured\n")
		}
		for _, r := range rows {
			a.printf("  %s\n    Source: %v\n", r.Name, r.Source)
		}
		return 0
	}
	return a.fail(pe.jsonOut, "error: unknown marketplace command "+cmd)
}

// ---- plugins ---------------------------------------------------------------------------------

func (a *app) readInstalled(pe *pluginEnv) (*obj, *obj, error) {
	top, err := readObj(pe.file("installed_plugins.json"))
	if err != nil {
		return nil, nil, err
	}
	if _, ok := top.get("version"); !ok {
		top.set("version", 2)
	}
	return top, top.sub("plugins"), nil
}

func (a *app) writeInstalled(pe *pluginEnv, top, plugins *obj) error {
	top.set("plugins", json.RawMessage(plugins.compact()))
	return writeThrough(pe.file("installed_plugins.json"), top.bytes(), 0o644, false)
}

func splitID(id string) (name, market string) {
	name, market, _ = strings.Cut(id, "@")
	return
}

// marketPlugin finds plugin name in marketplace market of this root: the folder Claude reads
// the plugin from and its version.
func (a *app) marketPlugin(pe *pluginEnv, name, market string) (srcDir, version string, found bool, err error) {
	known, err := readObj(pe.file("known_marketplaces.json"))
	if err != nil {
		return "", "", false, err
	}
	ent, ok := known.get(market)
	if !ok {
		return "", "", false, nil
	}
	mdir := ""
	if o, err := parseObj(ent); err == nil {
		mdir = o.str("installLocation")
	}
	m, err := readMarketFile(mdir)
	if err != nil {
		return "", "", false, err
	}
	for _, p := range m.Plugins {
		if p.Name != name {
			continue
		}
		var s string
		if json.Unmarshal(p.Source, &s) == nil {
			srcDir = filepath.Join(mdir, filepath.FromSlash(s))
		}
		version = p.Version
		found = srcDir != ""
	}
	if !found {
		return "", "", false, nil
	}
	if pj, err := readObj(filepath.Join(srcDir, ".claude-plugin", "plugin.json")); err == nil {
		if v := pj.str("version"); v != "" {
			version = v
		}
	}
	if version == "" {
		version = "unknown"
	}
	return srcDir, version, true, nil
}

func notFoundMsg(name, market string) string {
	return fmt.Sprintf("Plugin %q not found in marketplace %q. Your local copy may be out of date — try `claude plugin marketplace update %s`.", name, market, market)
}

func (a *app) install(pe *pluginEnv, rest []string) int {
	args := positional(rest)
	if len(args) != 1 {
		return a.fail(pe.jsonOut, "error: missing required argument 'plugin'")
	}
	id := args[0]
	name, market := splitID(id)
	if market == "" {
		return a.fail(pe.jsonOut, "Plugin name must be given as <plugin>@<marketplace>")
	}
	srcDir, version, found, err := a.marketPlugin(pe, name, market)
	if err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	if !found {
		return a.pluginFail(pe, "install", id, "not_found", notFoundMsg(name, market), true)
	}
	top, plugins, err := a.readInstalled(pe)
	if err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	if _, ok := plugins.get(id); ok && string(a.settingsEnv(pe.f).sub("enabledPlugins").vals[id]) == "true" {
		// Recorded in this root and already enabled for this folder: nothing is rewritten (the earlier installPath
		// stays). Without the enabledPlugins entry the install runs in full: the path moves to this folder (setup.md 5.2).
		return a.pluginOK(pe, "install", id,
			fmt.Sprintf("Plugin %q is already installed (scope: user) — it loads in place from %s, so edits there take effect at the next session start or /reload-plugins", id, srcDir),
			[2]any{"installedVersion", version}, [2]any{"availableVersion", version})
	}
	cache := filepath.Join(pe.root, "cache", market, name, version)
	if err := os.RemoveAll(cache); err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	if err := copyTree(srcDir, cache); err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	_ = os.MkdirAll(pe.file("marketplaces"), 0o755)
	rec := newObj()
	rec.set("scope", "user")
	rec.set("installPath", cache) // through the unresolved folder path of the folder that ran the command
	rec.set("version", version)
	rec.set("installedAt", a.stamp())
	rec.set("lastUpdated", a.stamp())
	plugins.set(id, []json.RawMessage{rec.compact()})
	if err := a.writeInstalled(pe, top, plugins); err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	s := a.settingsEnv(pe.f)
	en := s.sub("enabledPlugins")
	en.set(id, true)
	s.set("enabledPlugins", json.RawMessage(en.compact()))
	if err := a.writeSettings(pe, s); err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	return a.pluginOK(pe, "install", id, fmt.Sprintf("Successfully installed plugin: %s (scope: user)", id))
}

func (a *app) uninstall(pe *pluginEnv, rest []string) int {
	args := positional(rest)
	if len(args) != 1 {
		return a.fail(pe.jsonOut, "error: missing required argument 'plugin'")
	}
	id := args[0]
	top, plugins, err := a.readInstalled(pe)
	if err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	if _, ok := plugins.get(id); !ok {
		return a.pluginFail(pe, "uninstall", id, "not_installed", fmt.Sprintf("Plugin %q not found in installed plugins", id), true)
	}
	plugins.del(id)
	if err := a.writeInstalled(pe, top, plugins); err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	// The cache is kept; every version folder gets an .orphaned_at marker (epoch milliseconds).
	name, market := splitID(id)
	if vers, err := os.ReadDir(filepath.Join(pe.root, "cache", market, name)); err == nil {
		for _, v := range vers {
			if v.IsDir() {
				_ = os.WriteFile(filepath.Join(pe.root, "cache", market, name, v.Name(), ".orphaned_at"),
					[]byte(fmt.Sprint(a.now().UnixMilli())), 0o644)
			}
		}
	}
	// Only the running folder's settings lose the entry; other settings files that share the
	// plugin root still say true (setup.md 5.2).
	s := a.settingsEnv(pe.f)
	if _, ok := s.get("enabledPlugins"); ok {
		en := s.sub("enabledPlugins")
		en.del(id)
		s.set("enabledPlugins", json.RawMessage(en.compact()))
		if err := a.writeSettings(pe, s); err != nil {
			return a.fail(pe.jsonOut, err.Error())
		}
	}
	return a.pluginOK(pe, "uninstall", id, fmt.Sprintf("Successfully uninstalled plugin: %s (scope: user)", name), [2]any{"keptData", false})
}

func (a *app) setEnabled(pe *pluginEnv, rest []string, on bool) int {
	args := positional(rest)
	if len(args) != 1 {
		return a.fail(pe.jsonOut, "error: missing required argument 'plugin'")
	}
	id := args[0]
	verb := "enable"
	if !on {
		verb = "disable"
	}
	name, market := splitID(id)
	_, plugins, err := a.readInstalled(pe)
	if err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	if _, ok := plugins.get(id); !ok {
		// The real command also accepts a plugin that only its marketplace knows.
		if _, _, found, _ := a.marketPlugin(pe, name, market); !found {
			return a.pluginFail(pe, verb, id, "not_found", fmt.Sprintf("Plugin %q not found", id), false)
		}
	}
	s := a.settingsEnv(pe.f)
	en := s.sub("enabledPlugins")
	if string(en.vals[id]) == fmt.Sprint(on) {
		return a.pluginFail(pe, verb, id, "already_in_goal_state", fmt.Sprintf("Plugin %q is already %sd", id, verb), false)
	}
	en.set(id, on)
	s.set("enabledPlugins", json.RawMessage(en.compact()))
	if err := a.writeSettings(pe, s); err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	return a.pluginOK(pe, verb, id, fmt.Sprintf("Successfully %sd plugin: %s (scope: user)", verb, name))
}

func (a *app) updatePlugin(pe *pluginEnv, rest []string) int {
	args := positional(rest)
	if len(args) != 1 {
		return a.fail(pe.jsonOut, "error: missing required argument 'plugin'")
	}
	_, plugins, err := a.readInstalled(pe)
	if err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	if _, ok := plugins.get(args[0]); !ok {
		return a.fail(pe.jsonOut, fmt.Sprintf("Plugin '%s' is not installed", args[0]))
	}
	return a.ok(pe.jsonOut, fmt.Sprintf("%s is already at the latest version", args[0]), [2]any{"id", args[0]})
}

func (a *app) list(pe *pluginEnv) int {
	_, plugins, err := a.readInstalled(pe)
	if err != nil {
		return a.fail(pe.jsonOut, err.Error())
	}
	enabled := a.settingsEnv(pe.f).sub("enabledPlugins")
	type row struct {
		ID          string `json:"id"`
		Version     string `json:"version"`
		Scope       string `json:"scope"`
		Enabled     bool   `json:"enabled"`
		InstallPath string `json:"installPath"`
		ReadFrom    string `json:"readFromFolder,omitempty"`
		FolderVer   string `json:"folderVersion,omitempty"`
		InstalledAt string `json:"installedAt,omitempty"`
		LastUpdated string `json:"lastUpdated,omitempty"`
		ProjectOn   bool   `json:"projectEnabled"`
	}
	rows := []row{}
	keys := append([]string(nil), plugins.keys...)
	sort.Strings(keys)
	for _, id := range keys {
		var recs []map[string]any
		_ = json.Unmarshal(plugins.vals[id], &recs)
		for _, r := range recs {
			rw := row{ID: id, Enabled: string(enabled.vals[id]) == "true"}
			rw.Version, _ = r["version"].(string)
			rw.Scope, _ = r["scope"].(string)
			rw.InstallPath, _ = r["installPath"].(string)
			rw.InstalledAt, _ = r["installedAt"].(string)
			rw.LastUpdated, _ = r["lastUpdated"].(string)
			name, market := splitID(id)
			if src, ver, found, _ := a.marketPlugin(pe, name, market); found {
				rw.ReadFrom, rw.FolderVer = src, ver
			}
			rows = append(rows, rw)
		}
	}
	if pe.jsonOut {
		a.printf("%s\n", enc(rows))
		return 0
	}
	if len(rows) == 0 {
		a.printf("No plugins installed\n")
	}
	for _, r := range rows {
		st := "disabled"
		if r.Enabled {
			st = "enabled"
		}
		a.printf("  %s\n    Version: %s\n    Scope: %s\n    Status: %s\n", r.ID, r.Version, r.Scope, st)
	}
	return 0
}

// validate accepts a folder holding .claude-plugin/plugin.json or marketplace.json that
// parses; the real command checks far more.
func (a *app) validate(pe *pluginEnv, rest []string) int {
	args := positional(rest)
	if len(args) != 1 {
		return a.fail(false, "error: missing required argument 'path'")
	}
	for _, n := range []string{"plugin.json", "marketplace.json"} {
		if _, err := readObj(filepath.Join(args[0], ".claude-plugin", n)); err == nil {
			if _, err := os.Stat(filepath.Join(args[0], ".claude-plugin", n)); err == nil {
				a.printf("Validation passed\n")
				return 0
			}
		}
	}
	return a.fail(false, "No manifest found in "+args[0])
}

// copyTree copies src into dst (files 0644, folders 0755; the execute bit is kept).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		mode := os.FileMode(0o644)
		if fi, err := d.Info(); err == nil && fi.Mode()&0o111 != 0 {
			mode = 0o755
		}
		w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, in); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	})
}
