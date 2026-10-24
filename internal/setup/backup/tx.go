package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/setup/model"
)

// SaveMode says how Begin keeps the original of a path.
type SaveMode int

// Save modes.
const (
	// SaveNone records the state only (links carry their raw target; absent needs nothing).
	SaveNone SaveMode = iota
	// SaveCopy copies a regular file's bytes into files/NNNNNN (the file stays in place).
	SaveCopy
	// SaveReserve reserves a location (files/ or trees/) the original will be *moved* to by
	// the action itself (Tx.SaveTo returns it). For a link or an absent path nothing is reserved.
	SaveReserve
)

// SavedMovedPrefix marks PathState.Saved values that are not inside the set: "moved:" then
// one or more candidate absolute paths separated by newlines. The original now lives at the
// first candidate that exists; undo renames it back.
const SavedMovedPrefix = "moved:"

// PathSpec describes one path an action touches.
type PathSpec struct {
	Path string
	Via  []string
	Save SaveMode
	// Meta records metadata only: no hash, never saved. Forced for a `.claude.json`.
	Meta bool
	// Keys are RFC 6901 pointers edited in the file: their current values are saved to
	// keys/NNNNNN.json and undo reverts them one by one.
	Keys []string
	// SavedAs sets Before.Saved verbatim (a SavedMovedPrefix value) instead of saving.
	SavedAs string
	// Intended is what the action will leave at Path, when known before it runs (see
	// model.PathEntry.Intended). Nil means unknown.
	Intended *model.PathState
	// Intend gives, per pointer of Keys, the value the action will write (compact JSON);
	// a nil value means the action deletes the key. A pointer not in the map is unknown.
	Intend map[string]json.RawMessage
}

// Spec is the input of Begin.
type Spec struct {
	// Name goes to Entry.Name (service name, "timer").
	Name  string
	Paths []PathSpec
	// Watch lists folders a `run` may create things in; their listing is saved so the
	// created paths can be found (and removed on undo, even after a crash).
	Watch []string
	// Expect limits what undo removes of the paths created under Watch to those with one of
	// these names as a path element below the watched root (plus the new folders above
	// them), for example "wy-local" for the plugin commands. Empty means every created path
	// counts after a finished run, and none after an interrupted one.
	Expect []string
}

// Tx is one journaled action in progress.
type Tx struct {
	s      *Set
	idx    int
	seq    int
	saveTo map[string]string
}

// Begin saves the originals described by spec, appends the entry with phase "intent" and
// writes the manifest (fsynced) before returning: the caller then performs the action and
// calls Finish.
func (s *Set) Begin(actionID string, op model.Op, spec Spec) (*Tx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := len(s.m.Entries) + 1
	e := model.Entry{Seq: seq, Action: actionID, Op: op, Phase: model.PhaseIntent, Name: spec.Name, Created: []string{}}
	tx := &Tx{s: s, seq: seq, saveTo: map[string]string{}}
	for _, ps := range spec.Paths {
		pe, err := s.begin1(tx, ps)
		if err != nil {
			return nil, err
		}
		e.Paths = append(e.Paths, pe)
	}
	if len(spec.Watch) > 0 {
		list, err := listWatch(spec.Watch)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(watchFile{Roots: spec.Watch, Paths: list, Expect: spec.Expect})
		if err := os.WriteFile(s.watchPath(seq), b, 0o600); err != nil {
			return nil, err
		}
	}
	if e.Paths == nil {
		e.Paths = []model.PathEntry{}
	}
	s.m.Entries = append(s.m.Entries, e)
	tx.idx = len(s.m.Entries) - 1
	if err := s.save(); err != nil {
		s.m.Entries = s.m.Entries[:tx.idx]
		return nil, err
	}
	return tx, nil
}

func (s *Set) begin1(tx *Tx, ps PathSpec) (model.PathEntry, error) {
	meta := ps.Meta || IsStateFile(ps.Path)
	var (
		before model.PathState
		err    error
	)
	if meta {
		before, err = CaptureMeta(ps.Path)
	} else {
		before, err = Capture(ps.Path)
	}
	if err != nil {
		return model.PathEntry{}, err
	}
	pe := model.PathEntry{Path: ps.Path, Via: ps.Via, Before: before}
	if ps.Intended != nil {
		in := *ps.Intended
		pe.Intended = &in
	}
	switch ps.Save {
	case SaveCopy:
		if meta {
			break
		}
		switch before.Type {
		case model.TypeFile:
			rel := "files/" + objName(s.nextObj())
			if err := copyFile(ps.Path, s.Abs(rel), 0o600); err != nil {
				return pe, fmt.Errorf("backup: save %s: %w", ps.Path, err)
			}
			pe.Before.Saved = rel
		case model.TypeDir:
			return pe, fmt.Errorf("backup: %s is a folder; it can only be moved into the set", ps.Path)
		}
	case SaveReserve:
		switch before.Type {
		case model.TypeFile:
			pe.Before.Saved = "files/" + objName(s.nextObj())
			tx.saveTo[ps.Path] = s.Abs(pe.Before.Saved)
		case model.TypeDir:
			pe.Before.Saved = "trees/" + objName(s.nextObj())
			tx.saveTo[ps.Path] = s.Abs(pe.Before.Saved)
		}
	}
	if ps.SavedAs != "" {
		pe.Before.Saved = ps.SavedAs
	}
	for _, ptr := range ps.Keys {
		raw, present, err := ReadPointer(ps.Path, ptr)
		if err != nil {
			return pe, fmt.Errorf("backup: read %s %s: %w", ps.Path, ptr, err)
		}
		kc := model.KeyChange{Pointer: ptr, BeforePresent: present}
		if v, ok := ps.Intend[ptr]; ok {
			kc.Intended = true
			if v != nil {
				kc.AfterSHA256 = SHA256Hex(compactJSON(v))
			}
		}
		if present {
			kc.Saved = "keys/" + objName(s.nextObj()) + ".json"
			if err := os.WriteFile(s.Abs(kc.Saved), raw, 0o600); err != nil {
				return pe, err
			}
		}
		pe.Keys = append(pe.Keys, kc)
	}
	return pe, nil
}

// SaveTo returns the absolute location reserved for path's original by SaveReserve ("" when
// nothing was reserved: the path was a link or absent).
func (t *Tx) SaveTo(path string) string { return t.saveTo[path] }

// Seq is the entry's sequence number.
func (t *Tx) Seq() int { return t.seq }

// Edit lets the caller adjust the entry before Finish (for example to add paths found only
// after the action ran). Nothing is written until Finish.
func (t *Tx) Edit(f func(e *model.Entry)) {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	f(&t.s.m.Entries[t.idx])
}

// CreatedSince lists the paths under the Spec.Watch folders that did not exist at Begin,
// deepest first.
func (t *Tx) CreatedSince() ([]string, error) { return t.s.createdSince(t.seq) }

// Finish records the real state of every path as `after` (and the hash of every recorded
// key), the created paths, sets phase "done" and writes the manifest. Call it after a failed
// action too: rollback then sees what is really on disk.
func (t *Tx) Finish(created []string) error {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	e := &t.s.m.Entries[t.idx]
	for i := range e.Paths {
		pe := &e.Paths[i]
		var (
			after model.PathState
			err   error
		)
		if IsStateFile(pe.Path) || pe.Before.SHA256 == "" && pe.Before.Type == model.TypeFile {
			after, err = CaptureMeta(pe.Path)
		} else {
			after, err = Capture(pe.Path)
		}
		if err != nil {
			return err
		}
		if after.Type == model.TypeDir && treeOp(e.Op) && !isAncestor(e.Paths, pe.Path) {
			// A folder the action wrote (a copied tree, the marketplace): its tree hash, so
			// undo removes it only while it holds nothing but what wy wrote.
			if h, err := TreeHash(pe.Path); err == nil {
				after.SHA256 = h
			}
		}
		pe.After = &after
		for k := range pe.Keys {
			raw, present, err := ReadPointer(pe.Path, pe.Keys[k].Pointer)
			if err != nil || !present {
				pe.Keys[k].AfterSHA256 = ""
				continue
			}
			pe.Keys[k].AfterSHA256 = SHA256Hex(raw)
		}
	}
	if created != nil {
		e.Created = created
	}
	if len(e.Created) > 0 {
		// The state of every created path right after the run: undo leaves one that
		// changed since (7.3).
		states := map[string]model.PathState{}
		for _, c := range e.Created {
			if st, err := Capture(c); err == nil && st.Type != model.TypeAbsent {
				states[c] = st
			}
		}
		b, _ := json.Marshal(states)
		if err := os.WriteFile(t.s.createdPath(e.Seq), b, 0o600); err != nil {
			return err
		}
	}
	e.Phase = model.PhaseDone
	return t.s.save()
}

// treeOp reports whether op may leave a folder tree at its path (a copy, the marketplace).
func treeOp(op model.Op) bool {
	switch op {
	case model.OpLink, model.OpReplaceWithLink, model.OpWriteTree:
		return true
	}
	return false
}

// isAncestor reports whether p is a folder above another path of the same entry (a parent
// the action created on the way).
func isAncestor(paths []model.PathEntry, p string) bool {
	for _, o := range paths {
		if o.Path != p && strings.HasPrefix(o.Path, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func compactJSON(v []byte) []byte {
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return v
	}
	return b.Bytes()
}

func (s *Set) createdPath(seq int) string {
	return filepath.Join(s.Dir, "watch", fmt.Sprintf("%06d.created.json", seq))
}

// createdStates returns the states recorded by Finish for the created paths of entry seq.
func (s *Set) createdStates(seq int) map[string]model.PathState {
	b, err := os.ReadFile(s.createdPath(seq))
	if err != nil {
		return nil
	}
	var m map[string]model.PathState
	_ = json.Unmarshal(b, &m)
	return m
}

type watchFile struct {
	Roots  []string `json:"roots"`
	Paths  []string `json:"paths"`
	Expect []string `json:"expect,omitempty"`
}

func (s *Set) watchPath(seq int) string {
	return filepath.Join(s.Dir, "watch", fmt.Sprintf("%06d.json", seq))
}

// listWatch lists every path under roots (links resolved at the root only), absolute.
func listWatch(roots []string) ([]string, error) {
	var out []string
	for _, r := range roots {
		real, err := filepath.EvalSymlinks(r)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		err = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			out = append(out, p)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Set) readWatch(seq int) (*watchFile, error) {
	b, err := os.ReadFile(s.watchPath(seq))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var w watchFile
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// expected keeps, of created paths, those the entry's Expect names allow (see Spec.Expect)
// and the new folders above them. ok is false when the entry recorded no Expect.
func (w *watchFile) expected(created []string) (out []string, ok bool) {
	if w == nil || len(w.Expect) == 0 {
		return nil, false
	}
	names := map[string]bool{}
	for _, n := range w.Expect {
		names[n] = true
	}
	var roots []string
	for _, r := range w.Roots {
		if real, err := filepath.EvalSymlinks(r); err == nil {
			roots = append(roots, real)
		}
		roots = append(roots, r)
	}
	match := func(p string) bool {
		for _, r := range roots {
			rel, err := filepath.Rel(r, p)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			for _, el := range strings.Split(filepath.ToSlash(rel), "/") {
				if names[el] {
					return true
				}
			}
		}
		return false
	}
	keep := map[string]bool{}
	for _, p := range created {
		if match(p) {
			keep[p] = true
		}
	}
	for _, p := range created {
		for k := range keep {
			if strings.HasPrefix(k, p+string(filepath.Separator)) {
				keep[p] = true
				break
			}
		}
	}
	for _, p := range created {
		if keep[p] {
			out = append(out, p)
		}
	}
	return out, true
}

// createdSince diffs the watch folders of entry seq against the listing saved at its intent.
func (s *Set) createdSince(seq int) ([]string, error) {
	wp, err := s.readWatch(seq)
	if err != nil || wp == nil {
		return nil, err
	}
	w := *wp
	was := make(map[string]struct{}, len(w.Paths))
	for _, p := range w.Paths {
		was[p] = struct{}{}
	}
	now, err := listWatch(w.Roots)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range now {
		if _, ok := was[p]; !ok {
			out = append(out, p)
		}
	}
	// deepest first: longer paths first, so children precede parents
	sort.Slice(out, func(i, j int) bool {
		di, dj := strings.Count(out[i], string(filepath.Separator)), strings.Count(out[j], string(filepath.Separator))
		if di != dj {
			return di > dj
		}
		return out[i] < out[j]
	})
	return out, nil
}

// MissingAncestors lists the folders above path that do not exist yet, outermost first,
// so a caller can journal each as a created folder before MkdirAll.
func MissingAncestors(path string) []string {
	var out []string
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
		out = append([]string{d}, out...)
		if filepath.Dir(d) == d {
			break
		}
	}
	return out
}
