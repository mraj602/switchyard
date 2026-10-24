package share

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// lineTimestamp extracts the "timestamp" field of a history line as milliseconds: a JSON
// number (Claude Code writes epoch milliseconds), an RFC 3339 string or a numeric string.
func lineTimestamp(line []byte) (float64, bool) {
	var x struct {
		Timestamp json.RawMessage `json:"timestamp"`
	}
	if json.Unmarshal(line, &x) != nil || len(x.Timestamp) == 0 {
		return 0, false
	}
	var n float64
	if json.Unmarshal(x.Timestamp, &n) == nil {
		return n, true
	}
	var s string
	if json.Unmarshal(x.Timestamp, &s) != nil {
		return 0, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return float64(t.UnixMilli()), true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, true
	}
	return 0, false
}

// MergeLinesBytes is the history.jsonl merge of 5.3 over byte slices: the union of lines,
// de-duplicated by exact bytes, ordered by each line's "timestamp" (ties and the order of
// the inputs kept), lines without a timestamp at the end in their relative order. The
// result ends with a newline.
func MergeLinesBytes(inputs ...[]byte) []byte {
	type ln struct {
		b   []byte
		ts  float64
		has bool
	}
	seen := map[string]bool{}
	var withTS, without []ln
	for _, in := range inputs {
		for _, l := range bytes.Split(in, []byte("\n")) {
			if len(bytes.TrimSpace(l)) == 0 || seen[string(l)] {
				continue
			}
			seen[string(l)] = true
			ts, ok := lineTimestamp(l)
			if ok {
				withTS = append(withTS, ln{l, ts, true})
			} else {
				without = append(without, ln{l, 0, false})
			}
		}
	}
	sort.SliceStable(withTS, func(i, j int) bool { return withTS[i].ts < withTS[j].ts })
	var out bytes.Buffer
	for _, l := range append(withTS, without...) {
		out.Write(l.b)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// MergeLines merges the history.jsonl at from into path (5.3): path is rewritten atomically
// at its resolved target, from is left for the caller to back up and link.
func (s *Sharer) MergeLines(path, from string) error {
	if err := guard(path, from); err != nil {
		return err
	}
	a, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	target := path
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
		if t, err := filepath.EvalSymlinks(path); err == nil {
			target = t
		}
	}
	return atomicfile.Write(target, MergeLinesBytes(a, b), perm)
}

// MergeDir merges src into dst (5.3). For a folder named `projects` the merge goes down to
// the session-file level (every project folder is merged entry by entry); other folders
// merge at the top level only. See MergeDirDepth.
func (s *Sharer) MergeDir(dst, src, saveDir string) (model.MergeResult, error) {
	depth := 1
	if filepath.Base(dst) == "projects" {
		depth = 2
	}
	return s.MergeDirDepth(dst, src, saveDir, depth)
}

// MergeDirDepth is MergeDir with an explicit depth: entries missing from dst are renamed in
// (never copied inside one file system), entries identical in both move to saveDir (or are
// deleted when saveDir is empty), and entries that differ stay in src and are listed in
// Conflicts, unless both are real directories and depth > 1, when they are merged
// recursively. Names are relative to dst and use "/".
func (s *Sharer) MergeDirDepth(dst, src, saveDir string, depth int) (model.MergeResult, error) {
	var res model.MergeResult
	if err := guard(dst, src, saveDir); err != nil {
		return res, err
	}
	err := mergeInto(dst, src, saveDir, "", depth, &res)
	return res, err
}

func isRealDir(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil || !fi.IsDir() {
		return false
	}
	_, _, isLink := linkInfo(p)
	return !isLink
}

func mergeInto(dst, src, saveDir, rel string, depth int, res *model.MergeResult) error {
	names, err := readDirNames(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, n := range names {
		s, d := filepath.Join(src, n), filepath.Join(dst, n)
		name := n
		if rel != "" {
			name = rel + "/" + n
		}
		if !exists(d) {
			if err := moveAny(s, d); err != nil {
				return err
			}
			res.Moved = append(res.Moved, name)
			continue
		}
		eq, err := TreesEqual(d, s)
		if err != nil {
			return err
		}
		switch {
		case eq:
			if saveDir != "" {
				err = moveAny(s, filepath.Join(saveDir, filepath.FromSlash(name)))
			} else {
				err = removeAny(s)
			}
			if err != nil {
				return err
			}
			res.Dropped = append(res.Dropped, name)
		case depth > 1 && isRealDir(d) && isRealDir(s):
			if err := mergeInto(d, s, saveDir, name, depth-1, res); err != nil {
				return err
			}
		default:
			res.Conflicts = append(res.Conflicts, name)
		}
	}
	return nil
}

// Verdict is how a folder's copy of an item relates to the primary's (5.3 first table).
type Verdict string

// Verdicts.
const (
	VerdictLink      Verdict = "link"      // absent: link it
	VerdictAdopted   Verdict = "adopted"   // already a link resolving to the primary's
	VerdictElsewhere Verdict = "elsewhere" // a link resolving elsewhere: shared elsewhere, untouched
	VerdictReplace   Verdict = "replace"   // identical: replace by a link, original saved
	VerdictConflict  Verdict = "conflict"  // differs
	VerdictNoPrimary Verdict = "no-primary"
)

// Classify applies the first table of 5.3 to folderPath (F/X) against primaryPath (P/X).
func Classify(primaryPath, folderPath string) (Verdict, error) {
	if err := guard(primaryPath, folderPath); err != nil {
		return "", err
	}
	fi, err := os.Lstat(folderPath)
	if os.IsNotExist(err) {
		return VerdictLink, nil
	}
	if err != nil {
		return "", err
	}
	_ = fi
	if !exists(primaryPath) {
		return VerdictNoPrimary, nil
	}
	if _, _, isLink := linkInfo(folderPath); isLink {
		if sameTarget(folderPath, primaryPath) {
			return VerdictAdopted, nil
		}
		return VerdictElsewhere, nil
	}
	eq, err := TreesEqual(folderPath, primaryPath)
	if err != nil {
		return "", err
	}
	if eq {
		return VerdictReplace, nil
	}
	return VerdictConflict, nil
}

// Resolution is a conflict choice (`--conflicts`, `--resolve`).
type Resolution string

// Resolutions of 5.3.
const (
	ResKeep    Resolution = "keep"
	ResPrimary Resolution = "primary"
	ResFolder  Resolution = "folder"
	ResMerge   Resolution = "merge"
)

// ErrMergeRefused is returned for `merge` on an item that cannot be merged.
var ErrMergeRefused = errors.New("share: merge is only for history.jsonl and directories; use primary or folder")

// ResolveResult says what Resolve did.
type ResolveResult struct {
	Linked    bool
	Separate  bool     // left as is, "! separate"
	Conflicts []string // merge: entries still different (the folder is then not linked)
	Merge     model.MergeResult
}

// Resolve applies a resolution of the second table of 5.3 to F/X (folderPath) against P/X
// (primaryPath), saving everything it displaces under saveDir (named after the item). It is
// a composition of the Sharer primitives; apply uses the primitives directly so that every
// step is journaled, and tests use this to prove the table.
func (s *Sharer) Resolve(it Item, primaryPath, folderPath string, res Resolution, saveDir, kind string) (ResolveResult, error) {
	var out ResolveResult
	save := filepath.Join(saveDir, it.Name)
	switch res {
	case ResKeep, "":
		out.Separate = true
		return out, nil
	case ResPrimary:
		if err := s.ReplaceWithLink(folderPath, primaryPath, kind, save); err != nil {
			return out, err
		}
		out.Linked = true
	case ResFolder:
		if err := s.Move(primaryPath, filepath.Join(saveDir, "primary-"+it.Name)); err != nil {
			return out, err
		}
		if err := s.Move(folderPath, primaryPath); err != nil {
			return out, err
		}
		if err := s.Link(folderPath, primaryPath, kind); err != nil {
			return out, err
		}
		out.Linked = true
	case ResMerge:
		switch it.Merge {
		case "lines":
			if err := s.MergeLines(primaryPath, folderPath); err != nil {
				return out, err
			}
			if err := s.ReplaceWithLink(folderPath, primaryPath, kind, save); err != nil {
				return out, err
			}
			out.Linked = true
		case "dir":
			mr, err := s.MergeDir(primaryPath, folderPath, save)
			if err != nil {
				return out, err
			}
			out.Merge, out.Conflicts = mr, mr.Conflicts
			if len(mr.Conflicts) > 0 {
				out.Separate = true
				return out, nil
			}
			// F/X is now empty (or only empty folders): save the husk and link.
			if err := s.ReplaceWithLink(folderPath, primaryPath, kind, filepath.Join(saveDir, "husk-"+it.Name)); err != nil {
				return out, err
			}
			out.Linked = true
		default:
			return out, fmt.Errorf("%w: %s", ErrMergeRefused, it.Name)
		}
	default:
		return out, fmt.Errorf("share: unknown resolution %q", res)
	}
	return out, nil
}

// sameTarget reports whether a and b resolve to the same file or folder. File identity wins
// over comparing resolved paths: on Windows one folder has a short (RUNNER~1) and a long
// spelling, and EvalSymlinks does not always normalise a junction's target the same way.
func sameTarget(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	if err1 == nil && err2 == nil {
		return os.SameFile(fa, fb)
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
