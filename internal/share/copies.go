package share

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// CopyGroup is one file shared by synced copies on Windows when symlinks are not allowed
// (setup.md 5.7). Paths[0] is the primary. SHA256 is the content last synced.
type CopyGroup struct {
	Name     string   `json:"name"` // base name of the item, e.g. "settings.json"
	Kind     string   `json:"kind"` // "file", or "lines" (history.jsonl: line union)
	Paths    []string `json:"paths"`
	SHA256   string   `json:"sha256"`
	Conflict bool     `json:"conflict,omitempty"` // two copies changed since the last sync
}

type copiesFile struct {
	V      int         `json:"v"`
	Groups []CopyGroup `json:"groups"`
}

func copiesPath(stateDir string) string { return filepath.Join(stateDir, "share", "copies.json") }

// CopiesPath returns `<state>/share/copies.json` (journaled by commands that forget copies).
func CopiesPath(stateDir string) string { return copiesPath(stateDir) }

// LoadCopies reads `<state>/share/copies.json` (absent = no groups).
func LoadCopies(stateDir string) ([]CopyGroup, error) {
	b, err := os.ReadFile(copiesPath(stateDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f copiesFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Groups, nil
}

func saveCopies(stateDir string, groups []CopyGroup) error {
	if err := os.MkdirAll(filepath.Dir(copiesPath(stateDir)), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(copiesFile{V: 1, Groups: groups}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(copiesPath(stateDir), append(b, '\n'), 0o600)
}

func fileSHA(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// registerCopy records that copyPath is a synced copy of primary (called after a LinkCopy).
func registerCopy(stateDir, primary, copyPath string) error {
	groups, err := LoadCopies(stateDir)
	if err != nil {
		return err
	}
	sha, err := fileSHA(primary)
	if err != nil {
		return err
	}
	kind := "file"
	if filepath.Base(primary) == "history.jsonl" {
		kind = "lines"
	}
	found := false
	for i := range groups {
		g := &groups[i]
		if g.Paths[0] != primary {
			continue
		}
		found = true
		have := false
		for _, p := range g.Paths {
			have = have || p == copyPath
		}
		if !have {
			g.Paths = append(g.Paths, copyPath)
		}
		g.SHA256, g.Conflict = sha, false
	}
	if !found {
		groups = append(groups, CopyGroup{Name: filepath.Base(primary), Kind: kind, Paths: []string{primary, copyPath}, SHA256: sha})
	}
	return saveCopies(stateDir, groups)
}

// ForgetCopies removes every registered copy path for which match is true from copies.json
// (undo of a LinkCopy, `wy account remove`, `wy uninstall`) and returns them. A group whose
// primary matches, or that is left with no copy, is dropped whole. The file is rewritten only
// when something was removed.
func ForgetCopies(stateDir string, match func(path string) bool) ([]string, error) {
	groups, err := LoadCopies(stateDir)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	var removed []string
	var keep []CopyGroup
	for _, g := range groups {
		if len(g.Paths) == 0 {
			continue
		}
		if match(g.Paths[0]) {
			removed = append(removed, g.Paths[1:]...)
			continue
		}
		paths := []string{g.Paths[0]}
		for _, p := range g.Paths[1:] {
			if match(p) {
				removed = append(removed, p)
			} else {
				paths = append(paths, p)
			}
		}
		if len(paths) < 2 {
			continue
		}
		g.Paths = paths
		keep = append(keep, g)
	}
	if len(keep) == len(groups) && len(removed) == 0 {
		return nil, nil
	}
	if keep == nil {
		keep = []CopyGroup{}
	}
	return removed, saveCopies(stateDir, keep)
}

// CopySyncResult lists what SyncCopies did, by item name; paths are never values.
type CopySyncResult struct {
	Synced    []string `json:"synced,omitempty"`    // groups where content was copied
	Conflicts []string `json:"conflicts,omitempty"` // two copies changed: nothing copied
	// Unmanaged lists copy paths still in copies.json that the ledger no longer lists as
	// copies (undone, unshared, uninstalled): they are left alone and never synced.
	Unmanaged []string `json:"unmanaged,omitempty"`
	// Errors lists groups whose sync failed ("name: error"); the other groups still synced.
	Errors []string `json:"errors,omitempty"`
}

// SyncCopies compares each synced-copy group with its last-synced hash: if only one side
// changed it is copied to the others; if two changed, nothing is copied and the group is
// marked in copies.json for `wy doctor`. Line-union groups merge instead. It writes
// copies.json only when something changed, and nothing at all when nothing did.
//
// Only copies listed in managed (the ledger's copies, setup.md 9.3) take part: a path
// copies.json still names but the ledger does not is neither read nor written. A group that
// fails to sync is reported and keeps its old hash (it is retried next time); the others go
// on, and the error returned joins every failure.
func SyncCopies(stateDir string, managed []string) (CopySyncResult, error) {
	var res CopySyncResult
	groups, err := LoadCopies(stateDir)
	if err != nil || len(groups) == 0 {
		return res, err
	}
	isManaged := map[string]bool{}
	for _, p := range managed {
		isManaged[p] = true
	}
	orig := append([]CopyGroup(nil), groups...)
	for i := range orig {
		orig[i].Paths = append([]string(nil), orig[i].Paths...)
	}
	var errs []error
	fail := func(g *CopyGroup, err error) {
		res.Errors = append(res.Errors, g.Name+": "+err.Error())
		errs = append(errs, fmt.Errorf("%s: %w", g.Name, err))
	}
	for i := range groups {
		g := &groups[i]
		if len(g.Paths) == 0 {
			continue
		}
		// The synced view: the primary and the managed copies only.
		all := g.Paths
		view := []string{all[0]}
		for _, p := range all[1:] {
			if isManaged[p] {
				view = append(view, p)
			} else {
				res.Unmanaged = append(res.Unmanaged, p)
			}
		}
		if len(view) < 2 {
			continue
		}
		g.Paths = view
		failed := false
		write := func(p string, data []byte) bool {
			if err := writeLike(p, data); err != nil {
				fail(g, err)
				failed = true
				return false
			}
			return true
		}
		shas := make([]string, len(g.Paths))
		var changed []int
		for j, p := range g.Paths {
			if sha, err := fileSHA(p); err == nil {
				shas[j] = sha
				if sha != g.SHA256 {
					changed = append(changed, j)
				}
			}
		}
		if g.Kind == "lines" {
			var inputs [][]byte
			for _, p := range g.Paths {
				if b, err := os.ReadFile(p); err == nil {
					inputs = append(inputs, b)
				}
			}
			out := MergeLinesBytes(inputs...)
			h := sha256.Sum256(out)
			sum := hex.EncodeToString(h[:])
			wrote := false
			for j, p := range g.Paths {
				if shas[j] == sum {
					continue
				}
				if len(changed) == 0 && shas[j] != "" {
					continue
				}
				if write(p, out) {
					wrote = true
				}
			}
			if (len(changed) > 0 || wrote) && !failed {
				g.SHA256 = sum
			}
			if wrote {
				res.Synced = append(res.Synced, g.Name)
			}
			g.Paths = all
			continue
		}
		var src = -1
		switch {
		case len(changed) == 0:
			// Nothing changed; refill a missing copy from the primary.
			if shas[0] != "" {
				src = 0
			}
		case len(changed) == 1:
			src = changed[0]
		default:
			same := true
			for _, j := range changed[1:] {
				same = same && shas[j] == shas[changed[0]]
			}
			if same {
				src = changed[0]
			} else {
				g.Conflict = true
				res.Conflicts = append(res.Conflicts, g.Name)
				g.Paths = all
				continue
			}
		}
		if src < 0 {
			g.Paths = all
			continue
		}
		data, err := os.ReadFile(g.Paths[src])
		if err != nil {
			fail(g, err)
			g.Paths = all
			continue
		}
		wrote := false
		for j, p := range g.Paths {
			if j == src || shas[j] == shas[src] {
				continue
			}
			if write(p, data) {
				wrote = true
			}
		}
		if !failed {
			g.Conflict = false
			if len(changed) > 0 {
				g.SHA256 = shas[src]
			}
		}
		if wrote {
			res.Synced = append(res.Synced, g.Name)
		}
		g.Paths = all
	}
	if !reflect.DeepEqual(orig, groups) {
		if err := saveCopies(stateDir, groups); err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}

// writeLike rewrites path atomically keeping its mode (0600 for a new file).
func writeLike(path string, data []byte) error {
	perm := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	return atomicfile.Write(path, data, perm)
}
