package outbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FlushLock is the machine-wide flusher lock (outbox.md section 5): only the holder may
// advance the cursor or compact day files.
type FlushLock struct{ f *os.File }

// TryFlushLock takes flush.lock without blocking. ok is false if another process holds it.
func (o *Outbox) TryFlushLock() (l *FlushLock, ok bool, err error) {
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(o.Dir, "flush.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	ok, err = tryLock(f)
	if !ok || err != nil {
		f.Close()
		return nil, false, err
	}
	return &FlushLock{f}, true, nil
}

// Release drops the lock.
func (l *FlushLock) Release() error {
	unlock(l.f)
	return l.f.Close()
}

// CapResult reports what EnforceCap did.
type CapResult struct {
	UnflushedBefore int64
	UnflushedAfter  int64
	Dropped         map[string]int // kind -> records dropped
	Compacted       []string       // files rewritten
}

const sessionListKeep = time.Hour

// capEntry is one line of an unflushed day file, as seen by the cap policy.
type capEntry struct {
	file    string
	off     int64
	size    int64
	kind    string
	ts      string
	slot    string
	session string
}

// EnforceCap applies the size cap of outbox.md section 6. The caller holds the flush lock.
// When the unflushed part of the day files and spill files is over maxBytes, it drops
// records from the oldest unflushed day files, in this order, until the total fits:
// session_list older than an hour that is not the newest of its slot, limit_sample except
// the newest per session per hour, history_import. hook_event, token_rows, slot_info,
// web_usage and unknown kinds are never dropped: a dropped token_rows record would be lost
// for good, because wy scan has already moved its transcript offsets past those bytes. The newest day file and today's
// file are never compacted. A compacted file loses its already-flushed prefix.
func (o *Outbox) EnforceCap(maxBytes int64) (CapResult, error) {
	res := CapResult{Dropped: map[string]int{}}
	days, spills, err := o.list()
	if err != nil {
		return res, err
	}
	cur := o.ReadCursor()
	var unflushed []string
	for _, d := range days {
		if d >= cur.File {
			unflushed = append(unflushed, d)
		}
	}
	startOf := func(name string) int64 {
		if name == cur.File {
			return cur.Offset
		}
		return 0
	}
	total := int64(0)
	for _, n := range unflushed {
		if st, err := os.Stat(filepath.Join(o.Dir, n)); err == nil && st.Size() > startOf(n) {
			total += st.Size() - startOf(n)
		}
	}
	for _, n := range spills {
		if st, err := os.Stat(filepath.Join(o.Dir, n)); err == nil {
			total += st.Size()
		}
	}
	res.UnflushedBefore, res.UnflushedAfter = total, total
	if total <= maxBytes || len(unflushed) < 2 {
		return res, nil
	}

	today := o.now().UTC().Format(dayLayout) + ".jsonl"
	newest := unflushed[len(unflushed)-1]
	var entries []capEntry
	newestList := map[string]string{} // slot -> newest session_list ts, over all unflushed files
	for _, n := range unflushed {
		eligible := n != newest && n != today
		err := o.readLines(n, startOf(n), func(ln line) bool {
			var h struct {
				Kind    string `json:"kind"`
				TS      string `json:"ts"`
				Slot    string `json:"slot"`
				Payload struct {
					SessionID string `json:"session_id"`
				} `json:"payload"`
			}
			if json.Unmarshal(ln.raw, &h) != nil {
				return true
			}
			if h.Kind == "session_list" && h.TS > newestList[h.Slot] {
				newestList[h.Slot] = h.TS
			}
			if eligible {
				entries = append(entries, capEntry{n, ln.start, ln.end - ln.start, h.Kind, h.TS, h.Slot, h.Payload.SessionID})
			}
			return true
		})
		if err != nil {
			return res, err
		}
	}

	drop := map[string]map[int64]bool{} // file -> offsets
	remaining := total
	mark := func(e capEntry) {
		if drop[e.file] == nil {
			drop[e.file] = map[int64]bool{}
		}
		drop[e.file][e.off] = true
		remaining -= e.size
		res.Dropped[e.kind]++
	}
	// limit_sample: the newest per (session, hour) stays.
	keepSample := map[string]capEntry{}
	for _, e := range entries {
		if e.kind == "limit_sample" {
			k := e.session + "|" + hourOf(e.ts)
			if cur, ok := keepSample[k]; !ok || e.ts >= cur.ts {
				keepSample[k] = e
			}
		}
	}
	oldList := o.now().Add(-sessionListKeep).UTC().Format("2006-01-02T15:04:05.000Z")
	passes := []func(capEntry) bool{
		func(e capEntry) bool {
			return e.kind == "session_list" && e.ts < oldList && e.ts < newestList[e.slot]
		},
		func(e capEntry) bool {
			if e.kind != "limit_sample" {
				return false
			}
			k := keepSample[e.session+"|"+hourOf(e.ts)]
			return k.file != e.file || k.off != e.off
		},
		func(e capEntry) bool { return e.kind == "history_import" },
	}
	for _, want := range passes {
		for _, e := range entries { // entries are in file, offset order: oldest first
			if remaining <= maxBytes {
				break
			}
			if want(e) && !drop[e.file][e.off] {
				mark(e)
			}
		}
	}
	if len(drop) == 0 {
		return res, nil
	}
	files := make([]string, 0, len(drop))
	for n := range drop {
		files = append(files, n)
	}
	sort.Strings(files)
	for _, n := range files {
		if err := o.compactFile(n, cur, drop[n]); err != nil {
			return res, err
		}
		res.Compacted = append(res.Compacted, n)
	}
	res.UnflushedAfter = remaining
	return res, nil
}

func hourOf(ts string) string {
	if len(ts) >= 13 {
		return ts[:13]
	}
	return ts
}

// compactFile rewrites the unflushed part of one day file without the lines at the dropped
// offsets (outbox.md section 6, "Compaction of a file"). It holds the file's exclusive lock
// for the whole rewrite, re-reads the file under the lock so that records appended since the
// analysis are kept, and moves the cursor to offset 0 if it was in this file.
func (o *Outbox) compactFile(name string, cur Cursor, drop map[int64]bool) error {
	path := filepath.Join(o.Dir, name)
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := lockBlocking(f); err != nil {
		return err
	}
	defer unlock(f)

	var from int64
	if name == cur.File {
		from = cur.Offset
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if from > st.Size() {
		from = 0
	}
	data := make([]byte, st.Size()-from)
	if _, err := f.ReadAt(data, from); err != nil && len(data) > 0 {
		return err
	}
	var out bytes.Buffer
	for pos := int64(0); pos < int64(len(data)); {
		i := bytes.IndexByte(data[pos:], '\n')
		end := int64(len(data))
		if i >= 0 {
			end = pos + int64(i) + 1
		}
		if !drop[from+pos] { // the torn tail without a newline is kept as it is
			out.Write(data[pos:end])
		}
		pos = end
	}
	// The cursor goes to (name, 0) BEFORE the rename. In the other order a crash between
	// the two leaves an offset that points into the middle of (or past) the shorter file and
	// the records in front of it are skipped. This way a crash at worst resends records the
	// hub has already answered, and the hub dedupes by id.
	if cur.File == name {
		c := cur
		c.Offset = 0
		c.Updated = ""
		if err := o.WriteCursor(c); err != nil {
			return err
		}
	}
	if err := o.writeFileAtomicNamed(name+".compact.tmp", name, out.Bytes()); err != nil {
		return err
	}
	o.forgetCorruptMark(name) // offsets in the rewritten file are new
	return nil
}

// writeFileAtomicNamed writes data to tmp, syncs, renames over dst and syncs the folder.
func (o *Outbox) writeFileAtomicNamed(tmp, dst string, data []byte) error {
	tp := filepath.Join(o.Dir, tmp)
	f, err := os.OpenFile(tp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tp)
		return err
	}
	if o.beforeCompactRename != nil {
		o.beforeCompactRename()
	}
	if err := os.Rename(tp, filepath.Join(o.Dir, dst)); err != nil {
		return err
	}
	return syncDir(o.Dir)
}

// Prune deletes day files that lie wholly before the cursor and are older than keepDays
// (by the date in the name), and leftover *.compact.tmp files from a crashed compaction.
func (o *Outbox) Prune(keepDays int) (removed []string, err error) {
	days, _, err := o.list()
	if err != nil {
		return nil, err
	}
	cur := o.ReadCursor()
	cutoff := o.now().UTC().AddDate(0, 0, -keepDays).Format(dayLayout) + ".jsonl"
	for _, d := range days {
		if cur.File != "" && d < cur.File && d < cutoff {
			if err := os.Remove(filepath.Join(o.Dir, d)); err == nil {
				removed = append(removed, d)
			}
		}
	}
	if ents, err := os.ReadDir(o.Dir); err == nil {
		for _, e := range ents {
			n := e.Name()
			switch {
			case strings.HasSuffix(n, ".compact.tmp"):
				_ = os.Remove(filepath.Join(o.Dir, n))
			case strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".tmp"):
				// A crashed atomic write (atomicfile.TempName); a young one may be in use.
				if fi, err := e.Info(); err == nil && o.now().Sub(fi.ModTime()) > time.Hour {
					_ = os.Remove(filepath.Join(o.Dir, n))
				}
			}
		}
	}
	return removed, nil
}
