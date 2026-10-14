package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mraj602/switchyard/internal/setup/model"
)

const emptySettings = "{}\n"

func (p *planner) linkKind(spec model.ItemSpec) string {
	switch {
	case !p.win:
		return model.LinkSymlink
	case spec.Kind == model.KindDir:
		return model.LinkJunction
	case p.symlinks:
		return model.LinkSymlink
	}
	return model.LinkCopy
}

func (p *planner) pItemPath(name string) string { return filepath.Join(p.primDir, name) }

func (p *planner) hasRegular(it model.ItemState) bool {
	return it.State == model.ItemFile || it.State == model.ItemDir
}

// settingsWanted reports whether anything in this plan needs the primary's settings.json.
func (p *planner) statusLineWanted() bool { return !p.c.NoStatusLine }

// ensureSettings creates `{}` at the primary when Settings is on (or the status line is
// wanted), the primary has none, and no folder has one to promote (5.1).
func (p *planner) ensureSettings() {
	pIt := p.prim.item("settings.json")
	if p.hasRegular(pIt) || p.settingsCreated {
		return
	}
	for _, o := range p.others() {
		if p.hasRegular(o.item("settings.json")) {
			return // promoted (or reported) by the sharing loop
		}
	}
	needed := p.statusLineWanted() || (p.share.On[model.SwitchSettings] && len(p.others()) > 0)
	if !needed {
		return
	}
	path := p.pItemPath("settings.json")
	sum := sha256.Sum256([]byte(emptySettings))
	p.add(model.StepSharing, model.Action{Op: model.OpWriteFile, Path: path, Mode: "0644",
		ContentSHA256: hex.EncodeToString(sum[:]), Note: "empty settings (the primary had none)",
		Pre: pres(preAbsent(path))})
	p.settingsCreated = true
	p.touchedSet()[path] = true
	p.prim.f.SettingsTarget = path
	p.prim.f.Items["settings.json"] = model.ItemState{State: model.ItemFile, Size: int64(len(emptySettings)), SHA256: hex.EncodeToString(sum[:])}
}

func (p *planner) planSharing() error {
	if p.prim.f.Items == nil {
		p.prim.f.Items = map[string]model.ItemState{}
	}
	p.ensureSettings()
	for _, spec := range model.ShareItems {
		if spec.Name == "plugins" {
			continue
		}
		p.shareItem(spec)
	}
	p.sharePluginRoots()
	return nil
}

func (p *planner) resolution(n *fnode, spec model.ItemSpec) (string, bool) {
	if r, ok := p.c.Resolve[model.ResolveKey(n.f.Folder, spec.Name)]; ok {
		return r, true
	}
	r := p.c.Conflicts
	if r == "" {
		r = model.ResolveKeep
	}
	return r, false
}

func choicesFor(spec model.ItemSpec) []string {
	c := []string{model.ResolveKeep, model.ResolvePrimary, model.ResolveFolder}
	if spec.Merge != "" {
		c = append(c, model.ResolveMerge)
	}
	return c
}

func (p *planner) shareItem(spec model.ItemSpec) {
	name := spec.Name
	on := p.share.On[spec.Switch]
	others := p.others()
	pIt := p.prim.item(name)
	pHas := p.hasRegular(pIt)

	// What is already shared is always reported, switch or not (adopt, don't replace).
	for _, o := range others {
		it := o.item(name)
		switch it.State {
		case model.ItemLinked:
			p.out.Keep = append(p.out.Keep, model.Keep{What: "link", Path: p.tilde(filepath.Join(o.f.Dir, name)), Target: p.tilde(p.pItemPath(name))})
		case model.ItemElsewhere:
			p.out.Keep = append(p.out.Keep, model.Keep{What: "link-elsewhere", Path: p.tilde(filepath.Join(o.f.Dir, name)), Target: it.Target, Note: "shared elsewhere"})
		case model.ItemCopy:
			p.out.Keep = append(p.out.Keep, model.Keep{What: "copy", Path: p.tilde(filepath.Join(o.f.Dir, name))})
		}
	}
	if !on {
		for _, o := range others {
			if p.share.ExplicitOff[spec.Switch] && o.item(name).State == model.ItemLinked {
				p.warn("%s: un-sharing %s is not supported yet; the link is kept", o.f.Folder, name)
			}
		}
		return
	}

	if !pHas {
		p.promote(spec, others)
		return
	}
	for _, o := range others {
		it := o.item(name)
		path := filepath.Join(o.f.Dir, name)
		target := p.pItemPath(name)
		switch it.State {
		case model.ItemAbsent, "":
			p.add(model.StepSharing, model.Action{Op: model.OpLink, Path: path, Target: target, Kind: p.linkKind(spec),
				Pre: pres(preAbsent(path), preItemType(target, pIt))})
			o.links[name] = true
		case model.ItemLinked, model.ItemElsewhere, model.ItemCopy:
			// kept above
		case model.ItemFile, model.ItemDir:
			if it.Compare == model.CompareSame {
				pre, _ := preItem(path, it)
				p.add(model.StepSharing, model.Action{Op: model.OpReplaceWithLink, Path: path, Target: target, Kind: p.linkKind(spec),
					Pre: pres(pre, preItemType(target, pIt))})
				o.links[name] = true
				continue
			}
			p.conflict(spec, o, it, pIt)
		default:
			p.warn("%s/%s is not a regular file, folder or link; left alone", o.f.Folder, name)
		}
	}
}

func preItemType(path string, it model.ItemState) model.Precondition {
	if it.State == model.ItemDir {
		return preType(path, model.TypeDir)
	}
	return preType(path, model.TypeFile)
}

// promote handles a primary that lacks the item (5.1): nothing to share, or one copy moved
// into the primary and the rest linked, when every folder that has one agrees.
func (p *planner) promote(spec model.ItemSpec, others []*fnode) {
	name := spec.Name
	var cands []*fnode
	for _, o := range others {
		if p.hasRegular(o.item(name)) {
			cands = append(cands, o)
		}
	}
	if len(cands) == 0 {
		p.out.Keep = append(p.out.Keep, model.Keep{What: "nothing-to-share", Path: name})
		return
	}
	for _, o := range cands[1:] {
		if o.item(name).Compare != model.CompareSame {
			p.warn("%s differs between folders and %s has none; not shared (resolve by hand or share one first)", name, p.prim.f.Folder)
			return
		}
	}
	first := cands[0]
	fpath := filepath.Join(first.f.Dir, name)
	ppath := p.pItemPath(name)
	fit := first.item(name)
	pre, _ := preItem(fpath, fit)
	p.add(model.StepSharing, model.Action{Op: model.OpMove, From: fpath, To: ppath,
		Note: fmt.Sprintf("promote %s from %s into %s", name, first.f.Folder, p.prim.f.Folder),
		Pre:  pres(pre, preAbsent(ppath))})
	p.add(model.StepSharing, model.Action{Op: model.OpLink, Path: fpath, Target: ppath, Kind: p.linkKind(spec),
		Pre: pres(preAbsent(fpath))})
	first.links[name] = true
	for _, o := range cands[1:] {
		path := filepath.Join(o.f.Dir, name)
		pre, _ := preItem(path, o.item(name))
		p.add(model.StepSharing, model.Action{Op: model.OpReplaceWithLink, Path: path, Target: ppath, Kind: p.linkKind(spec),
			Pre: pres(pre, preItemType(ppath, fit))})
		o.links[name] = true
	}
	for _, o := range others {
		if o.item(name).State == model.ItemAbsent || o.item(name).State == "" {
			path := filepath.Join(o.f.Dir, name)
			p.add(model.StepSharing, model.Action{Op: model.OpLink, Path: path, Target: ppath, Kind: p.linkKind(spec),
				Pre: pres(preAbsent(path))})
			o.links[name] = true
		}
	}
	// The item now exists in the primary for everything after this.
	p.prim.f.Items[name] = fit
}

// conflict records an item that differs from the primary and plans its resolution (5.3).
func (p *planner) conflict(spec model.ItemSpec, o *fnode, it, pIt model.ItemState) {
	name := spec.Name
	res, explicit := p.resolution(o, spec)
	switch res {
	case model.ResolveKeep, model.ResolvePrimary, model.ResolveFolder, model.ResolveMerge:
	default:
		p.warn("unknown resolution %q for %s:%s; kept", res, o.f.Folder, name)
		res = model.ResolveKeep
	}
	if res == model.ResolveMerge && spec.Merge == "" {
		if explicit {
			p.warn("%s:%s cannot be merged; use primary or folder", o.f.Folder, name)
		}
		res = model.ResolveKeep
	}
	p.out.Conflicts = append(p.out.Conflicts, model.Conflict{Folder: o.f.Folder, Item: name, Resolution: res, Choices: choicesFor(spec)})

	path := filepath.Join(o.f.Dir, name)
	target := p.pItemPath(name)
	fpre, _ := preItem(path, it)
	ppre, _ := preItem(target, pIt)
	switch res {
	case model.ResolveKeep:
		// left as is: reported as "separate"
	case model.ResolvePrimary:
		p.add(model.StepSharing, model.Action{Op: model.OpReplaceWithLink, Path: path, Target: target, Kind: p.linkKind(spec),
			Note: "primary's " + name + " wins; " + o.f.Folder + "'s is saved in the backup set",
			Pre:  pres(fpre, preItemType(target, pIt))})
		o.links[name] = true
	case model.ResolveFolder:
		// P/X goes into the backup set first, so the move never lands on an existing path.
		p.add(model.StepSharing, model.Action{Op: model.OpMoveToBackup, Path: target,
			Note: "the primary's " + name + " is saved in the backup set",
			Pre:  pres(ppre)})
		p.add(model.StepSharing, model.Action{Op: model.OpMove, From: path, To: target,
			Note: o.f.Folder + "'s " + name + " becomes the primary's",
			Pre:  pres(fpre, preAbsent(target))})
		p.add(model.StepSharing, model.Action{Op: model.OpLink, Path: path, Target: target, Kind: p.linkKind(spec),
			Pre: pres(preAbsent(path))})
		o.links[name] = true
	case model.ResolveMerge:
		if spec.Merge == "lines" {
			p.add(model.StepSharing, model.Action{Op: model.OpMergeLines, Path: target, From: path,
				Note: "union of lines by timestamp", Pre: pres(preItemType(target, pIt), fpre)})
			p.add(model.StepSharing, model.Action{Op: model.OpReplaceWithLink, Path: path, Target: target, Kind: p.linkKind(spec),
				Pre: pres(fpre, preItemType(target, pIt))})
			o.links[name] = true
			return
		}
		if it.State != model.ItemDir || pIt.State != model.ItemDir || it.Diff == nil {
			p.warn("%s:%s cannot be merged (not two folders); kept", o.f.Folder, name)
			return
		}
		for _, rel := range it.Diff.Missing {
			from := filepath.Join(path, filepath.FromSlash(rel))
			to := filepath.Join(target, filepath.FromSlash(rel))
			p.add(model.StepSharing, model.Action{Op: model.OpMove, From: from, To: to,
				Note: "merge " + name + "/" + rel, Pre: pres(preAbsent(to))})
		}
		if len(it.Diff.Differ) > 0 {
			p.warn("%s:%s not linked: %s differ in both folders", o.f.Folder, name, strings.Join(it.Diff.Differ, ", "))
			return
		}
		p.add(model.StepSharing, model.Action{Op: model.OpReplaceWithLink, Path: path, Target: target, Kind: p.linkKind(spec),
			Note: "after merging", Pre: pres(preType(path, model.TypeDir), preType(target, model.TypeDir))})
		o.links[name] = true
	}
}
