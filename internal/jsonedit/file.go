package jsonedit

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
)

// Skip reasons (model.MCPSkip.Reason).
const (
	ReasonLiveSession = "live-session"
	ReasonLocked      = "locked"
	ReasonChanged     = "changed-while-writing"
)

// ErrSkipped matches every *SkipError: the file was left alone and the edit should be
// retried at the next sync.
var ErrSkipped = errors.New("jsonedit: file skipped")

// SkipError says why a file was skipped.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return "jsonedit: file skipped: " + e.Reason }

// Is makes errors.Is(err, ErrSkipped) true.
func (e *SkipError) Is(target error) bool { return target == ErrSkipped }

// Options tunes Apply. The zero value is the plain settings mode: no lock, no session check
// (setup.md 5.5).
type Options struct {
	// Claude selects the .claude.json mode of 5.4: the `<file>.lock` directory is taken, and
	// a live session in Folder skips the file.
	Claude bool
	// Folder is the config folder whose sessions/*.json are checked (Claude mode).
	Folder string
	// IgnoreLive turns the live-session skip off (`sy share sync --now`).
	IgnoreLive bool
	// LockWait is how long to wait for a fresh lock (default 5 s); LockStale is the age past
	// which a lock is broken (default 10 s).
	LockWait, LockStale time.Duration
	// Retries is how many times the edit restarts when the file changed under it (default 3).
	Retries int
}

// Op is one edit.
type Op struct {
	Pointer string
	Value   []byte // JSON; ignored when Delete
	Delete  bool
}

// OpResult is what one Op found and did.
type OpResult struct {
	BeforePresent bool
	Before        []byte
	Changed       bool
}

// Result is the outcome of Apply.
type Result struct {
	Target  string // resolved file
	Ops     []OpResult
	Written bool // false when nothing changed (no write happened)
}

// Changed reports whether any op changed the document.
func (r Result) Changed() bool {
	for _, o := range r.Ops {
		if o.Changed {
			return true
		}
	}
	return false
}

// Read returns the value at pointer in the file (resolved through links) as written.
func Read(path, pointer string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return Get(b, pointer)
}

// pidAlive is replaced in tests.
var pidAlive = processAlive

// LiveSession reports whether folder/sessions/*.json names a running process. Only the
// ".json" files are opened (the *.key files next to them are credentials-adjacent).
func LiveSession(folder string) bool {
	ents, err := os.ReadDir(filepath.Join(folder, "sessions"))
	if err != nil {
		return false
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		pid := 0
		if b, err := os.ReadFile(filepath.Join(folder, "sessions", name)); err == nil {
			var s struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(b, &s) == nil {
				pid = s.PID
			}
		}
		if pid == 0 {
			fmt.Sscanf(strings.TrimSuffix(name, ".json"), "%d", &pid)
		}
		if pid > 0 && pidAlive(pid) {
			return true
		}
	}
	return false
}

// takeLock makes `<path>.lock` (a directory, as Claude Code does) and returns its release.
func takeLock(path string, wait, stale time.Duration) (func(), error) {
	lock := path + ".lock"
	deadline := time.Now().Add(wait)
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			return func() { _ = os.Remove(lock) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > stale {
			_ = os.RemoveAll(lock)
			continue
		}
		if !time.Now().Before(deadline) {
			return nil, &SkipError{Reason: ReasonLocked}
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Apply runs ops against the file at path, in order, as one write. The write goes to the
// resolved target of any symlink chain (so every link keeps working), keeps the file's
// permission bits, and is skipped entirely when no op changes anything. In Claude mode it
// takes the lock directory and honours the live-session skip. A skipped file returns a
// *SkipError (errors.Is(err, ErrSkipped)).
func Apply(path string, o Options, ops ...Op) (Result, error) {
	if o.LockWait == 0 {
		o.LockWait = 5 * time.Second
	}
	if o.LockStale == 0 {
		o.LockStale = 10 * time.Second
	}
	if o.Retries == 0 {
		o.Retries = 3
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Result{}, err
	}
	if o.Claude {
		if !o.IgnoreLive && o.Folder != "" && LiveSession(o.Folder) {
			return Result{Target: target}, &SkipError{Reason: ReasonLiveSession}
		}
		unlock, err := takeLock(path, o.LockWait, o.LockStale)
		if err != nil {
			return Result{Target: target}, err
		}
		defer unlock()
	}
	for attempt := 0; attempt < o.Retries; attempt++ {
		res, retry, err := applyOnce(target, o, ops)
		if err != nil || !retry {
			return res, err
		}
	}
	return Result{Target: target}, &SkipError{Reason: ReasonChanged}
}

func applyOnce(target string, o Options, ops []Op) (Result, bool, error) {
	res := Result{Target: target}
	fi, err := os.Stat(target)
	if err != nil {
		return res, false, err
	}
	before, err := os.ReadFile(target)
	if err != nil {
		return res, false, err
	}
	doc := before
	for _, op := range ops {
		var ch Change
		var perr error
		if op.Delete {
			ch, perr = PlanDelete(doc, op.Pointer)
		} else {
			ch, perr = PlanSet(doc, op.Pointer, op.Value)
		}
		if perr != nil {
			return res, false, perr
		}
		res.Ops = append(res.Ops, OpResult{BeforePresent: ch.BeforePresent, Before: ch.Before, Changed: ch.Changed})
		doc = ch.Apply(doc)
	}
	if !res.Changed() {
		return res, false, nil
	}
	if err := Verify(before, doc, ops); err != nil {
		return res, false, err
	}
	tmp := filepath.Join(filepath.Dir(target), filepath.Base(target)+".sy."+randHex()+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return res, false, err
	}
	fail := func(err error) (Result, bool, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return res, false, err
	}
	if _, err := f.Write(doc); err != nil {
		return fail(err)
	}
	if err := f.Chmod(fi.Mode().Perm()); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return res, false, err
	}
	fi2, err := os.Stat(target)
	if err != nil || fi2.Size() != fi.Size() || !fi2.ModTime().Equal(fi.ModTime()) {
		_ = os.Remove(tmp)
		return res, true, nil
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return res, false, err
	}
	syncDir(filepath.Dir(target))
	res.Written = true
	return res, false, nil
}

func randHex() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// Verify is the check of setup.md 5.4 step 4: the result parses, every top-level key other
// than those the ops touched is deep-equal to before, and oauthAccount is byte-identical
// (unless an op targeted it, which sy never does).
func Verify(before, after []byte, ops []Op) error {
	if !json.Valid(after) {
		return errors.New("jsonedit: check failed: result does not parse")
	}
	touched := map[string]bool{}
	for _, op := range ops {
		toks, err := ParsePointer(op.Pointer)
		if err != nil {
			return err
		}
		touched[toks[0]] = true
	}
	var b, a map[string]json.RawMessage
	if json.Unmarshal(before, &b) != nil || json.Unmarshal(after, &a) != nil {
		return errors.New("jsonedit: check failed: document is not an object")
	}
	for k, bv := range b {
		if touched[k] {
			continue
		}
		av, ok := a[k]
		if !ok {
			return fmt.Errorf("jsonedit: check failed: key %q disappeared", k)
		}
		if !bytes.Equal(av, bv) && !sameValue(av, bv) {
			return fmt.Errorf("jsonedit: check failed: key %q changed", k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok && !touched[k] {
			return fmt.Errorf("jsonedit: check failed: key %q appeared", k)
		}
	}
	if !touched["oauthAccount"] {
		if !reflect.DeepEqual(b["oauthAccount"], a["oauthAccount"]) {
			return errors.New("jsonedit: check failed: oauthAccount changed")
		}
	}
	return nil
}
