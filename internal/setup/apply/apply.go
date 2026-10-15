// Package apply runs a model.Plan (docs/spec/setup.md 6.2): lock, backup set, the local
// steps through the Sharer, PluginInstaller and Exec in plan order with every action
// journaled by internal/setup/backup, preconditions re-checked, the link guard (7.5),
// rollback of everything on a local failure (7.4), then the hub step (failure keeps the
// local steps, exit 6), prototype removal gated on a healthy hub, and the ledger update.
//
// Logins (op login) are never run here: the command layer does them one folder at a time
// and the folders are returned in Result.Logins.
//
// Phase 3 WP-B. Depends on: internal/setup/backup, internal/setup/ledger, internal/setup/model.
package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/atomicfile"
	"github.com/mraj602/switchyard/internal/setup/backup"
	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/model"
)

// Exit codes (setup.md section 4).
const (
	ExitOK           = 0
	ExitFailed       = 1 // a local step failed and was rolled back, or the link guard tripped
	ExitLocked       = 2 // another `sy setup` holds <state>/setup.lock
	ExitPrecondition = 4 // the world changed since detect
	ExitHub          = 6 // hub step failed; local steps kept
)

// Error carries the exit code of a failed run.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// ErrPrecondition marks a precondition mismatch (exit 4).
var ErrPrecondition = errors.New("setup: precondition changed")

// Options are the run's inputs besides the plan.
type Options struct {
	StateDir  string
	Command   string // recorded in the manifest
	SyVersion string
	Machine   string
	// SyPath is the absolute sy path baked into the marketplace plugin (write_tree).
	SyPath string
	Now    func() time.Time

	// Links is the detect-time link baseline guarded after every run action (7.5).
	Links []model.LinkRecord
	// MCPFiles describes the state files of mcp_sync actions (live-session folder, primary).
	MCPFiles []model.MCPFile

	// ConfigPath is config.toml (config_set, connect); TokenPath defaults to <state>/token.
	ConfigPath string
	TokenPath  string

	// Hub inputs. Token is never journaled or logged.
	Token       string
	InsecureLAN bool
	// HubHealthy says the hub was already connected and healthy (prototype removal allowed
	// without a connect action in this plan).
	HubHealthy bool

	// LockHeld says the caller already holds <state>/setup.lock (taken with Lock before
	// recovery of an interrupted run, for example); Run then does not take it again.
	LockHeld bool

	// TestHook is called with ("intent"|"acted"|"done", seq): after the intent write, after
	// the action ran (before its entry is finished) and after the entry is done. Tests panic
	// in it to simulate a crash, or return an error to fail the action. Nil in production.
	TestHook func(event string, seq int) error
}

// Deps are the collaborators of apply.
type Deps struct {
	Sharer    model.Sharer
	Plugins   model.PluginInstaller
	Service   model.Service
	Connector model.Connector
	// Exec runs `run` actions; nil means OSExec.
	Exec Exec
	// Contents returns the bytes of a write_file action (their SHA-256 must equal
	// content_sha256).
	Contents func(a model.Action) ([]byte, error)
	// ConfigSet sets a key of config.toml (config_set); the file is journaled around it.
	ConfigSet func(key string, value json.RawMessage) error
	// RemoveTimer is used by rollback of a connect entry that installed the timer.
	RemoveTimer func(ctx context.Context) error
}

// Result reports a run.
type Result struct {
	// Backup is the set id ("" when nothing was changed).
	Backup string
	// Applied lists the ids of the actions that ran and stayed.
	Applied []string
	// Skipped lists actions not run (prototype removal without a healthy hub, logins).
	Skipped []string
	// Logins are the folders of login actions, for the command layer.
	Logins []string
	Hub    *model.HubResult
	// Status is "applied", "rolled-back", or "" (nothing to do / refused before any change).
	Status   string
	ExitCode int
	// Rollback is what the rollback did, when one ran.
	Rollback *backup.Result
	Notes    []string
}

// Run applies p. The error, when not nil, is an *Error with the exit code (also in
// Result.ExitCode). A refusal before any change (lock, preconditions) leaves no backup set.
func Run(ctx context.Context, p model.Plan, o Options, d Deps) (Result, error) {
	r := &runner{p: p, o: o, d: d, touched: map[string]bool{}}
	return r.run(ctx)
}

type runner struct {
	p       model.Plan
	o       Options
	d       Deps
	set     *backup.Set
	res     Result
	touched map[string]bool
	exclude map[string]bool // link-guard paths the plan changes on purpose
	// hub
	serviceRan bool
	hubOK      bool
}

func (r *runner) fail(code int, err error) (Result, error) {
	r.res.ExitCode = code
	return r.res, &Error{Code: code, Err: err}
}

func (r *runner) now() time.Time {
	if r.o.Now != nil {
		return r.o.Now()
	}
	return time.Now()
}

func (r *runner) undoDeps() backup.Deps {
	return backup.Deps{Sharer: r.d.Sharer, Service: r.d.Service, RemoveTimer: r.d.RemoveTimer}
}

func (r *runner) run(ctx context.Context) (Result, error) {
	var work []model.Action
	for _, a := range r.p.Actions {
		if a.Op == model.OpLogin {
			r.res.Logins = append(r.res.Logins, a.Folder)
			continue
		}
		work = append(work, a)
	}
	if len(work) == 0 {
		return r.res, nil
	}
	if r.o.StateDir == "" {
		return r.fail(ExitFailed, errors.New("apply: no state dir"))
	}
	if err := os.MkdirAll(r.o.StateDir, 0o700); err != nil {
		return r.fail(ExitFailed, err)
	}
	if !r.o.LockHeld {
		unlock, err := Lock(r.o.StateDir)
		if err != nil {
			code := ExitFailed
			if errors.Is(err, ErrLocked) {
				code = ExitLocked
			}
			return r.fail(code, err)
		}
		defer unlock()
	}

	// Every precondition against the world as it is, before anything changes. A path an
	// earlier action of the plan changes is checked against what that action leaves only
	// when the action has run (recheck): "absent" after a move away, for example.
	planned := map[string]bool{}
	for _, a := range work {
		for _, pre := range a.Pre {
			if planned[pre.Path] {
				continue
			}
			if err := checkPre(pre); err != nil {
				return r.fail(ExitPrecondition, fmt.Errorf("%s (%s): %w", a.ID, a.Op, err))
			}
		}
		for _, p := range actionPaths(a) {
			planned[p] = true
		}
	}
	r.exclude = map[string]bool{}
	for _, a := range work {
		switch a.Op {
		case model.OpLink, model.OpReplaceWithLink, model.OpMoveToBackup:
			r.exclude[a.Path] = true
		case model.OpMove:
			r.exclude[a.From] = true
		}
	}

	set, err := backup.Create(r.o.StateDir, backup.Header{
		Command: r.o.Command, SyVersion: r.o.SyVersion, Machine: r.o.Machine, PlanID: r.p.PlanID,
	}, r.now())
	if err != nil {
		return r.fail(ExitFailed, err)
	}
	r.set = set
	r.res.Backup = set.ID()

	localDone := false
	for i, a := range work {
		if !a.Step.Local() && !localDone {
			localDone = true
			if err := r.guard(); err != nil {
				return r.rollback(ctx, err, ExitFailed)
			}
		}
		if a.Step == model.StepPrototype && !(r.hubOK || r.o.HubHealthy) {
			r.res.Skipped = append(r.res.Skipped, a.ID)
			continue
		}
		if err := r.recheck(a); err != nil {
			if a.Step.Local() {
				return r.rollback(ctx, err, ExitPrecondition)
			}
			return r.nonLocalFailure(ctx, a, err, work[i+1:])
		}
		err := r.exec(ctx, a)
		r.markTouched(a)
		if err == nil && a.Op == model.OpRun {
			err = r.guard()
		}
		if err != nil {
			if a.Step.Local() {
				code := ExitFailed
				if errors.Is(err, ErrPrecondition) {
					code = ExitPrecondition
				}
				return r.rollback(ctx, err, code)
			}
			return r.nonLocalFailure(ctx, a, err, work[i+1:])
		}
		r.res.Applied = append(r.res.Applied, a.ID)
	}
	if !localDone {
		if err := r.guard(); err != nil {
			return r.rollback(ctx, err, ExitFailed)
		}
	}

	if err := r.set.SetStatus(model.StatusApplied); err != nil {
		return r.fail(ExitFailed, err)
	}
	r.res.Status = model.StatusApplied
	prev, err := ledger.Read(r.o.StateDir)
	if err == nil {
		err = ledger.Write(r.o.StateDir, ledger.Update(prev, r.p, r.set.Manifest()))
	}
	if err != nil {
		return r.fail(ExitFailed, fmt.Errorf("ledger: %w", err))
	}
	if r.res.Hub != nil && r.res.Hub.Error != "" {
		return r.fail(ExitHub, errors.New(r.res.Hub.Error))
	}
	return r.res, nil
}

// rollback undoes the whole in-progress set (7.4) and reports cause.
func (r *runner) rollback(ctx context.Context, cause error, code int) (Result, error) {
	rb, err := backup.UndoSet(context.WithoutCancel(ctx), r.set, r.undoDeps(), backup.Options{}, true)
	r.res.Rollback = &rb
	r.res.Status = model.StatusRolledBack
	r.res.Applied = nil
	if err != nil {
		cause = fmt.Errorf("%w (rollback failed: %v)", cause, err)
	} else if len(rb.Skipped) > 0 {
		cause = fmt.Errorf("%w (rollback left %d paths: see the backup set %s)", cause, len(rb.Skipped), r.set.ID())
	}
	return r.fail(code, cause)
}

// nonLocalFailure drops only the failed hub/prototype entry, keeps the local steps.
func (r *runner) nonLocalFailure(ctx context.Context, a model.Action, cause error, rest []model.Action) (Result, error) {
	for _, x := range rest {
		r.res.Skipped = append(r.res.Skipped, x.ID)
	}
	m := r.set.Manifest()
	if n := len(m.Entries); n > 0 && m.Entries[n-1].Action == a.ID {
		rb, _ := backup.UndoSince(context.WithoutCancel(ctx), r.set, r.undoDeps(), backup.Options{}, m.Entries[n-1].Seq)
		r.res.Rollback = &rb
	}
	if err := r.set.SetStatus(model.StatusApplied); err != nil {
		return r.fail(ExitFailed, err)
	}
	r.res.Status = model.StatusApplied
	prev, err := ledger.Read(r.o.StateDir)
	if err == nil {
		_ = ledger.Write(r.o.StateDir, ledger.Update(prev, r.p, r.set.Manifest()))
	}
	code := ExitHub
	switch a.Step {
	case model.StepHub:
		r.res.Hub = &model.HubResult{URL: a.URL, Error: cause.Error(), Step: string(a.Op)}
	default:
		code = ExitFailed
	}
	return r.fail(code, cause)
}

// ---- preconditions and the link guard -------------------------------------------------

func checkPre(pre model.Precondition) error {
	var (
		cur model.PathState
		err error
	)
	if backup.IsStateFile(pre.Path) {
		cur, err = backup.CaptureMeta(pre.Path)
	} else {
		cur, err = backup.Capture(pre.Path)
	}
	if err != nil {
		return err
	}
	w := pre.State
	switch {
	case cur.Type != w.Type:
		return fmt.Errorf("%w: %s is %s, expected %s", ErrPrecondition, pre.Path, cur.Type, w.Type)
	case w.Target != "" && cur.Target != w.Target:
		return fmt.Errorf("%w: %s points to %q, expected %q", ErrPrecondition, pre.Path, cur.Target, w.Target)
	case w.Type == model.TypeFile && w.SHA256 != "" && cur.SHA256 != "" && cur.SHA256 != w.SHA256:
		return fmt.Errorf("%w: %s changed (hash)", ErrPrecondition, pre.Path)
	case w.Type == model.TypeFile && w.Size != 0 && cur.Size != w.Size:
		return fmt.Errorf("%w: %s changed (size %d, expected %d)", ErrPrecondition, pre.Path, cur.Size, w.Size)
	}
	return nil
}

// recheck re-reads the preconditions right before an action, except for paths an earlier
// action of this run has changed on purpose.
func (r *runner) recheck(a model.Action) error {
	for _, pre := range a.Pre {
		if r.touched[pre.Path] {
			continue
		}
		if err := checkPre(pre); err != nil {
			return fmt.Errorf("%s (%s): %w", a.ID, a.Op, err)
		}
	}
	return nil
}

func (r *runner) markTouched(a model.Action) {
	for _, p := range actionPaths(a) {
		r.touched[p] = true
	}
}

// actionPaths lists the paths an action changes.
func actionPaths(a model.Action) []string {
	var out []string
	for _, p := range []string{a.Path, a.From, a.To, a.File, a.Unit} {
		if p != "" {
			out = append(out, p)
		}
	}
	return append(append(out, a.Files...), a.Touches...)
}

// guard re-reads every link of the detect baseline; any change is repaired from the record
// and reported (the caller then rolls back).
func (r *runner) guard() error {
	var bad []string
	for _, l := range r.o.Links {
		if r.exclude[l.Path] {
			continue
		}
		got, err := os.Readlink(l.Path)
		if err == nil && got == l.Raw {
			continue
		}
		bad = append(bad, l.Path)
		_ = os.RemoveAll(l.Path)
		if l.Kind == model.LinkJunction && r.d.Sharer != nil {
			_ = r.d.Sharer.Link(l.Path, l.Raw, model.LinkJunction)
		} else {
			_ = os.Symlink(l.Raw, l.Path)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("link guard: %d links changed during the run (restored): %s", len(bad), strings.Join(bad, ", "))
	}
	return nil
}

// ---- actions ---------------------------------------------------------------------------

func escapePtr(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// ancestors journals the folders a write will create, outermost first.
func ancestors(path string) []backup.PathSpec {
	var out []backup.PathSpec
	for _, d := range backup.MissingAncestors(path) {
		out = append(out, backup.PathSpec{Path: d, Intended: &model.PathState{Type: model.TypeDir}})
	}
	return out
}

// linkState is what Sharer.Link leaves at a path: the intended state of a link action, nil
// when it cannot be known up front.
func linkState(a model.Action) *model.PathState {
	switch kind(a) {
	case model.LinkSymlink:
		return &model.PathState{Type: model.TypeSymlink, Target: a.Target}
	case model.LinkJunction:
		return &model.PathState{Type: model.TypeJunction}
	case model.LinkCopy:
		st, err := backup.Capture(a.Target)
		if err != nil {
			return nil
		}
		switch st.Type {
		case model.TypeFile:
			return &model.PathState{Type: model.TypeFile, Size: st.Size, SHA256: st.SHA256}
		case model.TypeDir:
			h, err := backup.TreeHash(a.Target)
			if err != nil {
				return nil
			}
			return &model.PathState{Type: model.TypeDir, SHA256: h}
		}
	}
	return nil
}

// fileState is the intended state of a regular file written with content and mode.
func fileState(content []byte, mode os.FileMode) *model.PathState {
	return &model.PathState{Type: model.TypeFile, Size: int64(len(content)), SHA256: backup.SHA256Hex(content),
		Mode: model.ModeString(mode)}
}

func (r *runner) journaled(a model.Action, spec backup.Spec, act func(tx *backup.Tx) error) error {
	tx, err := r.set.Begin(a.ID, a.Op, spec)
	if err != nil {
		return err
	}
	hook := func(ev string) error {
		if r.o.TestHook == nil {
			return nil
		}
		return r.o.TestHook(ev, tx.Seq())
	}
	aerr := hook("intent")
	if aerr == nil {
		aerr = act(tx)
		if aerr == nil {
			aerr = hook("acted")
		}
	}
	ferr := tx.Finish(nil)
	if aerr == nil {
		aerr = hook("done")
	}
	if aerr != nil {
		return aerr
	}
	return ferr
}

func (r *runner) exec(ctx context.Context, a model.Action) error {
	sh := r.d.Sharer
	switch a.Op {
	case model.OpMkdir:
		mode := os.FileMode(0o700)
		if m, err := model.ParseMode(a.Mode); err == nil && a.Mode != "" {
			mode = m
		}
		spec := backup.Spec{Paths: append(ancestors(a.Path), backup.PathSpec{Path: a.Path,
			Intended: &model.PathState{Type: model.TypeDir, Mode: model.ModeString(mode)}})}
		return r.journaled(a, spec, func(*backup.Tx) error {
			if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
				return err
			}
			if err := os.Mkdir(a.Path, mode); err != nil {
				return err
			}
			return os.Chmod(a.Path, mode)
		})

	case model.OpLink:
		if st, err := backup.Capture(a.Path); err != nil {
			return err
		} else if st.Type == model.TypeFile || st.Type == model.TypeDir {
			return fmt.Errorf("%w: %s exists and is not a link", ErrPrecondition, a.Path)
		}
		spec := backup.Spec{Paths: append(ancestors(a.Path), backup.PathSpec{Path: a.Path, Intended: linkState(a)})}
		return r.journaled(a, spec, func(*backup.Tx) error {
			if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
				return err
			}
			return sh.Link(a.Path, a.Target, kind(a))
		})

	case model.OpReplaceWithLink:
		spec := backup.Spec{Paths: []backup.PathSpec{{Path: a.Path, Save: backup.SaveReserve, Intended: linkState(a)}}}
		return r.journaled(a, spec, func(tx *backup.Tx) error {
			return sh.ReplaceWithLink(a.Path, a.Target, kind(a), tx.SaveTo(a.Path))
		})

	case model.OpMove:
		return r.move(a)

	case model.OpMoveToBackup:
		spec := backup.Spec{Paths: []backup.PathSpec{{Path: a.Path, Save: backup.SaveReserve,
			Intended: &model.PathState{Type: model.TypeAbsent}}}}
		return r.journaled(a, spec, func(tx *backup.Tx) error {
			to := tx.SaveTo(a.Path)
			if to == "" {
				return fmt.Errorf("%w: %s is not a file or folder", ErrPrecondition, a.Path)
			}
			return sh.Move(a.Path, to)
		})

	case model.OpMergeLines:
		return r.mergeLines(a)

	case model.OpJSONSet, model.OpJSONDelete:
		intend := map[string]json.RawMessage{a.Pointer: nil}
		if a.Op == model.OpJSONSet {
			intend[a.Pointer] = a.Value
		}
		spec := backup.Spec{Paths: []backup.PathSpec{
			{Path: a.File, Via: a.Via, Save: backup.SaveCopy, Keys: []string{a.Pointer}, Intend: intend}}}
		return r.journaled(a, spec, func(*backup.Tx) error {
			var err error
			if a.Op == model.OpJSONSet {
				_, err = sh.JSONSet(a.File, a.Pointer, a.Value)
			} else {
				_, err = sh.JSONDelete(a.File, a.Pointer)
			}
			return err
		})

	case model.OpMCPSync:
		return r.mcpSync(ctx, a)

	case model.OpWriteFile:
		if r.d.Contents == nil {
			return errors.New("apply: no Contents provider for write_file")
		}
		content, err := r.d.Contents(a)
		if err != nil {
			return err
		}
		if a.ContentSHA256 != "" && backup.SHA256Hex(content) != a.ContentSHA256 {
			return fmt.Errorf("apply: content of %s does not match the plan's hash", a.Path)
		}
		mode := os.FileMode(0o644)
		if m, err := model.ParseMode(a.Mode); err == nil && a.Mode != "" {
			mode = m
		}
		spec := backup.Spec{Paths: append(ancestors(a.Path), backup.PathSpec{Path: a.Path, Save: backup.SaveCopy,
			Intended: fileState(content, mode)})}
		return r.journaled(a, spec, func(*backup.Tx) error {
			if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
				return err
			}
			return atomicfile.Write(a.Path, content, mode)
		})

	case model.OpWriteTree:
		return r.writeTree(a)

	case model.OpRun:
		return r.runCmd(ctx, a)

	case model.OpService:
		spec := backup.Spec{Name: a.Name, Paths: append(ancestors(a.Unit), backup.PathSpec{Path: a.Unit, Save: backup.SaveCopy})}
		err := r.journaled(a, spec, func(*backup.Tx) error {
			return r.d.Service.Install(ctx, model.ServiceSpec{Name: a.Name, Unit: a.Unit, Listen: a.Listen})
		})
		if err == nil {
			r.serviceRan = true
		}
		return err

	case model.OpConnect:
		return r.connect(ctx, a)

	case model.OpConfigSet:
		return r.configSet(a)
	}
	return fmt.Errorf("apply: unknown op %q", a.Op)
}

func kind(a model.Action) string {
	if a.Kind == "" {
		return model.LinkSymlink
	}
	return a.Kind
}

// move renames a.From to a.To. a.To must not exist: a move never merges into or replaces
// what is there (the planner saves an existing a.To first with move_to_backup; a directory
// merge is planned as one move per missing entry).
func (r *runner) move(a model.Action) error {
	sh := r.d.Sharer
	if _, err := os.Lstat(a.From); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrPrecondition, a.From, err)
	}
	if _, err := os.Lstat(a.To); err == nil || !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s exists", ErrPrecondition, a.To)
	}
	// a.To is journaled (absent before) ahead of a.From so that undo, newest path first,
	// first renames the original back from a.To and then removes whatever is left there
	// (a moved link is recreated at a.From, not renamed back).
	var moved *model.PathState
	if st, err := backup.Capture(a.From); err == nil {
		moved = &model.PathState{Type: st.Type, Target: st.Target, SHA256: st.SHA256, Size: st.Size}
	}
	spec := backup.Spec{Paths: append(ancestors(a.To),
		backup.PathSpec{Path: a.To, Intended: moved},
		backup.PathSpec{Path: a.From, SavedAs: backup.SavedMovedPrefix + a.To, Intended: &model.PathState{Type: model.TypeAbsent}})}
	return r.journaled(a, spec, func(*backup.Tx) error {
		if err := os.MkdirAll(filepath.Dir(a.To), 0o755); err != nil {
			return err
		}
		return sh.Move(a.From, a.To)
	})
}

func (r *runner) mcpSync(ctx context.Context, a model.Action) error {
	var ptrs []string
	if a.Names != nil {
		for _, group := range [][]string{a.Names.Added, a.Names.Changed, a.Names.Removed} {
			for _, n := range group {
				ptrs = append(ptrs, "/mcpServers/"+escapePtr(n))
			}
		}
	}
	if len(ptrs) == 0 {
		ptrs = []string{"/mcpServers"}
	}
	req := model.MCPSyncRequest{StateDir: r.o.StateDir}
	var paths []backup.PathSpec
	for _, f := range a.Files {
		mf := model.MCPFile{StateFile: f, Dir: filepath.Dir(f)}
		for _, c := range r.o.MCPFiles {
			if c.StateFile == f {
				mf = c
			}
		}
		req.Files = append(req.Files, mf)
		paths = append(paths, backup.PathSpec{Path: f, Meta: true, Keys: ptrs})
	}
	// The sync's base (share/mcp-base.json) is journaled with the keys: undo puts it back
	// too, or the next sync would read the reverted keys as deletions (5.4).
	paths = append(paths, backup.PathSpec{Path: MCPBasePath(r.o.StateDir), Save: backup.SaveCopy})
	return r.journaled(a, backup.Spec{Paths: paths}, func(*backup.Tx) error {
		_, err := r.d.Sharer.MCPSync(ctx, req)
		return err
	})
}

func (r *runner) runCmd(ctx context.Context, a model.Action) error {
	ex := r.d.Exec
	if ex == nil {
		ex = OSExec{}
	}
	var paths []backup.PathSpec
	for _, t := range a.Touches {
		if backup.IsStateFile(t) {
			return fmt.Errorf("apply: %s is a state file and cannot be in touches", t)
		}
		paths = append(paths, backup.PathSpec{Path: t, Save: backup.SaveCopy})
	}
	// Only what the plugin commands make for sy's marketplace, and for Warp's marketplace
	// that `sy term setup --warp-plugin` adds (phase 7, terminal.md T10), is ever removed by
	// undo (7.3).
	spec := backup.Spec{Paths: paths, Watch: a.Watch, Expect: []string{marketplaceName, warpMarketplaceName}}
	return r.journaled(a, spec, func(tx *backup.Tx) error {
		out, err := ex.Run(ctx, a.Argv, a.Env)
		created, cerr := tx.CreatedSince()
		if cerr == nil && len(created) > 0 {
			tx.Edit(func(e *model.Entry) { e.Created = created })
		}
		if err != nil {
			return err
		}
		return checkOutcome(out)
	})
}

func (r *runner) connect(ctx context.Context, a model.Action) error {
	if r.d.Connector == nil {
		return errors.New("apply: no Connector")
	}
	tokenPath := r.o.TokenPath
	if tokenPath == "" {
		tokenPath = filepath.Join(r.o.StateDir, "token")
	}
	spec := backup.Spec{Paths: []backup.PathSpec{
		{Path: r.o.ConfigPath, Save: backup.SaveCopy}, {Path: tokenPath, Save: backup.SaveCopy}}}
	if r.o.ConfigPath == "" {
		spec.Paths = spec.Paths[1:]
	}
	token := r.o.Token
	var res model.ConnectResult
	err := r.journaled(a, spec, func(tx *backup.Tx) error {
		if a.Reach == model.HubPull {
			// Phase 5: pulled over SSH: no token, no URL; Connect sets hub.reach and the timer.
			var err error
			res, err = r.d.Connector.Connect(ctx, model.ConnectRequest{Pull: true, Reach: a.Reach})
			if res.Timer {
				tx.Edit(func(e *model.Entry) { e.Name = "timer" })
			}
			return err
		}
		if token == "" && r.serviceRan {
			if err := r.d.Connector.WaitHealthy(ctx, a.URL, 30); err != nil {
				return err
			}
			t, err := r.d.Connector.MintMachineToken(ctx, r.o.Machine)
			if err != nil {
				return err
			}
			token = t
		}
		if token == "" {
			return errors.New("no hub token given")
		}
		var err error
		res, err = r.d.Connector.Connect(ctx, model.ConnectRequest{URL: a.URL, Token: token, InsecureLAN: r.o.InsecureLAN, Dial: a.Dial, Reach: a.Reach})
		if res.Timer {
			tx.Edit(func(e *model.Entry) { e.Name = "timer" })
		}
		return err
	})
	if err != nil {
		return err
	}
	r.res.Hub = &model.HubResult{URL: a.URL, Healthy: res.Healthy}
	if a.Reach == model.HubPull {
		return nil // nothing to ask: the hub comes to this machine
	}
	if !res.Healthy {
		r.res.Hub.Error = "the hub did not answer /health"
		r.res.Hub.Step = string(model.OpConnect)
		return nil // entry stays; reported as exit 6 at the end
	}
	r.hubOK = true
	return nil
}

func (r *runner) configSet(a model.Action) error {
	if r.d.ConfigSet == nil || r.o.ConfigPath == "" {
		return errors.New("apply: config_set needs ConfigPath and a ConfigSet function")
	}
	bak := r.o.ConfigPath + ".bak"
	spec := backup.Spec{Paths: append(ancestors(r.o.ConfigPath),
		backup.PathSpec{Path: r.o.ConfigPath, Save: backup.SaveCopy}, backup.PathSpec{Path: bak})}
	return r.journaled(a, spec, func(*backup.Tx) error {
		if err := os.MkdirAll(filepath.Dir(r.o.ConfigPath), 0o755); err != nil {
			return err
		}
		if _, err := os.Lstat(bak); err != nil {
			if b, rerr := os.ReadFile(r.o.ConfigPath); rerr == nil {
				if err := atomicfile.Write(bak, b, 0o600); err != nil {
					return err
				}
			}
		}
		return r.d.ConfigSet(a.Key, a.Value)
	})
}
