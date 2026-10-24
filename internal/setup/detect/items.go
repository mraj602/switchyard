package detect

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/mraj602/watchyard/internal/setup/model"
)

// hashLimit is the size under which a file's SHA-256 is recorded at detect time.
const hashLimit = 1 << 20

type rawItem struct {
	st       model.ItemState
	resolved string // real path after following links ("" if absent or dangling)
	regular  bool   // regular file after following links
	dir      bool   // directory after following links
}

// detectItems is step 1.3 (and the link-guard baseline of 7.5).
func (x *detector) detectItems() {
	folders := x.st.Folders
	raw := make([]map[string]*rawItem, len(folders))
	pi := -1
	for i := range folders {
		if folders[i].Primary {
			pi = i
		}
	}
	// The primary first: every other folder's links are classified against its items.
	pResolved := map[string]string{}
	if pi >= 0 {
		raw[pi] = x.rawItems(folders[pi].Dir, nil)
		for name, it := range raw[pi] {
			if it.resolved != "" {
				pResolved[name] = it.resolved
			}
		}
	}
	for i := range folders {
		if i != pi {
			raw[i] = x.rawItems(folders[i].Dir, pResolved)
		}
	}

	// The reference of the comparison: the primary's item, else (nothing to link to yet)
	// the first folder that has a regular one, so the planner can tell if a promote is safe.
	for _, spec := range model.ShareItems {
		refPath, refIdx := "", -1
		if pi >= 0 && raw[pi][spec.Name].resolved != "" {
			refPath, refIdx = raw[pi][spec.Name].resolved, pi
		} else {
			for i := range folders {
				if it := raw[i][spec.Name]; i != pi && it != nil && (it.st.State == model.ItemFile || it.st.State == model.ItemDir) {
					refPath, refIdx = it.resolved, i
					break
				}
			}
		}
		for i := range folders {
			it := raw[i][spec.Name]
			if i == refIdx || i == pi || (it.st.State != model.ItemFile && it.st.State != model.ItemDir) || refPath == "" {
				continue
			}
			x.compare(spec, it, folders[i].Dir, refPath)
		}
	}
	for i := range folders {
		folders[i].Items = map[string]model.ItemState{}
		for name, it := range raw[i] {
			folders[i].Items[name] = it.st
		}
	}
	x.recordLinks()
}

// rawItems classifies every shareable item of one folder. pResolved is nil for the primary.
func (x *detector) rawItems(dir string, pResolved map[string]string) map[string]*rawItem {
	out := map[string]*rawItem{}
	for _, spec := range model.ShareItems {
		out[spec.Name] = x.rawItem(filepath.Join(dir, spec.Name), spec.Name, pResolved)
	}
	return out
}

func (x *detector) rawItem(path, name string, pResolved map[string]string) *rawItem {
	li, err := os.Lstat(path)
	if err != nil {
		return &rawItem{st: model.ItemState{State: model.ItemAbsent}}
	}
	it := &rawItem{}
	switch {
	case li.Mode()&fs.ModeSymlink != 0:
		raw, _ := os.Readlink(path)
		res, err := filepath.EvalSymlinks(path)
		if err != nil {
			it.st = model.ItemState{State: model.ItemOther, LinkRaw: raw}
			return it
		}
		fi, err := os.Stat(res)
		if err != nil {
			it.st = model.ItemState{State: model.ItemOther, LinkRaw: raw}
			return it
		}
		it.resolved = res
		it.regular, it.dir = fi.Mode().IsRegular(), fi.IsDir()
		it.st = model.ItemState{LinkRaw: raw, Target: x.tilde(res)}
		switch {
		case pResolved == nil: // the primary's own link (dotfiles): it counts as the item itself
			it.st.State = kindOf(it)
		case pResolved[name] == res:
			it.st.State = model.ItemLinked
		default:
			it.st.State = model.ItemElsewhere
		}
		if it.regular {
			x.fillFile(&it.st, res, fi)
		}
	case li.Mode().IsRegular():
		it.resolved, it.regular = path, true
		it.st = model.ItemState{State: model.ItemFile}
		x.fillFile(&it.st, path, li)
	case li.IsDir():
		it.resolved, it.dir = path, true
		it.st = model.ItemState{State: model.ItemDir}
	default:
		it.st = model.ItemState{State: model.ItemOther}
	}
	return it
}

func kindOf(it *rawItem) string {
	if it.dir {
		return model.ItemDir
	}
	return model.ItemFile
}

func (x *detector) fillFile(st *model.ItemState, path string, fi fs.FileInfo) {
	st.Size = fi.Size()
	if fi.Size() < hashLimit {
		if h, err := hashFile(x.fsys, path); err == nil {
			st.SHA256 = h
		}
	}
}

// compare fills Compare (and Diff) of a regular file or directory against the reference.
func (x *detector) compare(spec model.ItemSpec, it *rawItem, dir, ref string) {
	fi, err := os.Lstat(ref)
	if err != nil {
		it.st.Compare = model.CompareUnknown
		return
	}
	var same bool
	switch {
	case it.regular && fi.Mode().IsRegular():
		if it.st.Size != fi.Size() {
			break
		}
		same, err = sameBytes(x.fsys, it.resolved, ref)
	case it.dir && fi.IsDir():
		same, err = sameEntry(x.fsys, it.resolved, ref)
	}
	if err != nil {
		it.st.Compare = model.CompareUnknown
		x.warn = append(x.warn, "could not compare "+x.tilde(filepath.Join(dir, spec.Name))+": "+err.Error())
		return
	}
	if same {
		it.st.Compare = model.CompareSame
		return
	}
	it.st.Compare = model.CompareDiffers
	if it.dir && fi.IsDir() && spec.Merge == "dir" {
		depth := 1
		if spec.Name == "projects" {
			depth = 2
		}
		if d, err := diffDir(x.fsys, it.resolved, ref, depth); err == nil {
			sort.Strings(d.Missing)
			sort.Strings(d.Differ)
			it.st.Diff = d
		}
	}
}

// recordLinks fills State.Links: every symlink directly in a folder, and a folder that is
// itself a link (the baseline of the link guard, 7.5).
func (x *detector) recordLinks() {
	seen := map[string]bool{}
	add := func(p string) {
		if seen[p] {
			return
		}
		raw, err := os.Readlink(p)
		if err != nil {
			return
		}
		seen[p] = true
		x.st.Links = append(x.st.Links, model.LinkRecord{Path: p, Raw: raw, Kind: model.LinkSymlink})
	}
	for _, f := range x.st.Folders {
		if fi, err := os.Lstat(f.Dir); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			add(f.Dir)
		}
		ents, err := x.fsys.ReadDir(f.Dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.Type()&fs.ModeSymlink != 0 {
				add(filepath.Join(f.Dir, e.Name()))
			}
		}
	}
	sort.Slice(x.st.Links, func(i, j int) bool { return x.st.Links[i].Path < x.st.Links[j].Path })
}
