package share

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mraj602/watchyard/internal/jsonedit"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// Sharer implements model.Sharer on the real file system.
type Sharer struct {
	// StateDir is `<state>` (config.StateDir). It holds share/mcp-base.json, the conflict log
	// and copies.json. It may be empty for the link and merge primitives; MCPSync takes its
	// own StateDir from the request.
	StateDir string
}

var _ model.Sharer = (*Sharer)(nil)

// New returns a Sharer for a state folder.
func New(stateDir string) *Sharer { return &Sharer{StateDir: stateDir} }

// ReadLink returns the raw target and kind (model.LinkSymlink or LinkJunction) when path
// itself is a link.
func ReadLink(path string) (raw, kind string, ok bool) {
	raw, junction, isLink := linkInfo(path)
	if !isLink {
		return "", "", false
	}
	if junction {
		return raw, model.LinkJunction, true
	}
	return raw, model.LinkSymlink, true
}

// SymlinksAllowed tries one symlink in dir (setup.md 5.7: done once at detect time in
// `<state>` on Windows).
func SymlinksAllowed(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	l := filepath.Join(dir, ".sy-symlink-probe-"+randHex())
	if err := os.Symlink(dir, l); err != nil {
		return false
	}
	_ = os.Remove(l)
	return true
}

// placeTemp creates the link (or copy) at a temp name next to path and returns it.
func placeTemp(path, target, kind string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp := tempName(path)
	var err error
	switch kind {
	case model.LinkSymlink:
		err = os.Symlink(target, tmp)
	case model.LinkJunction:
		err = makeJunction(tmp, target)
	case model.LinkCopy:
		err = copyPath(target, tmp)
	default:
		err = fmt.Errorf("share: unknown link kind %q", kind)
	}
	if err != nil {
		_ = removeAny(tmp)
		return "", err
	}
	return tmp, nil
}

func checkLinkArgs(path, target, kind string) error {
	if err := guard(path, target); err != nil {
		return err
	}
	if !filepath.IsAbs(target) {
		return fmt.Errorf("share: link target must be absolute: %s", target)
	}
	// The target must exist first: a dangling link makes Claude Code fail with ENOENT when
	// it mkdirs inside it (setup.md 5.1).
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("share: link target must exist: %w", err)
	}
	switch kind {
	case model.LinkSymlink, model.LinkJunction, model.LinkCopy:
		return nil
	}
	return fmt.Errorf("share: unknown link kind %q", kind)
}

// Link makes path a link to target (absolute, must exist) by creating it at a temp name and
// renaming it over path, so an existing file or link is never momentarily absent. A real
// directory at path is not replaced (use ReplaceWithLink).
func (s *Sharer) Link(path, target, kind string) error {
	if err := checkLinkArgs(path, target, kind); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
		if _, _, isLink := linkInfo(path); !isLink {
			return fmt.Errorf("share: %s is a directory; use ReplaceWithLink", path)
		}
	}
	tmp, err := placeTemp(path, target, kind)
	if err != nil {
		return err
	}
	if err := renameOver(tmp, path, kind == model.LinkJunction); err != nil {
		_ = removeAny(tmp)
		return err
	}
	return s.afterLink(path, target, kind)
}

// renameOver renames tmp over path. A folder (a copied tree) or a junction cannot be renamed
// over an existing link (ENOTDIR, access denied): the link is removed first and put back if
// the rename then fails.
func renameOver(tmp, path string, junctionKind bool) error {
	raw, junction, isLink := linkInfo(path)
	tfi, err := os.Lstat(tmp)
	if err != nil {
		return err
	}
	_, _, tmpLink := linkInfo(tmp)
	if !isLink || !(junctionKind || (tfi.IsDir() && !tmpLink)) {
		return os.Rename(tmp, path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		var rerr error
		if junction {
			rerr = makeJunction(path, raw)
		} else {
			rerr = os.Symlink(raw, path)
		}
		if rerr != nil {
			return fmt.Errorf("share: replacing the link %s failed (%v) and putting it back failed: %w", path, err, rerr)
		}
		return err
	}
	return nil
}

// Unshare replaces the link at path with an independent copy of what it resolves to
// (`wy account remove --unshare`). The copy is built at a temp name next to path first. Unlike
// Link with LinkCopy it never registers a synced copy: the copy is the folder's own from now
// on and `wy tick` never syncs it (5.7).
func (s *Sharer) Unshare(path string) error {
	if err := guard(path); err != nil {
		return err
	}
	if _, _, isLink := linkInfo(path); !isLink {
		return fmt.Errorf("share: %s is not a link", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("share: %s is a dangling link: %w", path, err)
	}
	if err := guard(real); err != nil {
		return err
	}
	tmp := tempName(path)
	if err := copyPath(real, tmp); err != nil {
		_ = removeAny(tmp)
		return err
	}
	if err := renameOver(tmp, path, false); err != nil {
		_ = removeAny(tmp)
		return err
	}
	return nil
}

// ForgetCopy removes path from the synced-copy groups (undo of a LinkCopy): from now on it is
// an independent file that `wy tick` leaves alone. A no-op when it is not registered.
func (s *Sharer) ForgetCopy(path string) error {
	if s.StateDir == "" {
		return nil
	}
	_, err := ForgetCopies(s.StateDir, func(p string) bool { return p == path })
	return err
}

func (s *Sharer) afterLink(path, target, kind string) error {
	if kind == model.LinkCopy && s.StateDir != "" {
		return registerCopy(s.StateDir, target, path)
	}
	return nil
}

// ReplaceWithLink replaces the item at path (file, link or directory) with a link. The link
// is built at a temp name first. With saveTo the original is moved there (copy+delete across
// file systems); without it the original is discarded (the caller has saved it).
func (s *Sharer) ReplaceWithLink(path, target, kind, saveTo string) error {
	if err := checkLinkArgs(path, target, kind); err != nil {
		return err
	}
	if err := guard(saveTo); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if saveTo != "" {
				return fmt.Errorf("share: nothing at %s to save", path)
			}
			return s.Link(path, target, kind)
		}
		return err
	}
	tmp, err := placeTemp(path, target, kind)
	if err != nil {
		return err
	}
	_, _, isLink := linkInfo(path)
	realDir := fi.IsDir() && !isLink
	if saveTo == "" && !realDir {
		if err := renameOver(tmp, path, kind == model.LinkJunction || (isLink && fi.IsDir())); err != nil {
			_ = removeAny(tmp)
			return err
		}
		return s.afterLink(path, target, kind)
	}
	// Move the original out of the way, then put the link in place.
	away := saveTo
	if away == "" {
		away = tempName(path)
	}
	if err := moveAny(path, away); err != nil {
		_ = removeAny(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = removeAny(tmp)
		if rerr := moveAny(away, path); rerr != nil {
			return fmt.Errorf("share: link failed (%v) and restoring %s failed: %w", err, path, rerr)
		}
		return err
	}
	if saveTo == "" {
		if err := os.RemoveAll(away); err != nil {
			return err
		}
	}
	return s.afterLink(path, target, kind)
}

// Move renames from to to (copy+delete across file systems). to must not exist.
func (s *Sharer) Move(from, to string) error {
	if err := guard(from, to); err != nil {
		return err
	}
	if _, err := os.Lstat(from); err != nil {
		return err
	}
	return moveAny(from, to)
}

// Promote moves one folder's copy of an item into the primary, which lacks it (5.1); the
// caller links the other folders afterwards. All copies that exist must already agree
// (see AgreeAll); the original is saved by the caller (or via saveTo with a link).
func (s *Sharer) Promote(from, primaryPath string) error {
	if exists(primaryPath) {
		return fmt.Errorf("share: primary already has %s", primaryPath)
	}
	return s.Move(from, primaryPath)
}

// AgreeAll reports whether every given path is identical to the first (TreesEqual).
func AgreeAll(paths []string) (bool, error) {
	for _, p := range paths[1:] {
		eq, err := TreesEqual(paths[0], p)
		if err != nil || !eq {
			return false, err
		}
	}
	return true, nil
}

// JSONSet sets the value at pointer in file by byte-span splice at the resolved target,
// keeping the mode. A .claude.json is touched only at /mcpServers (setup.md 0.3).
func (s *Sharer) JSONSet(file, pointer string, value json.RawMessage) (model.JSONResult, error) {
	return s.jsonEdit(file, pointer, value, false)
}

// JSONDelete removes the key at pointer.
func (s *Sharer) JSONDelete(file, pointer string) (model.JSONResult, error) {
	return s.jsonEdit(file, pointer, nil, true)
}

func (s *Sharer) jsonEdit(file, pointer string, value []byte, del bool) (model.JSONResult, error) {
	if err := guard(file); err != nil {
		return model.JSONResult{}, err
	}
	o := jsonedit.Options{}
	if filepath.Base(file) == ".claude.json" {
		if pointer != "/mcpServers" && !strings.HasPrefix(pointer, "/mcpServers/") {
			return model.JSONResult{}, fmt.Errorf("share: refusing to edit %s outside /mcpServers", file)
		}
		o = jsonedit.Options{Claude: true, Folder: filepath.Dir(file)}
	}
	res, err := jsonedit.Apply(file, o, jsonedit.Op{Pointer: pointer, Value: value, Delete: del})
	out := model.JSONResult{Target: res.Target}
	if len(res.Ops) == 1 {
		out.BeforePresent = res.Ops[0].BeforePresent
		out.Before = res.Ops[0].Before
		out.Changed = res.Ops[0].Changed
	}
	if err != nil && !errors.Is(err, jsonedit.ErrSkipped) {
		return out, fmt.Errorf("share: edit %s: %w", file, err)
	}
	return out, err
}

// Snapshot captures the PathState of path (lstat, raw link target, mode, mtime, SHA-256 of
// regular files). Links are never followed.
func (s *Sharer) Snapshot(path string) (model.PathState, error) {
	if err := guard(path); err != nil {
		return model.PathState{}, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return model.PathState{Type: model.TypeAbsent}, nil
		}
		return model.PathState{}, err
	}
	if raw, junction, isLink := linkInfo(path); isLink {
		t := model.TypeSymlink
		if junction {
			t = model.TypeJunction
		}
		return model.PathState{Type: t, Target: raw}, nil
	}
	st := model.PathState{Mode: model.ModeString(fi.Mode()), MtimeNS: fi.ModTime().UnixNano()}
	switch {
	case fi.IsDir():
		st.Type = model.TypeDir
	case fi.Mode().IsRegular():
		st.Type = model.TypeFile
		st.Size = fi.Size()
		f, err := os.Open(path)
		if err != nil {
			return model.PathState{}, err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return model.PathState{}, err
		}
		st.SHA256 = hex.EncodeToString(h.Sum(nil))
	default:
		return model.PathState{}, fmt.Errorf("share: %s is not a file, directory or link", path)
	}
	return st, nil
}
