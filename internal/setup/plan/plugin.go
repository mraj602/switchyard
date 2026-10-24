package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/model"
)

const pluginCacheEnv = "CLAUDE_CODE_PLUGIN_CACHE_DIR"

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func (p *planner) primTarget() string {
	if p.prim.f.SettingsTarget != "" {
		return p.prim.f.SettingsTarget
	}
	return filepath.Join(p.prim.f.Dir, "settings.json")
}

// finalTarget is the settings target a folder uses once the plan is applied.
func (p *planner) finalTarget(n *fnode) string {
	switch {
	case n.primary:
		return p.primTarget()
	case n.links["settings.json"]:
		return p.primTarget()
	case n.f.SettingsTarget != "":
		return n.f.SettingsTarget
	}
	return filepath.Join(n.f.Dir, "settings.json")
}

func (p *planner) targetState(path string) *model.SettingsTarget {
	for i := range p.st.SettingsTargets {
		if p.st.SettingsTargets[i].Path == path {
			return &p.st.SettingsTargets[i]
		}
	}
	return nil
}

func (p *planner) targetExists(path string) bool {
	if t := p.targetState(path); t != nil {
		return t.Exists
	}
	return p.settingsCreated && path == p.primTarget()
}

func (p *planner) rootEntry(root string) *model.PluginRoot {
	for i := range p.st.PluginRoots {
		if p.st.PluginRoots[i].Root == root {
			return &p.st.PluginRoots[i]
		}
	}
	return nil
}

// nodesOfTarget lists the folders that use settings target t after the plan.
func (p *planner) nodesOfTarget(t string) []*fnode {
	var out []*fnode
	for _, n := range p.nodes {
		if p.finalTarget(n) == t {
			out = append(out, n)
		}
	}
	return out
}

// viaOf lists the link paths that lead to settings target t.
func (p *planner) viaOf(t string) []string {
	var out []string
	for _, n := range p.nodes {
		sp := filepath.Join(n.f.Dir, "settings.json")
		if sp != t && p.finalTarget(n) == t {
			out = append(out, sp)
		}
	}
	return out
}

func (p *planner) covers(t string) []string { return p.folderNames(p.nodesOfTarget(t)) }

// sharePluginRoots applies 5.2 "Sharing the plugin root". It fills p.rootOf.
func (p *planner) sharePluginRoots() {
	p.rootOf = map[string]string{}
	others := p.others()
	pIt := p.prim.item("plugins")
	primRoot := p.prim.f.PluginRoot
	if primRoot == "" {
		primRoot = p.pItemPath("plugins")
	}
	p.rootOf[p.prim.f.Folder] = primRoot
	on := p.share.On[model.SwitchPlugins]

	layoutLinks := false
	for _, o := range others {
		it := o.item("plugins")
		if it.State == model.ItemLinked {
			layoutLinks = true
			p.out.Keep = append(p.out.Keep, model.Keep{What: "link", Path: p.tilde(filepath.Join(o.f.Dir, "plugins")), Target: p.tilde(p.pItemPath("plugins"))})
		}
		if it.State == model.ItemElsewhere {
			p.out.Keep = append(p.out.Keep, model.Keep{What: "link-elsewhere", Path: p.tilde(filepath.Join(o.f.Dir, "plugins")), Target: it.Target, Note: "shared elsewhere"})
		}
	}

	var needEnv []*fnode
	for _, o := range others {
		it := o.item("plugins")
		own := filepath.Join(o.f.Dir, "plugins")
		if o.f.PluginRoot != "" {
			p.rootOf[o.f.Folder] = o.f.PluginRoot
		} else {
			p.rootOf[o.f.Folder] = own
		}
		if !on {
			continue
		}
		switch {
		case it.State == model.ItemLinked:
			p.rootOf[o.f.Folder] = primRoot
		case it.State == model.ItemElsewhere:
		case o.isNew:
			if layoutLinks && p.hasRegular(pIt) {
				p.add(model.StepSharing, model.Action{Op: model.OpLink, Path: own, Target: p.pItemPath("plugins"), Kind: p.linkKind(model.ItemSpec{Kind: model.KindDir}),
					Note: "match the layout already in use", Pre: pres(preAbsent(own), preType(p.pItemPath("plugins"), model.TypeDir))})
				o.links["plugins"] = true
				p.rootOf[o.f.Folder] = primRoot
			} else {
				needEnv = append(needEnv, o)
			}
		case it.State == model.ItemAbsent || it.State == model.ItemDir:
			if e := p.rootEntry(o.f.PluginRoot); e != nil && e.FromEnv {
				continue // its root comes from an environment variable already; not ours to change
			}
			needEnv = append(needEnv, o)
		}
	}
	if len(needEnv) == 0 {
		return
	}
	value := p.pItemPath("plugins")
	if e := p.rootEntry(primRoot); e != nil && e.FromEnv {
		value = primRoot
	}
	done := map[string]bool{}
	for _, o := range needEnv {
		t := p.finalTarget(o)
		if done[t] {
			p.rootOf[o.f.Folder] = primRoot
			continue
		}
		if !p.targetExists(t) {
			// A new folder with its own (not yet existing) settings: link the plugin root instead.
			own := filepath.Join(o.f.Dir, "plugins")
			if p.hasRegular(pIt) && o.item("plugins").State == model.ItemAbsent {
				p.add(model.StepSharing, model.Action{Op: model.OpLink, Path: own, Target: p.pItemPath("plugins"), Kind: p.linkKind(model.ItemSpec{Kind: model.KindDir}),
					Pre: pres(preAbsent(own), preType(p.pItemPath("plugins"), model.TypeDir))})
				o.links["plugins"] = true
				p.rootOf[o.f.Folder] = primRoot
			}
			continue
		}
		done[t] = true
		cur := ""
		envKeys := 0
		if ts := p.targetState(t); ts != nil {
			cur, envKeys = ts.PluginCacheDir, len(ts.EnvKeys)
		}
		p.rootOf[o.f.Folder] = primRoot
		for _, n := range p.nodesOfTarget(t) {
			if !n.primary && n.item("plugins").State != model.ItemElsewhere {
				p.rootOf[n.f.Folder] = primRoot
			}
		}
		switch {
		case cur == value:
			p.out.Keep = append(p.out.Keep, model.Keep{What: "env", Path: p.tilde(t), Note: "plugin root already shared"})
			continue
		case cur != "":
			p.warn("%s already sets %s to another folder; left as is", p.tilde(t), pluginCacheEnv)
			continue
		}
		a := model.Action{Op: model.OpJSONSet, File: t, Via: p.viaOf(t), Covers: p.covers(t),
			Note: "shared plugin root " + value, Pre: pres(preType(t, model.TypeFile))}
		if envKeys == 0 {
			a.Pointer = "/env"
			a.Value = jobj(pluginCacheEnv, value)
		} else {
			a.Pointer = "/env/" + pluginCacheEnv
			a.Value = jstr(value)
		}
		p.add(model.StepSharing, a)
		p.touchedSet()[t] = true
		p.envTargets = append(p.envTargets, t)
		// A folder's own plugins are copied into the shared root (5.2, fourth row).
		for _, n := range p.nodesOfTarget(t) {
			if !n.primary {
				p.queueOwnPlugins(n, primRoot)
			}
		}
	}
}

func (p *planner) touchedSet() map[string]bool {
	if p.touched == nil {
		p.touched = map[string]bool{}
	}
	return p.touched
}

// queueOwnPlugins remembers the plugins a folder's own plugins/ has and the shared root lacks.
func (p *planner) queueOwnPlugins(n *fnode, primRoot string) {
	if n.isNew || n.item("plugins").State != model.ItemDir {
		return
	}
	e := p.rootEntry(n.f.PluginRoot)
	if e == nil || e.Root == primRoot {
		return
	}
	pe := p.rootEntry(primRoot)
	for _, id := range e.Plugins {
		if pe != nil && contains(pe.Plugins, id) {
			continue
		}
		p.ownPlugins = append(p.ownPlugins, ownPlugin{id: id, from: e, folder: n.f.Folder})
	}
}

type ownPlugin struct {
	id     string
	from   *model.PluginRoot
	folder string
}

// --- the wy-local plugin ------------------------------------------------------------------

func (p *planner) marketplaceDir() string {
	return filepath.Join(p.st.StateDir, "plugin", "marketplace")
}

func (p *planner) claudeEnv(n *fnode) map[string]*string {
	if n.f.Default {
		return map[string]*string{"CLAUDE_CONFIG_DIR": nil}
	}
	d := n.f.Dir
	return map[string]*string{"CLAUDE_CONFIG_DIR": &d}
}

type group struct {
	root  string
	nodes []*fnode
	entry *model.PluginRoot
	owner *fnode
}

// touchPre gives the precondition for a file a run action edits: only the first action that
// touches a path states what it expects (the others would see the first one's changes).
func (p *planner) touchPre(path string, exists bool) []model.Precondition {
	if p.touchedSet()[path] {
		return nil
	}
	p.touched[path] = true
	if exists {
		return pres(preType(path, model.TypeFile))
	}
	return pres(preAbsent(path))
}

func (p *planner) runAction(argv []string, as *fnode, touches []string, exists []bool, watch string, covers []string, note string) model.Action {
	a := model.Action{Op: model.OpRun, Argv: argv, Env: p.claudeEnv(as), Touches: touches, Covers: covers, Note: note}
	if watch != "" {
		a.Watch = []string{watch}
	}
	for i, t := range touches {
		a.Pre = append(a.Pre, p.touchPre(t, exists[i])...)
	}
	return a
}

func (p *planner) planPlugin() error {
	// The plugin and pane of the project's name before a rename are disabled first, while
	// their folders are still in the marketplace; the current plugin is then installed or
	// enabled below as for a folder that has none (rename.md 2.4).
	seenL := map[string]bool{}
	for _, n := range p.nodes {
		t := p.finalTarget(n)
		if seenL[t] {
			continue
		}
		seenL[t] = true
		ts := p.targetState(t)
		if ts == nil {
			continue
		}
		for _, id := range ts.LegacyPlugins {
			if strings.HasSuffix(id, "@skills-dir") {
				p.warn("%s has a plugin folder of the old name inside skills/ (%s); remove it by hand (it cannot be disabled)", p.tilde(t), id)
				continue
			}
			if p.c.NoPlugin {
				continue
			}
			p.warn("%s has %s from before the rename; disabling it (the current plugin replaces it)", p.tilde(t), id)
			a := p.runAction([]string{"claude", "plugin", "disable", id, "--json"}, p.nodesOfTarget(t)[0], []string{t}, []bool{ts.Exists}, "", p.covers(t), "")
			p.add(model.StepPlugin, a)
		}
	}

	// Duplicates first (5.2): exactly one watchyard plugin per folder.
	seen := map[string]bool{}
	for _, n := range p.nodes {
		t := p.finalTarget(n)
		if seen[t] {
			continue
		}
		seen[t] = true
		ts := p.targetState(t)
		if ts == nil || len(ts.WatchyardPlugins) < 2 {
			continue
		}
		keep := ts.WatchyardPlugins[0]
		if contains(ts.WatchyardPlugins, PluginID) {
			keep = PluginID
		}
		first := p.nodesOfTarget(t)[0]
		for _, id := range ts.WatchyardPlugins {
			if id == keep {
				continue
			}
			if id == "watchyard@skills-dir" {
				p.warn("%s has a watchyard plugin folder inside skills/ as well as %s; remove it by hand (it cannot be disabled)", p.tilde(t), keep)
				continue
			}
			if p.c.NoPlugin {
				continue
			}
			p.warn("two watchyard plugins are enabled in %s; disabling %s and keeping %s (two copies would report every event twice)", p.tilde(t), id, keep)
			a := p.runAction([]string{"claude", "plugin", "disable", id, "--json"}, first, []string{t}, []bool{ts.Exists}, "", p.covers(t), "")
			p.add(model.StepPlugin, a)
		}
	}
	if p.c.NoPlugin {
		return nil
	}

	// Groups of folders that share one plugin root.
	var groups []*group
	byRoot := map[string]*group{}
	for _, n := range p.nodes {
		root := p.rootOf[n.f.Folder]
		if root == "" {
			root = filepath.Join(n.f.Dir, "plugins")
		}
		g := byRoot[root]
		if g == nil {
			g = &group{root: root, entry: p.rootEntry(root)}
			byRoot[root] = g
			groups = append(groups, g)
		}
		g.nodes = append(g.nodes, n)
	}
	for _, g := range groups {
		for _, n := range g.nodes {
			if g.entry != nil && g.entry.Owner == n.f.Folder {
				g.owner = n
			}
		}
		if g.owner == nil {
			for _, n := range g.nodes {
				if filepath.Join(n.f.Dir, "plugins") == g.root {
					g.owner = n
					break
				}
			}
		}
		if g.owner == nil {
			g.owner = g.nodes[0]
		}
	}

	var runs []model.Action
	needTree := false
	// Plugins of folders' own plugins/ that the shared root lacks come first.
	primRoot := p.rootOf[p.prim.f.Folder]
	if len(p.ownPlugins) > 0 {
		pg := byRoot[primRoot]
		if pg != nil {
			pe := pg.entry
			addedMarket := map[string]bool{}
			for _, op := range p.ownPlugins {
				mkt := op.id[strings.LastIndex(op.id, "@")+1:]
				src := op.from.Sources[mkt]
				if pe == nil || !contains(pe.Markets, mkt) {
					if src == "" {
						p.warn("%s: plugin %s has no known source for marketplace %s; add it by hand", op.folder, op.id, mkt)
						continue
					}
					if !addedMarket[mkt] {
						addedMarket[mkt] = true
						runs = append(runs, p.runAction([]string{"claude", "plugin", "marketplace", "add", src, "--json"}, pg.owner,
							[]string{p.finalTarget(pg.owner), filepath.Join(primRoot, "known_marketplaces.json")},
							[]bool{p.targetExists(p.finalTarget(pg.owner)), pe != nil && len(pe.Markets) > 0}, primRoot, nil,
							"copy "+op.folder+"'s own plugins into the shared root; a failure is a warning"))
					}
				}
				runs = append(runs, p.runAction([]string{"claude", "plugin", "install", op.id, "--json"}, pg.owner,
					[]string{p.finalTarget(pg.owner), filepath.Join(primRoot, "installed_plugins.json")},
					[]bool{p.targetExists(p.finalTarget(pg.owner)), pe != nil && len(pe.Plugins) > 0}, primRoot, nil,
					"from "+op.folder+"; a failure is a warning"))
			}
		}
	}

	for _, g := range groups {
		// Targets in this group that have no watchyard plugin at all.
		var need []string
		seenT := map[string]bool{}
		for _, n := range g.nodes {
			t := p.finalTarget(n)
			if seenT[t] {
				continue
			}
			seenT[t] = true
			ts := p.targetState(t)
			switch {
			case ts == nil || len(ts.WatchyardPlugins) == 0:
				need = append(need, t)
			case contains(ts.WatchyardPlugins, PluginID):
				p.out.Keep = append(p.out.Keep, model.Keep{What: "plugin", Path: p.tilde(t), Note: PluginID + " enabled"})
			default:
				p.out.Keep = append(p.out.Keep, model.Keep{What: "plugin", Path: p.tilde(t), Note: "adopted " + ts.WatchyardPlugins[0]})
			}
		}
		if len(need) == 0 {
			continue
		}
		var gcov []string
		for _, n := range g.nodes {
			gcov = append(gcov, n.f.Folder)
		}
		installed := g.entry != nil && contains(g.entry.Plugins, PluginID)
		known := g.entry != nil && contains(g.entry.Markets, MarketplaceName)
		ownerT := p.finalTarget(g.owner)
		enabledByInstall := ""
		hasKnown := g.entry != nil && len(g.entry.Markets) > 0
		hasInst := g.entry != nil && len(g.entry.Plugins) > 0
		// The marketplace of the command's name before a rename points at the same folder,
		// which now carries the current name: it is removed (with the old plugins it still
		// lists) before the current one is added (rename.md 2.4).
		if lm := brand.LegacyMarketplace(); lm != "" && g.entry != nil && contains(g.entry.Markets, lm) {
			runs = append(runs, p.runAction([]string{"claude", "plugin", "marketplace", "remove", lm, "--json"}, g.owner,
				[]string{ownerT, filepath.Join(g.root, "known_marketplaces.json"), filepath.Join(g.root, "installed_plugins.json")},
				[]bool{p.targetExists(ownerT), true, hasInst}, g.root, nil, "marketplace from before the rename"))
		}
		if !installed {
			if !known {
				runs = append(runs, p.runAction([]string{"claude", "plugin", "marketplace", "add", p.marketplaceDir(), "--json"}, g.owner,
					[]string{ownerT, filepath.Join(g.root, "known_marketplaces.json")},
					[]bool{p.targetExists(ownerT), hasKnown}, g.root, nil, ""))
				needTree = true
			}
			note := ""
			if len(gcov) > 1 {
				note = "shared settings and plugins"
			}
			runs = append(runs, p.runAction([]string{"claude", "plugin", "install", PluginID, "--json"}, g.owner,
				[]string{ownerT, filepath.Join(g.root, "installed_plugins.json")},
				[]bool{p.targetExists(ownerT), hasInst}, g.root, gcov, note))
			needTree = true
			enabledByInstall = ownerT
			if !contains(need, ownerT) {
				// The install enabled it in a settings file that already has another watchyard plugin.
				runs = append(runs, p.runAction([]string{"claude", "plugin", "disable", PluginID, "--json"}, g.owner,
					[]string{ownerT}, []bool{true}, "", p.covers(ownerT), "the folder already has "+"a watchyard plugin"))
			}
		}
		for _, t := range need {
			if t == enabledByInstall {
				continue
			}
			nodes := p.nodesOfTarget(t)
			runs = append(runs, p.runAction([]string{"claude", "plugin", "enable", PluginID, "--json"}, nodes[0],
				[]string{t}, []bool{p.targetExists(t)}, "", p.covers(t), ""))
		}
	}
	if needTree {
		dir := p.marketplaceDir()
		sum := sha256.Sum256([]byte(MarketplaceName + "\x00" + p.opt.Version + "\x00" + p.st.Wy))
		pre := preAbsent(dir)
		if p.st.Ledger != nil && p.st.Ledger.Plugin != nil && p.st.Ledger.Plugin.Marketplace != "" {
			pre = preType(dir, model.TypeDir)
		}
		p.add(model.StepPlugin, model.Action{Op: model.OpWriteTree, Path: dir, ContentSHA256: hex.EncodeToString(sum[:]),
			Note: fmt.Sprintf("watchyard %s (%s)", p.opt.Version, MarketplaceName), Pre: pres(pre)})
	}
	for _, r := range runs {
		p.add(model.StepPlugin, r)
	}
	return nil
}

func jobj(k, v string) []byte {
	kb, vb := jstr(k), jstr(v)
	return []byte("{" + string(kb) + ":" + string(vb) + "}")
}

var _ = sort.Strings
