package scan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

// ErrLocked means another `wy scan` holds <state>/scan/scan.lock.
var ErrLocked = errors.New("another wy scan is running")

// FromIdentity converts discovered slots to the slots the scanner takes.
func FromIdentity(slots []identity.Slot) []Slot {
	out := make([]Slot, len(slots))
	for i, s := range slots {
		out[i] = Slot{Folder: s.Name, Dir: s.Dir}
	}
	return out
}

// RunOptions configures Run, the `wy scan` command.
type RunOptions struct {
	Home     string
	StateDir string
	Slots    []Slot
	// AllSlots is every slot of the machine (Slots may be narrowed by --slot). It decides
	// which transcripts folders are shared, so that a narrowed scan does not stamp one slot
	// on rows of a folder several slots share. Empty means Slots.
	AllSlots []Slot
	Outbox   *outbox.Outbox
	Machine  string // machine id, "m_..."
	Client   string // "wy/0.1.0"
	// SeatOf returns the login of a slot for history_import records; nil or a nil result
	// means no seat.
	SeatOf  func(folder string) *record.Seat
	History bool // also send one history_import per slot (once, unless Force)
	Force   bool // resend history even if it was sent before
	Now     func() time.Time
}

// Summary is what Run did.
type Summary struct {
	Files          int      // transcript files that had new bytes
	Bytes          int64    // transcript bytes consumed
	Rows           int      // token rows queued
	Records        int      // records appended to the outbox (all kinds)
	HistorySent    []string // slots whose history_import was queued
	HistorySkipped []string // slots whose history was already sent (and not forced)
}

func (o *RunOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Run scans the transcripts that grew since the last run, appends token_rows records (and
// history_import records when asked) to the outbox, and only then commits the scan offsets,
// so a crash between the two re-sends rows (the hub max-merges them) and never loses any.
// Only one Run can be active per state folder: a second one returns ErrLocked.
func Run(o RunOptions) (Summary, error) {
	var sum Summary
	dir := filepath.Join(o.StateDir, "scan")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return sum, err
	}
	unlock, ok, err := tryLock(filepath.Join(dir, "scan.lock"))
	if err != nil {
		return sum, fmt.Errorf("scan lock: %w", err)
	}
	if !ok {
		return sum, ErrLocked
	}
	defer unlock()

	sc, err := New(Options{Slots: o.Slots, AllSlots: o.AllSlots, OffsetsPath: filepath.Join(dir, "offsets.json"), Home: o.Home})
	if err != nil {
		return sum, err
	}
	res, err := sc.Scan()
	if err != nil {
		return sum, err
	}
	sum.Files, sum.Bytes, sum.Rows = res.Files, res.Bytes, res.Rows

	meta := func(slot string) record.Meta {
		return record.Meta{Machine: o.Machine, Slot: slot, Client: o.Client, Now: o.now()}
	}
	for _, b := range res.Batches {
		n, err := o.appendBatch(b, meta(b.Slot))
		sum.Records += n
		if err != nil {
			return sum, err // offsets not committed: the rows are sent again next time
		}
	}

	if o.History {
		if err := o.sendHistory(dir, &sum, meta); err != nil {
			return sum, err
		}
	}
	// Outbox first, offsets after.
	if err := res.Commit(); err != nil {
		return sum, fmt.Errorf("save scan offsets: %w", err)
	}
	return sum, nil
}

// appendBatch writes one batch as one token_rows record, halving it when the encoded record
// would be over 1 MiB (long cwd or model strings). It returns how many records it wrote.
func (o *RunOptions) appendBatch(b Batch, m record.Meta) (int, error) {
	p := record.TokenRowsPayload{Rows: make([]record.TokenRow, 0, len(b.Rows))}
	for _, r := range b.Rows {
		p.Rows = append(p.Rows, o.tokenRow(r))
	}
	for _, h := range b.LimitHits {
		raw, err := json.Marshal(limitHit(h))
		if err != nil {
			return 0, err
		}
		p.LimitHits = append(p.LimitHits, raw)
	}
	for _, s := range b.Sessions {
		raw, err := json.Marshal(s)
		if err != nil {
			return 0, err
		}
		p.Sessions = append(p.Sessions, raw)
	}
	if b.Source != nil {
		src := *b.Source
		raw, err := json.Marshal(src)
		if err != nil {
			return 0, err
		}
		p.Source = raw
	}
	return o.appendTokenRows(m, p)
}

func (o *RunOptions) appendTokenRows(m record.Meta, p record.TokenRowsPayload) (int, error) {
	r, err := record.NewTokenRows(m, p)
	if err != nil {
		return 0, err
	}
	if _, err := r.Encode(); err == nil {
		if _, err := o.Outbox.Append(r); err != nil {
			return 0, err
		}
		return 1, nil
	} else if len(p.Rows) < 2 {
		return 0, err
	}
	// Too big: split the rows; the extras (limit hits, sessions) ride with the first half.
	mid := len(p.Rows) / 2
	first := record.TokenRowsPayload{Rows: p.Rows[:mid], LimitHits: p.LimitHits, Sessions: p.Sessions, Source: nil}
	second := record.TokenRowsPayload{Rows: p.Rows[mid:]}
	n1, err := o.appendTokenRows(m, first)
	if err != nil {
		return n1, err
	}
	n2, err := o.appendTokenRows(m, second)
	return n1 + n2, err
}

func (o *RunOptions) tokenRow(r TokenRow) record.TokenRow {
	out := record.TokenRow{
		Key: r.Key, TS: record.FormatTime(time.UnixMilli(r.TS)), SessionID: r.SessionID,
		Cwd: tildePath(o.Home, r.CWD), Model: r.Model,
		Input: r.Input, Output: r.Output, Cache5m: r.Cache5m, Cache1h: r.Cache1h, CacheRead: r.CacheRead,
	}
	if r.Thinking > 0 {
		v := r.Thinking
		out.Thinking = &v
	}
	if r.Sidechain {
		v := true
		out.Sidechain = &v
	}
	return out
}

// tildePath writes an absolute path inside home as "~/rest" with "/" separators; anything
// else (empty, outside home) is returned as it is, with "/" separators.
func tildePath(home, p string) string {
	if p == "" || home == "" || !filepath.IsAbs(p) {
		return p
	}
	return identity.Tilde(home, p)
}

func limitHit(h LimitHit) map[string]any {
	m := map[string]any{
		"session_id": h.SessionID,
		"ts":         record.FormatTime(time.UnixMilli(h.TS)),
		"type":       h.Type,
	}
	if h.Status != "" {
		m["status"] = h.Status
	}
	if h.ResetsAt != 0 {
		m["resets_at"] = record.FormatTime(time.UnixMilli(h.ResetsAt))
	}
	return m
}

// historyMarker is <state>/scan/history.json: the slots whose stats-cache.json was sent.
type historyMarker struct {
	V    int               `json:"v"`
	Sent map[string]string `json:"sent"` // slot -> RFC 3339 time it was queued
}

func readMarker(path string) historyMarker {
	m := historyMarker{V: 1, Sent: map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		var x historyMarker
		if json.Unmarshal(b, &x) == nil && x.Sent != nil {
			m = x
			m.V = 1
		}
	}
	return m
}

func (o *RunOptions) sendHistory(dir string, sum *Summary, meta func(string) record.Meta) error {
	markerPath := filepath.Join(dir, "history.json")
	marker := readMarker(markerPath)
	var todo []HistoryImport
	for _, h := range ReadHistory(o.Slots) {
		if _, done := marker.Sent[h.Slot]; done && !o.Force {
			if h.Part <= 1 {
				sum.HistorySkipped = append(sum.HistorySkipped, h.Slot)
			}
			continue
		}
		todo = append(todo, h)
	}
	sent := map[string]bool{}
	for _, h := range todo {
		m := meta(h.Slot)
		if o.SeatOf != nil {
			m.Seat = o.SeatOf(h.Slot)
		}
		r, err := record.NewHistoryImport(m, record.HistoryImportPayload{Source: h.Source, Raw: h.Raw, Part: h.Part, Parts: h.Parts})
		if err != nil {
			return err
		}
		if _, err := o.Outbox.Append(r); err != nil {
			return fmt.Errorf("history_import for %s: %w", h.Slot, err)
		}
		sum.Records++
		sent[h.Slot] = true
	}
	if len(sent) == 0 {
		return nil
	}
	now := record.FormatTime(o.now())
	for s := range sent {
		marker.Sent[s] = now
		sum.HistorySent = append(sum.HistorySent, s)
	}
	sort.Strings(sum.HistorySent)
	b, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	// Written after the records are in the outbox: a crash in between sends history twice,
	// never zero times.
	return atomicfile.Write(markerPath, append(b, '\n'), 0o600)
}
