package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// Exit codes the command layer maps results to (setup.md section 4).
const (
	// ExitSkipped: something was skipped and listed (7.3).
	ExitSkipped = 5
	// ExitStack: undoing an older set while a newer one is still applied (7.3).
	ExitStack = 2
)

// ErrNothingToUndo means no set is eligible (or the id names none).
var ErrNothingToUndo = errors.New("setup: nothing to undo")

// StackError refuses to undo set ID while newer sets are still applied (exit 2).
type StackError struct {
	ID    string
	Newer []string
}

func (e *StackError) Error() string {
	return fmt.Sprintf("backup %s is older than %s, which is still applied: undo that first, or use --force",
		e.ID, strings.Join(e.Newer, ", "))
}

// Deps are the collaborators undo needs. Sharer is required (JSON key edits, junctions);
// Service and RemoveTimer are optional (service and connect entries).
type Deps struct {
	Sharer  model.Sharer
	Service model.Service
	// RemoveTimer stops and removes the flush timer a `connect` entry installed.
	RemoveTimer func(ctx context.Context) error
}

// Options tunes Undo.
type Options struct {
	// Force allows undoing an older set under newer applied ones, and moves a path that
	// changed since the run into undo-saved/ before restoring it.
	Force bool
}

// Skip is a path undo left alone.
type Skip struct {
	Seq    int    `json:"seq"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Result is what an undo (or rollback) did.
type Result struct {
	ID string `json:"id"`
	// Restored lists paths put back exactly.
	Restored []string `json:"restored,omitempty"`
	// KeysRestored lists "file pointer" pairs reverted at key level while later edits were kept.
	KeysRestored []string `json:"keys_restored,omitempty"`
	// Kept lists created folders left because they hold files wy did not create.
	Kept []string `json:"kept,omitempty"`
	// Skipped lists paths that changed since the run (exit 5).
	Skipped []Skip `json:"skipped,omitempty"`
	// Notes are human lines (the device token is not revoked, ...).
	Notes []string `json:"notes,omitempty"`
	// Status is the set's new status.
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
}

// Eligible reports whether a set can be undone without naming it (7.3).
func Eligible(i Info) bool {
	switch i.Status {
	case model.StatusApplied, model.StatusPartlyUndone:
		return true
	case model.StatusRolledBack:
		return i.Remaining > 0
	}
	return false
}

// pick resolves id ("" = newest eligible) and applies the stack rule.
func pick(stateDir, id string, force bool) (string, error) {
	list, err := List(stateDir)
	if err != nil {
		return "", err
	}
	target := -1
	if id == "" {
		for i := len(list) - 1; i >= 0; i-- {
			if Eligible(list[i]) {
				target = i
				break
			}
		}
	} else {
		for i := range list {
			if list[i].ID == id {
				target = i
			}
		}
	}
	if target < 0 {
		return "", ErrNothingToUndo
	}
	if st := list[target].Status; st == model.StatusUndone || (st == model.StatusRolledBack && list[target].Remaining == 0) {
		return "", fmt.Errorf("setup: backup %s is already %s", list[target].ID, st)
	}
	var newer []string
	for _, n := range list[target+1:] {
		if n.Status == model.StatusApplied || n.Status == model.StatusPartlyUndone ||
			n.Status == model.StatusInProgress || (n.Status == model.StatusRolledBack && n.Remaining > 0) {
			newer = append(newer, n.ID)
		}
	}
	if len(newer) > 0 && !force {
		return "", &StackError{ID: list[target].ID, Newer: newer}
	}
	return list[target].ID, nil
}

// UndoPlan resolves which set Undo(id) would act on and returns its plan: one line per
// entry, newest first (7.3: undo always prints its own plan first).
func UndoPlan(stateDir, id string, o Options) (string, []string, error) {
	rid, err := pick(stateDir, id, o.Force)
	if err != nil {
		return "", nil, err
	}
	s, err := Open(stateDir, rid)
	if err != nil {
		return "", nil, err
	}
	var lines []string
	m := s.Manifest()
	for i := len(m.Entries) - 1; i >= 0; i-- {
		e := m.Entries[i]
		if e.Phase == model.PhaseReverted {
			continue
		}
		var ps []string
		for _, p := range e.Paths {
			ps = append(ps, p.Path)
		}
		lines = append(lines, fmt.Sprintf("undo #%d %-17s %s%s", e.Seq, e.Op, strings.Join(ps, ", "),
			map[bool]string{true: "  (interrupted: maybe done)", false: ""}[e.Phase == model.PhaseIntent]))
	}
	return rid, lines, nil
}

// Undo reverses a set (setup.md 7.3). id "" means the newest eligible set. It never asks:
// the caller prints UndoPlan and confirms first.
func Undo(ctx context.Context, stateDir, id string, d Deps, o Options) (Result, error) {
	rid, err := pick(stateDir, id, o.Force)
	if err != nil {
		return Result{}, err
	}
	s, err := Open(stateDir, rid)
	if err != nil {
		return Result{}, err
	}
	return UndoSet(ctx, s, d, o, false)
}

// MarkApplied marks an in-progress set as applied (7.6: "mark it as applied").
func MarkApplied(stateDir, id string) error {
	s, err := Open(stateDir, id)
	if err != nil {
		return err
	}
	return s.SetStatus(model.StatusApplied)
}

// UndoSet reverses the entries of an open set, newest first. With rollback true (7.4, a
// failed apply or 7.6 recovery) the final status is rolled-back instead of undone/partly-undone.
func UndoSet(ctx context.Context, s *Set, d Deps, o Options, rollback bool) (Result, error) {
	res, err := UndoSince(ctx, s, d, o, 1)
	if err != nil {
		return res, err
	}
	remaining := 0
	for _, e := range s.Manifest().Entries {
		if e.Phase != model.PhaseReverted {
			remaining++
		}
	}
	status := model.StatusUndone
	switch {
	case rollback:
		status = model.StatusRolledBack
	case remaining > 0:
		status = model.StatusPartlyUndone
	}
	if err := s.SetStatus(status); err != nil {
		return res, err
	}
	res.Status = status
	return res, nil
}

// UndoSince reverses the entries with Seq >= minSeq (newest first) and leaves the set's
// status alone: apply uses it to drop a failed hub step while keeping the local steps.
func UndoSince(ctx context.Context, s *Set, d Deps, o Options, minSeq int) (Result, error) {
	if d.Sharer == nil {
		return Result{}, errors.New("backup: undo needs a Sharer")
	}
	u := &undoer{s: s, d: d, o: o, res: Result{ID: s.ID()}}
	m := s.Manifest()
	for i := len(m.Entries) - 1; i >= 0; i-- {
		e := m.Entries[i]
		if e.Phase == model.PhaseReverted || e.Seq < minSeq {
			continue
		}
		if err := ctx.Err(); err != nil {
			return u.res, err
		}
		if u.entry(ctx, e) {
			if err := s.markReverted(e.Seq); err != nil {
				return u.res, err
			}
		}
	}
	if len(u.res.Skipped) > 0 {
		u.res.ExitCode = ExitSkipped
	}
	return u.res, nil
}

func (s *Set) markReverted(seq int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.m.Entries {
		if s.m.Entries[i].Seq == seq {
			s.m.Entries[i].Phase = model.PhaseReverted
		}
	}
	return s.save()
}

type undoer struct {
	s   *Set
	d   Deps
	o   Options
	res Result
}

func (u *undoer) skip(seq int, path, reason string) {
	u.res.Skipped = append(u.res.Skipped, Skip{Seq: seq, Path: path, Reason: reason})
}

// CopyForgetter is implemented by a Sharer that registers synced copies (internal/share,
// Windows copy mode): undo of a link entry deregisters the copy at the path it restored, so
// `wy tick` stops syncing the restored original into the shared primary (5.7).
type CopyForgetter interface {
	ForgetCopy(path string) error
}

// entry undoes one entry; true when every path was handled (the entry is then reverted).
func (u *undoer) entry(ctx context.Context, e model.Entry) bool {
	clean := true
	switch e.Op {
	case model.OpService:
		if u.d.Service != nil && e.Name != "" {
			if err := u.d.Service.Uninstall(ctx, e.Name); err != nil {
				u.skip(e.Seq, e.Name, "stopping the service: "+err.Error())
				clean = false
			}
		}
	case model.OpConnect:
		if e.Name == "timer" && u.d.RemoveTimer != nil {
			if err := u.d.RemoveTimer(ctx); err != nil {
				u.skip(e.Seq, "timer", "removing the timer: "+err.Error())
				clean = false
			}
		}
		u.res.Notes = append(u.res.Notes, "the device token on the hub is not revoked: wy hub token revoke <name>")
	}

	fc, _ := u.d.Sharer.(CopyForgetter)
	for i := len(e.Paths) - 1; i >= 0; i-- {
		pe := e.Paths[i]
		if !u.path(ctx, e, pe) {
			clean = false
			continue
		}
		if fc != nil && (e.Op == model.OpLink || e.Op == model.OpReplaceWithLink) && !isAncestor(e.Paths, pe.Path) {
			if err := fc.ForgetCopy(pe.Path); err != nil {
				u.skip(e.Seq, pe.Path, "deregistering the synced copy: "+err.Error())
				clean = false
			}
		}
	}
	// Created paths last: a journaled file the run created (a registry) is reverted above
	// first, so the folders it made can then go when empty.
	states := u.s.createdStates(e.Seq)
	paths, emptyOnly := u.created(e)
	for _, p := range paths {
		if emptyOnly[p] {
			// not one of the Expect names: an empty folder goes (nothing is lost), anything
			// else stays without a word
			if st, err := Capture(p); err == nil && st.Type == model.TypeDir && os.Remove(p) == nil {
				u.res.Restored = append(u.res.Restored, p)
			}
			continue
		}
		if !u.removeCreated(e, p, states) {
			clean = false
		}
	}
	return clean
}

// created lists the paths a `run` entry created that undo removes (deepest first): after a
// finished run the recorded list, after an interrupted one the watched folders diffed now;
// both narrowed to the entry's Expect names when it has them (an interrupted run without
// them removes nothing: what appeared since may be anyone's). The created paths outside the
// Expect names are listed too, marked emptyOnly: they go only when they are empty folders
// (the `marketplaces/` Claude Code makes beside wy's). Paths the entry journals itself (a
// registry it touched) are left to the path-level undo, which reverts only wy's entries.
func (u *undoer) created(e model.Entry) (out []string, emptyOnly map[string]bool) {
	w, _ := u.s.readWatch(e.Seq)
	created := e.Created
	if e.Phase == model.PhaseIntent {
		created, _ = u.s.createdSince(e.Seq)
	}
	exp, ok := w.expected(created)
	if !ok && e.Phase == model.PhaseIntent {
		return nil, nil
	}
	emptyOnly = map[string]bool{}
	if ok {
		in := map[string]bool{}
		for _, p := range exp {
			in[p] = true
		}
		for _, p := range created {
			emptyOnly[p] = !in[p]
		}
	}
	own := map[string]bool{}
	for _, pe := range e.Paths {
		own[pe.Path] = true
	}
	for _, p := range created {
		if !own[p] {
			out = append(out, p)
		}
	}
	return out, emptyOnly
}

func isCredentials(p string) bool { return filepath.Base(p) == ".credentials.json" }

// removeCreated removes a path an entry created (never a folder holding unknown files,
// never a login, never a file changed since the run); false means skipped.
func (u *undoer) removeCreated(e model.Entry, p string, states map[string]model.PathState) bool {
	if isCredentials(p) {
		u.res.Kept = append(u.res.Kept, p)
		return true
	}
	st, err := Capture(p)
	if err != nil || st.Type == model.TypeAbsent {
		return true
	}
	if st.Type == model.TypeDir {
		removeLoginlessState(p)
		if err := os.Remove(p); err != nil {
			u.res.Kept = append(u.res.Kept, p)
			return true
		}
		u.res.Restored = append(u.res.Restored, p)
		return true
	}
	if rec, ok := states[p]; ok && !StateEq(st, rec) {
		u.skip(e.Seq, p, "created by the run and changed since; left as is")
		return false
	}
	if err := os.Remove(p); err != nil {
		u.skip(e.Seq, p, err.Error())
		return false
	}
	u.res.Restored = append(u.res.Restored, p)
	return true
}

// removeLoginlessState empties a folder wy created of what Claude Code puts in any new
// folder on its first run of a command (for example the `claude auth status` setup runs for
// every folder), so that it does not make undo keep the folder: a `.claude.json` without an
// oauthAccount and an empty `sessions/`. It does so only when they are the folder's only
// entries; anything else means the folder is in use and nothing is touched (7.3).
func removeLoginlessState(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	state, sessions := false, false
	for _, e := range ents {
		switch {
		case e.Name() == ".claude.json" && e.Type().IsRegular():
			state = true
		case e.Name() == "sessions" && e.IsDir():
			sub, err := os.ReadDir(filepath.Join(dir, "sessions"))
			if err != nil || len(sub) > 0 {
				return
			}
			sessions = true
		default:
			return
		}
	}
	if state {
		f := filepath.Join(dir, ".claude.json")
		b, err := os.ReadFile(f)
		if err != nil {
			return
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			return
		}
		if v, ok := m["oauthAccount"]; ok && string(bytes.TrimSpace(v)) != "null" && string(bytes.TrimSpace(v)) != "{}" {
			return
		}
		if os.Remove(f) != nil {
			return
		}
	}
	if sessions {
		_ = os.Remove(filepath.Join(dir, "sessions"))
	}
}

// wantTreeHash is the tree hash a folder at pe.Path must have to be what wy wrote ("" when
// none was recorded).
func wantTreeHash(pe model.PathEntry) string {
	if pe.After != nil && pe.After.Type == model.TypeDir && pe.After.SHA256 != "" {
		return pe.After.SHA256
	}
	if pe.Intended != nil && pe.Intended.Type == model.TypeDir && pe.Intended.SHA256 != "" {
		return pe.Intended.SHA256
	}
	return ""
}

// intendedEq is StateEq against an Intended state, where an empty link target matches any
// link of that type (a junction's raw form is only known once it exists).
func intendedEq(cur, in model.PathState) bool {
	if (in.Type == model.TypeSymlink || in.Type == model.TypeJunction) && in.Target == "" {
		return cur.Type == in.Type
	}
	return StateEq(cur, in)
}

// path undoes one PathEntry; false means skipped.
func (u *undoer) path(ctx context.Context, e model.Entry, pe model.PathEntry) bool {
	meta := IsStateFile(pe.Path)
	cur, err := capture(pe.Path, meta)
	if err != nil {
		u.skip(e.Seq, pe.Path, err.Error())
		return false
	}
	if meta && len(pe.Keys) > 0 {
		return u.keyRevert(e, pe, cur)
	}
	if cur.Type == model.TypeDir {
		if want := wantTreeHash(pe); want != "" {
			if h, err := TreeHash(pe.Path); err == nil {
				cur.SHA256 = h
			}
		}
	}
	// A saved tree still in the set means the folder at path is a replacement, however much
	// it looks like the original (a folder state has no hash): restore it.
	stale := pe.Before.Type == model.TypeDir && pe.Before.Saved != "" &&
		!strings.HasPrefix(pe.Before.Saved, SavedMovedPrefix) && exists(u.s.Abs(pe.Before.Saved))
	if !stale && StateEq(cur, pe.Before) {
		return true
	}
	// What is there is what wy left: the recorded after-state, or for an interrupted entry
	// (no after-state) the state it meant to leave. Anything else may hold later edits.
	ours := false
	switch {
	case pe.After != nil:
		ours = StateEq(cur, *pe.After)
	case pe.Intended != nil:
		ours = intendedEq(cur, *pe.Intended)
	}
	if e.Op == model.OpService && cur.Type == model.TypeAbsent {
		ours = true // the service removed its own unit
	}
	if ours {
		if err := u.restore(ctx, e, pe, cur); err != nil {
			u.skip(e.Seq, pe.Path, err.Error())
			return false
		}
		return true
	}
	if len(pe.Keys) > 0 {
		return u.keyRevert(e, pe, cur)
	}
	if e.Op == model.OpRun && runPointers(pe.Path) != nil {
		return u.runRevert(e, pe, cur)
	}
	if u.o.Force {
		dst := filepath.Join(u.s.Dir, "undo-saved", fmt.Sprintf("%06d-%s", e.Seq, filepath.Base(pe.Path)))
		if cur.Type != model.TypeAbsent {
			// the changed version must be safe in undo-saved before anything replaces it
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				u.skip(e.Seq, pe.Path, "moving to undo-saved: "+err.Error())
				return false
			}
			if err := MoveTree(pe.Path, dst); err != nil {
				u.skip(e.Seq, pe.Path, "moving to undo-saved: "+err.Error())
				return false
			}
		}
		cur = model.PathState{Type: model.TypeAbsent}
		if err := u.restore(ctx, e, pe, cur); err != nil {
			u.skip(e.Seq, pe.Path, err.Error())
			return false
		}
		u.res.Notes = append(u.res.Notes, "forced: "+pe.Path+" had changed since the run; the changed version is in "+dst)
		return true
	}
	if pe.After == nil {
		u.skip(e.Seq, pe.Path, "changed since the interrupted run (maybe by you later); left as is")
		return false
	}
	u.skip(e.Seq, pe.Path, "changed since the run")
	return false
}

// removeDir removes the folder at pe.Path that undo must take away. A parent created on the
// way, or a folder of an op that writes no trees, is removed only when empty (after the
// login-less state of a first run): kept=true otherwise. The folder an op wrote as a tree
// (a copy, the marketplace) is removed whole only when it is verified to hold just what wy
// wrote: its tree hash matched the recorded one (cur.SHA256), or it equals the tree of the
// link it replaced (an older manifest without the hash).
func (u *undoer) removeDir(e model.Entry, pe model.PathEntry, cur model.PathState) (kept bool, err error) {
	if isAncestor(e.Paths, pe.Path) || !treeOp(e.Op) {
		removeLoginlessState(pe.Path)
		if os.Remove(pe.Path) != nil {
			return true, nil
		}
		return false, nil
	}
	if cur.SHA256 == "" && !copyOfTarget(pe) {
		return false, errors.New("the folder holds files wy did not write; left as is")
	}
	return false, os.RemoveAll(pe.Path)
}

// copyOfTarget reports whether the folder at pe.Path is an unchanged copy of what the link
// it replaced pointed to.
func copyOfTarget(pe model.PathEntry) bool {
	b := pe.Before
	if (b.Type != model.TypeSymlink && b.Type != model.TypeJunction) || b.Target == "" {
		return false
	}
	t := b.Target
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(pe.Path), t)
	}
	h1, err1 := TreeHash(pe.Path)
	h2, err2 := TreeHash(t)
	return err1 == nil && err2 == nil && h1 == h2
}

// restore puts pe.Before back at pe.Path; cur is what is there now.
func (u *undoer) restore(ctx context.Context, e model.Entry, pe model.PathEntry, cur model.PathState) error {
	b := pe.Before
	if b.Type == model.TypeAbsent {
		if isCredentials(pe.Path) {
			u.res.Kept = append(u.res.Kept, pe.Path)
			return nil
		}
		switch cur.Type {
		case model.TypeAbsent:
			return nil
		case model.TypeDir:
			kept, err := u.removeDir(e, pe, cur)
			if err != nil {
				return err
			}
			if kept {
				u.res.Kept = append(u.res.Kept, pe.Path)
				return nil
			}
		default:
			if err := os.Remove(pe.Path); err != nil {
				return err
			}
		}
		u.res.Restored = append(u.res.Restored, pe.Path)
		return nil
	}
	switch cur.Type {
	case model.TypeAbsent:
	case model.TypeDir:
		kept, err := u.removeDir(e, pe, cur)
		if err != nil {
			return err
		}
		if kept {
			return errors.New("a folder holding files wy did not write is in the way")
		}
	case model.TypeFile:
		// a file over a file is replaced atomically by restoreSaved: removing it first
		// would leave nothing at the path if undo stopped in between
		if b.Type != model.TypeFile {
			if err := os.Remove(pe.Path); err != nil {
				return err
			}
		}
	default:
		if err := os.Remove(pe.Path); err != nil {
			return err
		}
	}
	switch b.Type {
	case model.TypeSymlink:
		if err := os.Symlink(b.Target, pe.Path); err != nil {
			return err
		}
	case model.TypeJunction, model.TypeCopy:
		if err := u.d.Sharer.Link(pe.Path, b.Target, model.LinkJunction); err != nil {
			return err
		}
	case model.TypeFile, model.TypeDir:
		if err := u.restoreSaved(pe.Path, b); err != nil {
			return err
		}
	default:
		return fmt.Errorf("cannot restore a %s", b.Type)
	}
	u.res.Restored = append(u.res.Restored, pe.Path)
	return nil
}

func (u *undoer) restoreSaved(path string, b model.PathState) error {
	if b.Saved == "" {
		return errors.New("nothing was saved for it")
	}
	if strings.HasPrefix(b.Saved, SavedMovedPrefix) {
		moved := false
		for _, c := range strings.Split(strings.TrimPrefix(b.Saved, SavedMovedPrefix), "\n") {
			if c == "" {
				continue
			}
			if st, err := Capture(c); err == nil && st.Type == b.Type {
				if err := MoveTree(c, path); err != nil {
					return err
				}
				moved = true
				break
			}
		}
		if !moved {
			return errors.New("the moved original is gone")
		}
	} else {
		abs := u.s.Abs(b.Saved)
		fi, err := os.Lstat(abs)
		if err != nil {
			return fmt.Errorf("saved copy missing: %w", err)
		}
		if fi.IsDir() {
			if err := MoveTree(abs, path); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(abs)
			if err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if m, err := model.ParseMode(b.Mode); err == nil && b.Mode != "" {
				mode = m
			}
			if err := atomicfile.Write(path, data, mode); err != nil {
				return err
			}
		}
	}
	if b.Mode != "" {
		if m, err := model.ParseMode(b.Mode); err == nil {
			if err := os.Chmod(path, m); err != nil {
				return err
			}
		}
	}
	if b.Type == model.TypeFile && b.MtimeNS != 0 {
		mt := time.Unix(0, b.MtimeNS)
		if err := os.Chtimes(path, mt, mt); err != nil {
			return err
		}
	}
	return nil
}

// keyRevert reverts each recorded pointer of a JSON edit, keeping every other later change.
// When the file differs from its saved original only at those pointers, and each holds what
// wy wrote, the saved original is put back whole instead (exact bytes; nothing to keep).
func (u *undoer) keyRevert(e model.Entry, pe model.PathEntry, cur model.PathState) bool {
	if cur.Type != model.TypeFile && cur.Type != model.TypeSymlink {
		u.skip(e.Seq, pe.Path, "the file is gone; keys cannot be reverted")
		return false
	}
	var ptrs []string
	for _, k := range pe.Keys {
		ptrs = append(ptrs, k.Pointer)
	}
	if !IsStateFile(pe.Path) && cur.Type == model.TypeFile && pe.Before.Type == model.TypeFile && pe.Before.Saved != "" {
		ours := true
		for _, k := range pe.Keys {
			ours = ours && u.keyIsOurs(e, pe, k)
		}
		if ours && u.confinedTo(pe, ptrs) {
			if err := u.restore(context.Background(), e, pe, cur); err != nil {
				u.skip(e.Seq, pe.Path, err.Error())
				return false
			}
			return true
		}
	}
	ok := true
	reverted := false
	for _, k := range pe.Keys {
		var want json.RawMessage
		if k.BeforePresent {
			b, err := os.ReadFile(u.s.Abs(k.Saved))
			if err != nil {
				u.skip(e.Seq, pe.Path+" "+k.Pointer, "saved value missing: "+err.Error())
				ok = false
				continue
			}
			want = b
		}
		now, present, err := ReadPointer(pe.Path, k.Pointer)
		if err != nil {
			u.skip(e.Seq, pe.Path+" "+k.Pointer, err.Error())
			ok = false
			continue
		}
		if present == k.BeforePresent && (!present || bytes.Equal(now, want)) {
			continue // already as before
		}
		if !u.keyIsOurs(e, pe, k) {
			u.skip(e.Seq, pe.Path+" "+k.Pointer, "key changed since the run")
			ok = false
			continue
		}
		var err2 error
		if k.BeforePresent {
			_, err2 = u.d.Sharer.JSONSet(pe.Path, k.Pointer, want)
		} else {
			_, err2 = u.d.Sharer.JSONDelete(pe.Path, k.Pointer)
		}
		if err2 != nil {
			u.skip(e.Seq, pe.Path+" "+k.Pointer, err2.Error())
			ok = false
			continue
		}
		reverted = true
		u.res.KeysRestored = append(u.res.KeysRestored, pe.Path+" "+k.Pointer)
	}
	if ok && reverted {
		u.res.Notes = append(u.res.Notes, "restored key, kept later edits: "+pe.Path)
	}
	return ok
}

// keyIsOurs reports whether the value at k.Pointer now is the one wy wrote (or, for an
// interrupted edit whose value was not known up front, whether it may be: key-level revert
// is then the least that can be undone). A value already equal to the original counts too.
func (u *undoer) keyIsOurs(e model.Entry, pe model.PathEntry, k model.KeyChange) bool {
	now, present, err := ReadPointer(pe.Path, k.Pointer)
	if err != nil {
		return false
	}
	if present == k.BeforePresent {
		if !present {
			return true
		}
		if b, err := os.ReadFile(u.s.Abs(k.Saved)); err == nil && bytes.Equal(now, b) {
			return true
		}
	}
	if e.Phase == model.PhaseIntent && !k.Intended {
		return true
	}
	if present {
		return k.AfterSHA256 != "" && SHA256Hex(now) == k.AfterSHA256
	}
	return k.AfterSHA256 == ""
}

// confinedTo reports whether the file at pe.Path and its saved original differ only at the
// given pointers (both parse as JSON; the values at the pointers are ignored).
func (u *undoer) confinedTo(pe model.PathEntry, ptrs []string) bool {
	before, err := os.ReadFile(u.s.Abs(pe.Before.Saved))
	if err != nil {
		return false
	}
	now, err := os.ReadFile(pe.Path)
	if err != nil {
		return false
	}
	return jsonEqualExcept(before, now, ptrs, true, false)
}

// jsonEqualExcept compares two JSON documents with the values at ptrs removed from both. With
// prune, empty objects are dropped (a parent the edit created); with registry, a document
// reduced to nothing but a "version" number counts as empty (a registry file Claude Code
// created for wy's entry alone).
func jsonEqualExcept(a, b []byte, ptrs []string, prune, registry bool) bool {
	parse := func(d []byte) (any, bool) {
		if len(bytes.TrimSpace(d)) == 0 {
			return map[string]any{}, true
		}
		dec := json.NewDecoder(bytes.NewReader(d))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) != nil {
			return nil, false
		}
		return v, true
	}
	va, ok1 := parse(a)
	vb, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for _, p := range ptrs {
		va = deletePointer(va, p)
		vb = deletePointer(vb, p)
	}
	if prune {
		va, vb = pruneEmpty(va), pruneEmpty(vb)
	}
	if registry {
		va, vb = dropLoneVersion(va), dropLoneVersion(vb)
	}
	return reflect.DeepEqual(va, vb)
}

func deletePointer(v any, ptr string) any {
	if ptr == "" || !strings.HasPrefix(ptr, "/") {
		return v
	}
	toks := strings.Split(ptr[1:], "/")
	cur := v
	for i, tok := range toks {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return v
		}
		if i == len(toks)-1 {
			delete(m, tok)
			return v
		}
		cur = m[tok]
	}
	return v
}

func pruneEmpty(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for k, x := range m {
		x = pruneEmpty(x)
		if sub, ok := x.(map[string]any); ok && len(sub) == 0 {
			delete(m, k)
			continue
		}
		m[k] = x
	}
	return m
}

func dropLoneVersion(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		if _, ok := m["version"].(json.Number); ok {
			return map[string]any{}
		}
	}
	return v
}

// runPointers returns the pointers a `run` (claude plugin ...) may have changed in a file
// (7.3: key-level for settings, entry-level for the two plugin registries); nil for others.
func runPointers(path string) []string {
	id := brand.Slug + "@" + brand.Marketplace
	switch filepath.Base(path) {
	case "settings.json":
		return []string{"/enabledPlugins/" + id, "/extraKnownMarketplaces/" + brand.Marketplace}
	case "installed_plugins.json":
		return []string{"/plugins/" + id, "/" + id}
	case "known_marketplaces.json":
		return []string{"/" + brand.Marketplace}
	}
	return nil
}

// runRevert reverts, in a file a `run` touched that is not exactly as the run left it (it
// changed since, or the run was interrupted), only the watchyard@wy-local / wy-local entries
// to what the saved original had. When everything else in the file still equals the
// original, the original is put back whole (or the file removed, when the run created it
// for wy's entries alone).
func (u *undoer) runRevert(e model.Entry, pe model.PathEntry, cur model.PathState) bool {
	if cur.Type != model.TypeFile && cur.Type != model.TypeSymlink {
		u.skip(e.Seq, pe.Path, "changed since the run")
		return false
	}
	var beforeDoc []byte
	if pe.Before.Saved != "" {
		beforeDoc, _ = os.ReadFile(u.s.Abs(pe.Before.Saved))
	}
	if now, err := os.ReadFile(pe.Path); err == nil && cur.Type == model.TypeFile {
		switch {
		case pe.Before.Type == model.TypeFile && beforeDoc != nil && jsonEqualExcept(beforeDoc, now, runPointers(pe.Path), true, false),
			pe.Before.Type == model.TypeAbsent && jsonEqualExcept(nil, now, runPointers(pe.Path), true, true):
			if err := u.restore(context.Background(), e, pe, cur); err != nil {
				u.skip(e.Seq, pe.Path, err.Error())
				return false
			}
			return true
		}
	}
	ok := true
	for _, p := range runPointers(pe.Path) {
		now, present, err := ReadPointer(pe.Path, p)
		if err != nil {
			u.skip(e.Seq, pe.Path+" "+p, err.Error())
			ok = false
			continue
		}
		var want json.RawMessage
		wantPresent := false
		if beforeDoc != nil {
			want, wantPresent, _ = PointerIn(beforeDoc, p)
		}
		var err2 error
		switch {
		case wantPresent && (!present || !bytes.Equal(now, want)):
			_, err2 = u.d.Sharer.JSONSet(pe.Path, p, want)
		case !wantPresent && present:
			_, err2 = u.d.Sharer.JSONDelete(pe.Path, p)
		default:
			continue
		}
		if err2 != nil {
			u.skip(e.Seq, pe.Path+" "+p, err2.Error())
			ok = false
			continue
		}
		u.res.KeysRestored = append(u.res.KeysRestored, pe.Path+" "+p)
	}
	if ok {
		u.res.Notes = append(u.res.Notes, "restored entries, kept later edits: "+pe.Path)
	}
	return ok
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }
