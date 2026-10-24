package scan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// MaxRowsPerBatch is the most rows one token_rows record may carry (record.md 4.4).
const MaxRowsPerBatch = 2000

// maxLine is the longest transcript line parsed; longer lines are skipped.
const maxLine = 256 << 20

// Slot is one Claude Code config folder.
type Slot struct {
	// Folder is the slot name as written in records: "~/.claude", "~/.claude-2".
	Folder string
	// Dir is the absolute path of the folder.
	Dir string
}

var slotRE = regexp.MustCompile(`^\.claude([-_].+)?$`)

// DiscoverSlots lists ~/.claude and its ~/.claude-* / ~/.claude_* siblings (one per login),
// sorted by name.
func DiscoverSlots(home string) []Slot {
	ents, _ := os.ReadDir(home)
	var out []Slot
	for _, e := range ents {
		if !slotRE.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(home, e.Name())
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, Slot{Folder: "~/" + e.Name(), Dir: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out
}

// Options configures a Scanner.
type Options struct {
	// Slots to scan. Each slot's <Dir>/projects is resolved with symlinks; slots that share
	// one projects folder are scanned once and their batches carry no slot (record.md 4.4).
	Slots []Slot
	// AllSlots is every slot of the machine, a superset of Slots. Which projects folders are
	// shared is decided over AllSlots, then only the folders of Slots are scanned, so
	// `wy scan --slot X` does not stamp X on rows of a folder that X shares with other
	// slots. Empty means Slots.
	AllSlots []Slot
	// OffsetsPath is the JSON file holding per-file byte offsets (<state>/scan/offsets.json).
	// Empty means no persistence: every Scan starts from 0.
	OffsetsPath string
	// Home is used to write paths with "~" in Source. Optional.
	Home string
}

// Source is the transcript file and byte range behind a batch (debugging only).
type Source struct {
	Path string `json:"path"`
	From int64  `json:"from"`
	To   int64  `json:"to"`
}

// Batch becomes one token_rows record.
type Batch struct {
	Slot      string // "" when the transcripts folder is shared by several slots
	Rows      []TokenRow
	LimitHits []LimitHit
	Sessions  []SessionInfo
	Source    *Source // set when every row came from a single file
}

// Result is the outcome of one Scan. Offsets are only persisted by Commit, so the caller
// can write the records to the outbox first and lose nothing if it crashes in between.
type Result struct {
	Batches []Batch
	Files   int   // files that had new bytes
	Bytes   int64 // bytes consumed
	Rows    int

	sc      *Scanner
	updates map[string]fileState
}

// Commit persists the new offsets. A no-op without OffsetsPath.
func (r *Result) Commit() error {
	if r.sc.opts.OffsetsPath == "" || len(r.updates) == 0 {
		return nil
	}
	for p, st := range r.updates {
		r.sc.offsets[p] = st
	}
	r.updates = nil
	return r.sc.saveOffsets()
}

type fileState struct {
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"` // epoch ms
	Offset int64  `json:"offset"`
	Head   uint64 `json:"head,omitempty"` // fingerprint of the first bytes: detects rotation
}

type offsetsFile struct {
	V     int                  `json:"v"`
	Files map[string]fileState `json:"files"`
}

// Scanner scans transcripts incrementally.
type Scanner struct {
	opts    Options
	offsets map[string]fileState
}

// New loads the offsets file (a missing or corrupt file means start from 0).
func New(opts Options) (*Scanner, error) {
	s := &Scanner{opts: opts, offsets: map[string]fileState{}}
	if opts.OffsetsPath != "" {
		if b, err := os.ReadFile(opts.OffsetsPath); err == nil {
			var f offsetsFile
			if json.Unmarshal(b, &f) == nil && f.V == 1 && f.Files != nil {
				s.offsets = f.Files
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return s, nil
}

func (s *Scanner) saveOffsets() error {
	b, err := json.Marshal(offsetsFile{V: 1, Files: s.offsets})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.opts.OffsetsPath), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.opts.OffsetsPath, b, 0o600)
}

type root struct {
	path string
	slot string
}

func (s *Scanner) roots() []root {
	all := s.opts.AllSlots
	if len(all) == 0 {
		all = s.opts.Slots
	}
	byPath := map[string][]string{}
	var order []string
	for _, sl := range all {
		p, err := filepath.EvalSymlinks(filepath.Join(sl.Dir, "projects"))
		if err != nil {
			continue
		}
		if _, seen := byPath[p]; !seen {
			order = append(order, p)
		}
		byPath[p] = append(byPath[p], sl.Folder)
	}
	wanted := map[string]bool{} // the folders to scan, by resolved path
	for _, sl := range s.opts.Slots {
		if p, err := filepath.EvalSymlinks(filepath.Join(sl.Dir, "projects")); err == nil {
			wanted[p] = true
		}
	}
	var out []root
	for _, p := range order {
		if !wanted[p] {
			continue
		}
		r := root{path: p}
		if len(byPath[p]) == 1 {
			r.slot = byPath[p][0]
		}
		out = append(out, r)
	}
	return out
}

func walk(dir string, fn func(path string, info fs.FileInfo)) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			walk(p, fn)
		} else if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
			if info, err := e.Info(); err == nil {
				fn(p, info)
			}
		}
	}
}

// headPrint fingerprints the file's first line (up to 4 KiB), which never changes while the
// file only grows. 0 means the first line is not complete yet.
func headPrint(f *os.File, size int64) uint64 {
	buf := make([]byte, min(size, 4096))
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0
	}
	i := bytes.IndexByte(buf[:n], '\n')
	if i < 0 {
		if n < 4096 {
			return 0
		}
		i = n
	}
	h := fnv.New64a()
	h.Write(buf[:i])
	return h.Sum64()
}

// rootAgg collects one scan of one projects folder.
type rootAgg struct {
	slot    string
	rows    []TokenRow
	rowIdx  map[string]int
	rowFile []int // index into files, for each row's first appearance
	hits    []LimitHit
	hitSeen map[string]bool
	sess    map[string]*SessionInfo
	sessOrd []string
	files   []Source
}

func (a *rootAgg) session(id string) *SessionInfo {
	si, ok := a.sess[id]
	if !ok {
		si = &SessionInfo{SessionID: id}
		a.sess[id] = si
		a.sessOrd = append(a.sessOrd, id)
	}
	return si
}

// Scan reads every transcript that grew since the last Commit and returns the new facts,
// deduplicated per API response (max-merged counters) and chunked into batches.
func (s *Scanner) Scan() (*Result, error) {
	res := &Result{sc: s, updates: map[string]fileState{}}
	for _, r := range s.roots() {
		agg := &rootAgg{slot: r.slot, rowIdx: map[string]int{}, hitSeen: map[string]bool{}, sess: map[string]*SessionInfo{}}
		walk(r.path, func(p string, info fs.FileInfo) { s.scanFile(res, agg, p, info) })
		res.Batches = append(res.Batches, agg.batches()...)
	}
	for _, b := range res.Batches {
		res.Rows += len(b.Rows)
	}
	return res, nil
}

func (s *Scanner) scanFile(res *Result, agg *rootAgg, p string, info fs.FileInfo) {
	size, mtime := info.Size(), info.ModTime().UnixMilli()
	prev, had := s.offsets[p]
	f, err := os.Open(p)
	if err != nil {
		return // vanished or unreadable: skip
	}
	defer f.Close()
	head := headPrint(f, size)
	off := prev.Offset
	// New, truncated, rotated or rewritten: start over.
	if !had || off > size || (prev.Head != 0 && head != 0 && prev.Head != head) {
		off = 0
	}
	if had && prev.Size == size && prev.MTime == mtime && off == size {
		return
	}
	start := off
	sessOfFile := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	fileIdx := len(agg.files)
	br := bufio.NewReaderSize(io.NewSectionReader(f, off, size-off), 1<<20)
	for {
		line, complete := readLine(br)
		if !complete {
			break // trailing partial line: wait for the rest
		}
		off += int64(len(line)) + 1
		if len(line) > maxLine {
			continue
		}
		pr, ok := ParseLine(string(line))
		if !ok {
			continue
		}
		switch {
		case pr.Row != nil:
			if i, dup := agg.rowIdx[pr.Row.Key]; dup {
				agg.rows[i].merge(*pr.Row)
			} else {
				agg.rowIdx[pr.Row.Key] = len(agg.rows)
				agg.rows = append(agg.rows, *pr.Row)
				agg.rowFile = append(agg.rowFile, fileIdx)
			}
		case pr.Hit != nil:
			k := fmt.Sprintf("%s|%d", pr.Hit.SessionID, pr.Hit.TS)
			if !agg.hitSeen[k] {
				agg.hitSeen[k] = true
				agg.hits = append(agg.hits, *pr.Hit)
			}
		case pr.Owner != nil:
			si := agg.session(pr.Owner.SessionID)
			if si.OwnerAccount == "" && si.OwnerOrg == "" { // first one wins
				si.OwnerAccount, si.OwnerOrg = pr.Owner.OwnerAccount, pr.Owner.OwnerOrg
			}
		case pr.Remote != nil:
			id := pr.Remote.SessionID
			if id == "" {
				id = sessOfFile
			}
			agg.session(id).RemoteURL = pr.Remote.RemoteURL // last one wins
		}
	}
	agg.files = append(agg.files, Source{Path: s.display(p), From: start, To: off})
	res.Files++
	res.Bytes += off - start
	res.updates[p] = fileState{Size: size, MTime: mtime, Offset: off, Head: head}
}

// readLine returns the next line without its newline. complete is false when the data ends
// without a newline (the line is not consumed) or at EOF.
func readLine(br *bufio.Reader) (line []byte, complete bool) {
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return nil, false
		}
		return line[:len(line)-1], true
	}
}

func (s *Scanner) display(p string) string {
	if s.opts.Home == "" {
		return p
	}
	homes := []string{s.opts.Home}
	if r, err := filepath.EvalSymlinks(s.opts.Home); err == nil && r != s.opts.Home {
		homes = append(homes, r) // macOS: /var/... is /private/var/...
	}
	for _, h := range homes {
		if rel, err := filepath.Rel(h, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}

func (a *rootAgg) batches() []Batch {
	var sessions []SessionInfo
	for _, id := range a.sessOrd {
		sessions = append(sessions, *a.sess[id])
	}
	var out []Batch
	for i := 0; i < len(a.rows); i += MaxRowsPerBatch {
		j := min(i+MaxRowsPerBatch, len(a.rows))
		b := Batch{Slot: a.slot, Rows: a.rows[i:j:j]}
		single := true
		for _, f := range a.rowFile[i:j] {
			if f != a.rowFile[i] {
				single = false
				break
			}
		}
		if single {
			src := a.files[a.rowFile[i]]
			b.Source = &src
		}
		out = append(out, b)
	}
	if len(out) == 0 && (len(a.hits) > 0 || len(sessions) > 0) {
		out = append(out, Batch{Slot: a.slot})
	}
	if len(out) > 0 {
		out[0].LimitHits, out[0].Sessions = a.hits, sessions
	}
	return out
}
