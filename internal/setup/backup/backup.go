// Package backup is the journal of `sy setup` (docs/spec/setup.md section 7): a backup set
// per run under <state>/backup/<id>/ holding a manifest rewritten atomically after every
// entry, the saved bytes of every file touched, the saved trees, the removed or replaced
// JSON values, and Undo, which reverses a set (7.3, 7.4, 7.6).
//
// The write side is Create / Begin / Tx.Finish: an entry is written with phase "intent"
// and fsynced before the action runs, and "done" afterwards. The read side is List, Open,
// UndoPlan and Undo. Apply (internal/setup/apply) drives the write side; its rollback is
// Undo run on the in-progress set.
//
// The package never opens `.credentials.json`, and a `.claude.json` is handled as metadata
// plus the recorded JSON keys only (never saved whole: it holds the login).
//
// Depends on: internal/atomicfile, internal/setup/model.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/atomicfile"
	"github.com/mraj602/switchyard/internal/setup/model"
)

const manifestName = "manifest.json"

// Header is the run description written into a new set's manifest.
type Header struct {
	Command   string
	SyVersion string
	Machine   string
	PlanID    string
}

// Set is one open backup set. Methods are safe for concurrent use.
type Set struct {
	// Dir is <state>/backup/<id>.
	Dir string

	mu  sync.Mutex
	m   model.Manifest
	obj int
	now func() time.Time
}

// Root returns <state>/backup.
func Root(stateDir string) string { return filepath.Join(stateDir, "backup") }

// Create makes a new set for a run started at now: <state>/backup/<id> (mode 0700; a clash in
// the same second appends -2, -3, ...) with its manifest header (status in-progress).
func Create(stateDir string, h Header, now time.Time) (*Set, error) {
	root := Root(stateDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	base := model.BackupID(now)
	id := base
	for n := 2; ; n++ {
		err := os.Mkdir(filepath.Join(root, id), 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || n > 1000 {
			return nil, err
		}
		id = base + "-" + strconv.Itoa(n)
	}
	s := &Set{Dir: filepath.Join(root, id), now: time.Now}
	for _, d := range []string{"files", "trees", "keys", "watch"} {
		if err := os.Mkdir(filepath.Join(s.Dir, d), 0o700); err != nil {
			return nil, err
		}
	}
	s.m = model.Manifest{
		V: 1, ID: id, Command: h.Command, SyVersion: h.SyVersion, Machine: h.Machine,
		PlanID: h.PlanID, StartedAt: model.TimeString(now), Status: model.StatusInProgress,
		Entries: []model.Entry{},
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
}

// Open loads the set with the given id.
func Open(stateDir, id string) (*Set, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return nil, fmt.Errorf("backup: bad id %q", id)
	}
	dir := filepath.Join(Root(stateDir), id)
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	s := &Set{Dir: dir, now: time.Now}
	if err := json.Unmarshal(b, &s.m); err != nil {
		return nil, fmt.Errorf("backup %s: manifest: %w", id, err)
	}
	for _, d := range []string{"files", "trees", "keys", "watch"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o700)
		ents, _ := os.ReadDir(filepath.Join(dir, d))
		for _, e := range ents {
			name := strings.TrimSuffix(e.Name(), ".json")
			if n, err := strconv.Atoi(name); err == nil && n > s.obj {
				s.obj = n
			}
		}
	}
	for _, e := range s.m.Entries {
		if e.Seq > s.obj {
			s.obj = e.Seq
		}
	}
	return s, nil
}

// ID returns the set id.
func (s *Set) ID() string { return s.m.ID }

// Manifest returns a deep copy of the current manifest.
func (s *Set) Manifest() model.Manifest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyLocked()
}

func (s *Set) copyLocked() model.Manifest {
	b, _ := json.Marshal(s.m)
	var m model.Manifest
	_ = json.Unmarshal(b, &m)
	if m.Entries == nil {
		m.Entries = []model.Entry{}
	}
	return m
}

// SetStatus sets the status; leaving in-progress also stamps finished_at.
func (s *Set) SetStatus(status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m.Status = status
	if status != model.StatusInProgress {
		s.m.FinishedAt = model.TimeString(s.now())
	}
	return s.save()
}

// save rewrites the manifest atomically with fsync (callers hold the lock or own the set).
func (s *Set) save() error {
	b, err := json.MarshalIndent(&s.m, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(s.Dir, manifestName), append(b, '\n'), 0o600)
}

// nextObj returns the next number for a saved object (files/, trees/, keys/).
func (s *Set) nextObj() int {
	s.obj++
	return s.obj
}

func objName(n int) string { return fmt.Sprintf("%06d", n) }

// Abs resolves a path relative to the set (a PathState.Saved value) to an absolute path.
// The result never leaves the set: a "../" in a manifest cannot reach a file outside it.
func (s *Set) Abs(rel string) string {
	sep := string(filepath.Separator)
	return filepath.Join(s.Dir, filepath.Clean(sep+filepath.FromSlash(rel)))
}

// Info is one line of `sy setup --undo-list`.
type Info struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	StartedAt string `json:"started_at"`
	Status    string `json:"status"`
	Entries   int    `json:"entries"`
	// Remaining counts entries not yet reverted.
	Remaining int `json:"remaining"`
}

// List returns every set under <state>/backup, oldest first. A folder without a readable
// manifest is skipped.
func List(stateDir string) ([]Info, error) {
	ents, err := os.ReadDir(Root(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(Root(stateDir), e.Name(), manifestName))
		if err != nil {
			continue
		}
		var m model.Manifest
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		in := Info{ID: e.Name(), Command: m.Command, StartedAt: m.StartedAt, Status: m.Status, Entries: len(m.Entries)}
		for _, en := range m.Entries {
			if en.Phase != model.PhaseReverted {
				in.Remaining++
			}
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return idLess(out[i].ID, out[j].ID) })
	return out, nil
}

// idLess orders ids by time, then by clash suffix numerically.
func idLess(a, b string) bool {
	ba, na := splitID(a)
	bb, nb := splitID(b)
	if ba != bb {
		return ba < bb
	}
	return na < nb
}

func splitID(id string) (string, int) {
	if i := strings.Index(id, "Z-"); i >= 0 {
		n, _ := strconv.Atoi(id[i+2:])
		return id[:i+1], n
	}
	return id, 1
}

// InProgress returns the ids of sets still marked in-progress (7.6), oldest first.
func InProgress(stateDir string) ([]string, error) {
	l, err := List(stateDir)
	var out []string
	for _, i := range l {
		if i.Status == model.StatusInProgress {
			out = append(out, i.ID)
		}
	}
	return out, err
}
