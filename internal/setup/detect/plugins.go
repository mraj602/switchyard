package detect

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/statuswrap"
)

const pluginCacheEnv = "CLAUDE_CODE_PLUGIN_CACHE_DIR"

func (x *detector) readJSONFile(path string, v any) error {
	f, err := x.fsys.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// detectPlugins is step 1.4: settings targets, plugin roots, duplicates.
func (x *detector) detectPlugins() {
	targets := map[string]*model.SettingsTarget{}
	var order []string
	for i := range x.st.Folders {
		f := &x.st.Folders[i]
		sp := filepath.Join(f.Dir, "settings.json")
		res := sp
		if r, err := filepath.EvalSymlinks(sp); err == nil {
			res = r
		}
		f.SettingsTarget = res
		t := targets[res]
		if t == nil {
			t = &model.SettingsTarget{Path: res}
			targets[res] = t
			order = append(order, res)
			x.parseSettings(t)
		}
		t.Folders = append(t.Folders, f.Folder)
		if x.hasSkillsDirPlugin(f.Dir, brand.Slug) && !contains(t.WatchyardPlugins, brand.Slug+"@skills-dir") {
			t.WatchyardPlugins = append(t.WatchyardPlugins, brand.Slug+"@skills-dir")
			sort.Strings(t.WatchyardPlugins)
		}
		if brand.Renamed() && x.hasSkillsDirPlugin(f.Dir, brand.OldSlug()) && !contains(t.LegacyPlugins, brand.OldSlug()+"@skills-dir") {
			t.LegacyPlugins = append(t.LegacyPlugins, brand.OldSlug()+"@skills-dir")
			sort.Strings(t.LegacyPlugins)
		}
	}
	for _, p := range order {
		x.st.SettingsTargets = append(x.st.SettingsTargets, *targets[p])
	}

	// Plugin roots.
	procEnv := x.Getenv(pluginCacheEnv)
	roots := map[string]*model.PluginRoot{}
	var rorder []string
	var primaryRoot string
	for i := range x.st.Folders {
		f := &x.st.Folders[i]
		own := filepath.Join(f.Dir, "plugins")
		root, fromEnv := own, false
		if r, err := filepath.EvalSymlinks(own); err == nil {
			root = r
		}
		switch {
		case procEnv != "":
			root, fromEnv = x.realOrClean(procEnv), true
		case targets[f.SettingsTarget].PluginCacheDir != "":
			root, fromEnv = x.realOrClean(targets[f.SettingsTarget].PluginCacheDir), true
		}
		f.PluginRoot = root
		if li, err := os.Lstat(own); err == nil && li.IsDir() && sameDir(own, root) {
			f.OwnsRoot = true
		}
		if f.Primary {
			primaryRoot = root
		}
		pr := roots[root]
		if pr == nil {
			pr = &model.PluginRoot{Root: root, FromEnv: fromEnv}
			roots[root] = pr
			rorder = append(rorder, root)
			x.readRootFiles(pr)
		}
		pr.Folders = append(pr.Folders, f.Folder)
		if f.OwnsRoot && pr.Owner == "" {
			pr.Owner = f.Folder
		}
		if li, err := os.Lstat(own); err == nil && li.Mode()&os.ModeSymlink != 0 && !f.Primary && root == primaryRoot && primaryRoot != "" {
			pr.Adopted = true
		}
	}
	for _, r := range rorder {
		// A link to the primary's plugins/ may be seen before the primary itself.
		if r == primaryRoot {
			for _, f := range x.st.Folders {
				if f.Primary || f.PluginRoot != r {
					continue
				}
				if li, err := os.Lstat(filepath.Join(f.Dir, "plugins")); err == nil && li.Mode()&os.ModeSymlink != 0 {
					roots[r].Adopted = true
				}
			}
		}
		x.st.PluginRoots = append(x.st.PluginRoots, *roots[r])
	}
}

func (x *detector) realOrClean(p string) string {
	p = x.expand(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func (x *detector) hasSkillsDirPlugin(dir, name string) bool {
	skills := filepath.Join(dir, "skills")
	ents, err := x.fsys.ReadDir(skills)
	if err != nil {
		return false
	}
	for _, e := range ents {
		var pj struct{ Name string }
		if x.readJSONFile(filepath.Join(skills, e.Name(), ".claude-plugin", "plugin.json"), &pj) == nil && pj.Name == name {
			return true
		}
	}
	return false
}

// parseSettings reads the keys of a settings file that setup cares about. Values of env are
// never kept (only its keys and the plugin cache dir, which is a path).
func (x *detector) parseSettings(t *model.SettingsTarget) {
	if _, err := os.Stat(t.Path); err != nil {
		return
	}
	t.Exists = true
	var doc struct {
		EnabledPlugins map[string]any             `json:"enabledPlugins"`
		ExtraMarkets   map[string]json.RawMessage `json:"extraKnownMarketplaces"`
		Env            map[string]any             `json:"env"`
		StatusLine     map[string]any             `json:"statusLine"`
		Hooks          map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := x.readJSONFile(t.Path, &doc); err != nil {
		x.warn = append(x.warn, x.tilde(t.Path)+" could not be parsed: "+err.Error())
		return
	}
	for id, v := range doc.EnabledPlugins {
		if t.EnabledPlugins == nil {
			t.EnabledPlugins = map[string]bool{}
		}
		on, _ := v.(bool)
		t.EnabledPlugins[id] = on
		if on && strings.HasPrefix(id, brand.Slug+"@") {
			t.WatchyardPlugins = append(t.WatchyardPlugins, id)
		}
		if on && isLegacyPlugin(id) {
			t.LegacyPlugins = append(t.LegacyPlugins, id)
		}
	}
	sort.Strings(t.WatchyardPlugins)
	sort.Strings(t.LegacyPlugins)
	for n := range doc.ExtraMarkets {
		t.ExtraMarketplaces = append(t.ExtraMarketplaces, n)
	}
	sort.Strings(t.ExtraMarketplaces)
	for k, v := range doc.Env {
		t.EnvKeys = append(t.EnvKeys, k)
		if k == pluginCacheEnv {
			t.PluginCacheDir, _ = v.(string)
		}
	}
	sort.Strings(t.EnvKeys)
	if doc.StatusLine != nil {
		sl := &model.StatusLine{}
		sl.Type, _ = doc.StatusLine["type"].(string)
		sl.Command, _ = doc.StatusLine["command"].(string)
		sl.Ours = sl.Command != "" && statuswrap.IsOurs(sl.Command)
		t.StatusLine = sl
	}
}

// isLegacyPlugin reports whether id is the plugin or pane mod of the project's name before a
// rename, from any marketplace (rename.md 2.4).
func isLegacyPlugin(id string) bool {
	if !brand.Renamed() {
		return false
	}
	o := brand.OldSlug()
	return strings.HasPrefix(id, o+"@") || strings.HasPrefix(id, o+"-pane@")
}

func itoa(i int) string { return strconv.Itoa(i) }

// readRootFiles reads installed_plugins.json and known_marketplaces.json of a plugin root.
func (x *detector) readRootFiles(pr *model.PluginRoot) {
	var inst map[string]json.RawMessage
	if x.readJSONFile(filepath.Join(pr.Root, "installed_plugins.json"), &inst) == nil {
		ids := inst
		if raw, ok := inst["plugins"]; ok {
			ids = nil
			_ = json.Unmarshal(raw, &ids)
		}
		for id := range ids {
			if strings.Contains(id, "@") {
				pr.Plugins = append(pr.Plugins, id)
			}
		}
		sort.Strings(pr.Plugins)
	}
	var known map[string]json.RawMessage
	if x.readJSONFile(filepath.Join(pr.Root, "known_marketplaces.json"), &known) == nil {
		for name, raw := range known {
			pr.Markets = append(pr.Markets, name)
			if src := sourceOf(raw); src != "" {
				if pr.Sources == nil {
					pr.Sources = map[string]string{}
				}
				pr.Sources[name] = src
			}
		}
		sort.Strings(pr.Markets)
	}
}

func sourceOf(raw json.RawMessage) string {
	var m struct {
		Source json.RawMessage `json:"source"`
	}
	if json.Unmarshal(raw, &m) != nil || len(m.Source) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(m.Source, &s) == nil {
		return s
	}
	var o map[string]any
	if json.Unmarshal(m.Source, &o) != nil {
		return ""
	}
	for _, k := range []string{"path", "repo", "url"} {
		if v, ok := o[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
