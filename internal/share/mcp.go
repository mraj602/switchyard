package share

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mraj602/switchyard/internal/atomicfile"
	"github.com/mraj602/switchyard/internal/jsonedit"
	"github.com/mraj602/switchyard/internal/setup/model"
)

// Paths under `<state>/share/`.
const (
	mcpBaseName      = "mcp-base.json"
	mcpConflictsName = "mcp-conflicts.jsonl"
)

type serverSet struct {
	order []string
	vals  map[string]json.RawMessage
}

func (s serverSet) canon(name string) string {
	v, ok := canon(s.vals[name])
	if !ok {
		return string(s.vals[name])
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func canon(raw []byte) (any, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return nil, false
	}
	return v, true
}

// parseServers reads a JSON object keeping key order (later duplicates win).
func parseServers(raw []byte) (serverSet, error) {
	set := serverSet{vals: map[string]json.RawMessage{}}
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil {
		return set, err
	}
	if dl, ok := tok.(json.Delim); !ok || dl != '{' {
		return set, errors.New("mcpServers is not an object")
	}
	for d.More() {
		kt, err := d.Token()
		if err != nil {
			return set, err
		}
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return set, err
		}
		k := kt.(string)
		if _, dup := set.vals[k]; !dup {
			set.order = append(set.order, k)
		}
		set.vals[k] = v
	}
	return set, nil
}

func (s serverSet) equal(o serverSet) bool {
	if len(s.vals) != len(o.vals) {
		return false
	}
	for k := range s.vals {
		if _, ok := o.vals[k]; !ok || s.canon(k) != o.canon(k) {
			return false
		}
	}
	return true
}

func (s serverSet) marshal() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, n := range s.order {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(n)
		b.Write(k)
		b.WriteByte(':')
		b.Write(s.vals[n])
	}
	b.WriteByte('}')
	return b.Bytes()
}

type mcpBase struct {
	V       int                        `json:"v"`
	Files   []string                   `json:"files"` // state files of the last synced set
	Servers map[string]json.RawMessage `json:"servers"`
}

func loadBase(stateDir string) (*mcpBase, error) {
	b, err := os.ReadFile(MCPBasePath(stateDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m mcpBase
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil // unreadable base: behave as a first sync (union, nothing removed)
	}
	if m.Servers == nil {
		m.Servers = map[string]json.RawMessage{}
	}
	return &m, nil
}

func saveBase(stateDir string, m mcpBase) error {
	dir := filepath.Join(stateDir, "share")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, mcpBaseName), append(b, '\n'), 0o600)
}

// naturalLess orders "~/.claude-2" before "~/.claude-10".
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		if unicode.IsDigit(rune(a[0])) && unicode.IsDigit(rune(b[0])) {
			i, j := 0, 0
			for i < len(a) && unicode.IsDigit(rune(a[i])) {
				i++
			}
			for j < len(b) && unicode.IsDigit(rune(b[j])) {
				j++
			}
			na, nb := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

type mcpFile struct {
	f   model.MCPFile
	cur serverSet
	// noKey: the state file has no mcpServers key at all (Claude Code recreated it, or the
	// folder never had servers). It receives the merged list but never votes a deletion:
	// only an explicit {} (or a missing name in an object) deletes (5.4).
	noKey bool
}

// MCPBasePath is <state>/share/mcp-base.json.
func MCPBasePath(stateDir string) string { return filepath.Join(stateDir, "share", mcpBaseName) }

// MCPGuard is <state>/share/mcp-guard.json, written when a sync refused to run because it
// would have removed every server of the base from the folders that still have them (for
// `sy doctor`). It is removed by the next sync that runs.
type MCPGuard struct {
	Time    string   `json:"time"`
	Removed []string `json:"removed"` // server names (never values)
	By      []string `json:"by"`      // folders whose state files lack them
}

const mcpGuardName = "mcp-guard.json"

// ReadMCPGuard returns the guard record, nil when the last sync ran.
func ReadMCPGuard(stateDir string) (*MCPGuard, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "share", mcpGuardName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g MCPGuard
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// MCPRemoval is one line of <state>/share/mcp-removed.jsonl: a server of the base the sync
// removed from every folder because one folder deleted it (names only).
type MCPRemoval struct {
	Time string `json:"time"`
	Name string `json:"name"`
	By   string `json:"by"` // folder whose state file no longer had it
}

const mcpRemovedName = "mcp-removed.jsonl"

// ReadMCPRemovals reads the removal log (for `sy doctor`).
func ReadMCPRemovals(stateDir string) ([]MCPRemoval, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "share", mcpRemovedName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []MCPRemoval
	for _, l := range bytes.Split(b, []byte("\n")) {
		var r MCPRemoval
		if len(l) > 0 && json.Unmarshal(l, &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func appendJSONLines(stateDir, name string, lines []any) error {
	if len(lines) == 0 {
		return nil
	}
	dir := filepath.Join(stateDir, "share")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, l := range lines {
		b, _ := json.Marshal(l)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// MCPSyncConflict is one line of `<state>/share/mcp-conflicts.jsonl`: names and files, never
// values.
type MCPSyncConflict struct {
	Time   string `json:"time"`
	Name   string `json:"name"`
	File   string `json:"file"`   // folder whose version lost
	Winner string `json:"winner"` // folder whose version was kept
}

// ReadConflicts reads the conflict log (for `sy doctor`).
func ReadConflicts(stateDir string) ([]MCPSyncConflict, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "share", mcpConflictsName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []MCPSyncConflict
	for _, l := range bytes.Split(b, []byte("\n")) {
		var c MCPSyncConflict
		if len(l) > 0 && json.Unmarshal(l, &c) == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

func appendConflicts(stateDir string, cs []MCPSyncConflict) error {
	if len(cs) == 0 {
		return nil
	}
	dir := filepath.Join(stateDir, "share")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, mcpConflictsName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, c := range cs {
		b, _ := json.Marshal(c)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MCPSync is the three-way mcpServers merge of setup.md 5.4. It never returns server values.
func (s *Sharer) MCPSync(ctx context.Context, req model.MCPSyncRequest) (model.MCPSyncResult, error) {
	var res model.MCPSyncResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	stateDir := req.StateDir
	if stateDir == "" {
		stateDir = s.StateDir
	}
	if stateDir == "" {
		return res, errors.New("share: MCPSync needs a state dir")
	}

	// Read every state file's mcpServers (missing = {}); a file that is absent or unreadable
	// does not take part and is reported.
	var files []*mcpFile
	incomplete := false
	for _, f := range req.Files {
		if err := guard(f.StateFile); err != nil {
			return res, err
		}
		doc, err := os.ReadFile(f.StateFile)
		if err != nil {
			res.Skipped = append(res.Skipped, model.MCPSkip{File: f.StateFile, Reason: "no-state-file"})
			continue
		}
		raw, ok, err := jsonedit.Get(doc, "/mcpServers")
		if err != nil {
			res.Skipped = append(res.Skipped, model.MCPSkip{File: f.StateFile, Reason: "unreadable"})
			incomplete = true
			continue
		}
		set := serverSet{vals: map[string]json.RawMessage{}}
		if ok {
			if set, err = parseServers(raw); err != nil {
				res.Skipped = append(res.Skipped, model.MCPSkip{File: f.StateFile, Reason: "unreadable"})
				incomplete = true
				continue
			}
		}
		files = append(files, &mcpFile{f: f, cur: set, noKey: !ok})
	}
	if len(files) == 0 {
		return res, nil
	}
	// Primary first, the rest in natural folder order.
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].f.Primary != files[j].f.Primary {
			return files[i].f.Primary
		}
		return naturalLess(files[i].f.Folder, files[j].f.Folder)
	})

	base, err := loadBase(stateDir)
	if err != nil {
		return res, err
	}
	merged := serverSet{vals: map[string]json.RawMessage{}}
	var conflicts []MCPSyncConflict
	conflictNames := map[string]bool{}
	deletedBy := map[string]string{} // base server -> folder whose deletion won
	logConflict := func(name, loser, winner string) {
		conflicts = append(conflicts, MCPSyncConflict{Time: time.Now().UTC().Format(time.RFC3339), Name: name, File: loser, Winner: winner})
		conflictNames[name] = true
	}
	known := map[string]bool{} // state files of the last synced set
	if base != nil {
		for _, p := range base.Files {
			known[p] = true
		}
	}
	var baseView serverSet // for the name changes
	if base == nil {
		// First sync: union, same-name differences resolve to the primary (then the lowest folder).
		winner := map[string]*mcpFile{}
		for _, f := range files {
			for _, n := range f.cur.order {
				if w, ok := winner[n]; ok {
					if w.cur.canon(n) != f.cur.canon(n) {
						logConflict(n, f.f.Folder, w.f.Folder)
					}
					continue
				}
				winner[n] = f
				merged.order = append(merged.order, n)
				merged.vals[n] = f.cur.vals[n]
			}
		}
		baseView = files[0].cur
	} else {
		bset := serverSet{vals: base.Servers}
		baseView = bset
		names := map[string]bool{}
		for n := range base.Servers {
			names[n] = true
		}
		for _, f := range files {
			for n := range f.cur.vals {
				names[n] = true
			}
		}
		for _, n := range sortedKeys(names) {
			bv, inBase := base.Servers[n]
			bc := ""
			if inBase {
				bc = bset.canon(n)
			}
			type vote struct {
				f       *mcpFile
				deleted bool
				key     string
			}
			var votes []vote
			for _, f := range files {
				v, in := f.cur.vals[n]
				switch {
				case !inBase && in:
					votes = append(votes, vote{f, false, f.cur.canon(n)})
				case inBase && !in && known[f.f.StateFile] && !f.noKey:
					votes = append(votes, vote{f, true, "\x00deleted"})
				case inBase && in && f.cur.canon(n) != bc:
					_ = v
					votes = append(votes, vote{f, false, f.cur.canon(n)})
				}
			}
			if len(votes) == 0 {
				if inBase {
					merged.order = append(merged.order, n)
					merged.vals[n] = bv
				}
				continue
			}
			win := votes[0] // files are primary-first, then natural order
			for _, v := range votes[1:] {
				if v.key != win.key {
					logConflict(n, v.f.f.Folder, win.f.f.Folder)
				}
			}
			if !win.deleted {
				merged.order = append(merged.order, n)
				merged.vals[n] = win.f.cur.vals[n]
			} else {
				deletedBy[n] = win.f.f.Folder
			}
		}
		// Present order: primary's order first, then the others.
		var ordered []string
		seen := map[string]bool{}
		for _, f := range files {
			for _, n := range f.cur.order {
				if _, ok := merged.vals[n]; ok && !seen[n] {
					seen[n] = true
					ordered = append(ordered, n)
				}
			}
		}
		for _, n := range merged.order {
			if !seen[n] {
				seen[n] = true
				ordered = append(ordered, n)
			}
		}
		merged.order = ordered
	}

	// Names that changed relative to the base (names only).
	for _, n := range merged.order {
		if _, ok := baseView.vals[n]; !ok {
			res.Names.Added = append(res.Names.Added, n)
		} else if baseView.canon(n) != merged.canon(n) {
			res.Names.Changed = append(res.Names.Changed, n)
		}
	}
	for _, n := range baseView.order {
		if _, ok := merged.vals[n]; !ok {
			res.Names.Removed = append(res.Names.Removed, n)
		}
	}
	if base != nil {
		for _, n := range sortedKeys(base.Servers) {
			if _, ok := merged.vals[n]; !ok && !contains(res.Names.Removed, n) {
				res.Names.Removed = append(res.Names.Removed, n)
			}
		}
	}
	sort.Strings(res.Names.Added)
	sort.Strings(res.Names.Changed)
	sort.Strings(res.Names.Removed)
	res.Conflicts = sortedKeys(conflictNames)

	// Guard: a pass that would strip every server of the base from the folders that still
	// have them is refused and reported (`sy doctor`): a state file reset or restored without
	// them must not empty every folder. Removing them all is done by hand in each folder.
	if base != nil && len(base.Servers) > 0 {
		var lost []string
		for _, n := range sortedKeys(base.Servers) {
			if _, ok := merged.vals[n]; !ok {
				lost = append(lost, n)
			}
		}
		strip := false
		for _, f := range files {
			for _, n := range lost {
				if _, ok := f.cur.vals[n]; ok {
					strip = true
				}
			}
		}
		if len(lost) == len(base.Servers) && strip {
			res.Guard = "would-remove-all"
			var by []string
			for _, f := range files {
				if !f.noKey && known[f.f.StateFile] && len(f.cur.vals) < len(base.Servers) {
					by = append(by, f.f.Folder)
				}
			}
			for _, f := range files {
				res.Skipped = append(res.Skipped, model.MCPSkip{File: f.f.StateFile, Reason: "would-remove-all-servers"})
			}
			res.Written = nil
			if req.DryRun {
				return res, nil
			}
			g := MCPGuard{Time: time.Now().UTC().Format(time.RFC3339), Removed: lost, By: by}
			b, _ := json.MarshalIndent(g, "", "  ")
			if err := os.MkdirAll(filepath.Join(stateDir, "share"), 0o700); err != nil {
				return res, err
			}
			return res, atomicfile.Write(filepath.Join(stateDir, "share", mcpGuardName), append(b, '\n'), 0o600)
		}
	}

	// Write the merged object to each file whose mcpServers differs.
	payload := merged.marshal()
	for _, f := range files {
		if f.cur.equal(merged) {
			continue
		}
		if req.DryRun {
			res.Written = append(res.Written, f.f.StateFile)
			continue
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		_, err := jsonedit.Apply(f.f.StateFile, jsonedit.Options{Claude: true, Folder: f.f.Dir, IgnoreLive: req.Now},
			jsonedit.Op{Pointer: "/mcpServers", Value: payload})
		var se *jsonedit.SkipError
		switch {
		case errors.As(err, &se):
			res.Skipped = append(res.Skipped, model.MCPSkip{File: f.f.StateFile, Reason: se.Reason})
			incomplete = true
		case err != nil:
			return res, fmt.Errorf("share: mcp sync %s: %w", f.f.Folder, err)
		default:
			res.Written = append(res.Written, f.f.StateFile)
		}
	}
	if req.DryRun {
		return res, nil
	}

	// The merged list becomes the base only when every file now holds it.
	baseChanged := false
	if !incomplete {
		nb := mcpBase{V: 1, Servers: map[string]json.RawMessage{}}
		for _, f := range files {
			nb.Files = append(nb.Files, f.f.StateFile)
		}
		sort.Strings(nb.Files)
		for _, n := range merged.order {
			nb.Servers[n] = merged.vals[n]
		}
		if base == nil || !reflect.DeepEqual(sortedStrings(base.Files), nb.Files) ||
			!(serverSet{vals: base.Servers}).equal(serverSet{vals: nb.Servers, order: merged.order}) {
			if err := saveBase(stateDir, nb); err != nil {
				return res, err
			}
			baseChanged = true
		}
	}
	if len(res.Written) > 0 || baseChanged {
		if err := appendConflicts(stateDir, conflicts); err != nil {
			return res, err
		}
		var removals []any
		now := time.Now().UTC().Format(time.RFC3339)
		for _, n := range sortedKeys(deletedBy) {
			removals = append(removals, MCPRemoval{Time: now, Name: n, By: deletedBy[n]})
		}
		if err := appendJSONLines(stateDir, mcpRemovedName, removals); err != nil {
			return res, err
		}
	}
	if !incomplete {
		_ = os.Remove(filepath.Join(stateDir, "share", mcpGuardName))
	}
	return res, nil
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
