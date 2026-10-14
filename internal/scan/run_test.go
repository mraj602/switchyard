package scan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/record"
)

// runEnv is a fake home with ~/.claude holding the redacted capture transcripts.
type runEnv struct {
	home, state string
	slot        Slot
	ob          *outbox.Outbox
}

func newRunEnv(t *testing.T) runEnv {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	proj := filepath.Join(dir, "projects", "-home-user-p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob("../../testdata/transcripts/*.jsonl")
	if len(m) == 0 {
		t.Fatal("no transcript fixtures")
	}
	for _, p := range m {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proj, filepath.Base(p)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	state := t.TempDir()
	return runEnv{home: home, state: state, slot: Slot{Folder: "~/.claude", Dir: dir},
		ob: outbox.New(filepath.Join(state, "outbox"))}
}

func (e runEnv) opts() RunOptions {
	return RunOptions{Home: e.home, StateDir: e.state, Slots: []Slot{e.slot}, Outbox: e.ob,
		Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", Client: "sy/test"}
}

func readAll(t *testing.T, ob *outbox.Outbox) []*record.Record {
	t.Helper()
	b, err := ob.Read(outbox.Cursor{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []*record.Record
	for _, it := range b.Items {
		var r record.Record
		if err := json.Unmarshal(it.Raw, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, &r)
	}
	return out
}

func TestRunWritesTokenRowsThenCommits(t *testing.T) {
	e := newRunEnv(t)
	sum, err := Run(e.opts())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Rows == 0 || sum.Records == 0 || sum.Files != 3 {
		t.Fatalf("summary %+v", sum)
	}
	recs := readAll(t, e.ob)
	if len(recs) != sum.Records {
		t.Fatalf("%d records in the outbox, summary says %d", len(recs), sum.Records)
	}
	rows := 0
	for _, r := range recs {
		if r.Kind != record.KindTokenRows {
			t.Fatalf("kind %s", r.Kind)
		}
		if r.Machine != "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K" || r.Client != "sy/test" || r.Seat != nil {
			t.Errorf("envelope %+v", r)
		}
		if r.Slot != "~/.claude" {
			t.Errorf("slot %q", r.Slot)
		}
		if err := r.Validate(); err != nil || len(r.ID) != 26 {
			t.Errorf("record not valid: %v %q", err, r.ID)
		}
		var p record.TokenRowsPayload
		if err := r.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		for _, row := range p.Rows {
			rows++
			if row.Key == "" || row.SessionID == "" || !strings.HasSuffix(row.TS, "Z") {
				t.Errorf("row %+v", row)
			}
			if row.Cwd != "" && strings.HasPrefix(row.Cwd, "/home/") && strings.HasPrefix(row.Cwd, e.home) {
				t.Errorf("cwd not ~-normalised: %q", row.Cwd)
			}
		}
		if len(p.Source) == 0 && len(p.Rows) > 0 {
			t.Log("row batch without a single source (several files)")
		}
	}
	if rows != sum.Rows {
		t.Fatalf("%d rows in records, summary %d", rows, sum.Rows)
	}

	// Offsets were committed: a second run finds nothing and writes nothing.
	sum2, err := Run(e.opts())
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Rows != 0 || sum2.Records != 0 || sum2.Files != 0 {
		t.Fatalf("second run %+v", sum2)
	}
	if got := len(readAll(t, e.ob)); got != len(recs) {
		t.Fatalf("second run appended records: %d -> %d", len(recs), got)
	}
}

// Outbox first, offsets after: when the outbox cannot be written, no offset is saved.
func TestRunDoesNotCommitWhenOutboxFails(t *testing.T) {
	e := newRunEnv(t)
	// A file where the outbox folder should be: every Append fails.
	bad := filepath.Join(e.state, "blocked")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := e.opts()
	o.Outbox = outbox.New(filepath.Join(bad, "outbox"))
	if _, err := Run(o); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(filepath.Join(e.state, "scan", "offsets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("offsets were committed after a failed append: %v", err)
	}
	// With a working outbox the same rows come out: nothing was lost.
	sum, err := Run(e.opts())
	if err != nil || sum.Rows == 0 {
		t.Fatalf("retry: %+v %v", sum, err)
	}
}

func TestRunLock(t *testing.T) {
	e := newRunEnv(t)
	if err := os.MkdirAll(filepath.Join(e.state, "scan"), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, ok, err := tryLock(filepath.Join(e.state, "scan", "scan.lock"))
	if err != nil || !ok {
		t.Fatalf("take lock: %v %v", ok, err)
	}
	if _, err := Run(e.opts()); !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked while another scan holds the lock, got %v", err)
	}
	unlock()
	if _, err := Run(e.opts()); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
}

func TestRunHistoryOnce(t *testing.T) {
	e := newRunEnv(t)
	stats := `{"version":2,"lastComputedDate":"2026-10-11","dailyActivity":[{"date":"2026-10-11","messageCount":3}],"dailyModelTokens":[{"date":"2026-10-11","tokensByModel":{"m":5}}],"iguana_necktie":{"x":1}}`
	if err := os.WriteFile(filepath.Join(e.slot.Dir, "stats-cache.json"), []byte(stats), 0o644); err != nil {
		t.Fatal(err)
	}
	seat := &record.Seat{Account: "a", Org: "o", Src: "claude_json"}
	hist := func(force bool) (Summary, []*record.Record) {
		o := e.opts()
		o.History, o.Force = true, force
		o.SeatOf = func(string) *record.Seat { return seat }
		sum, err := Run(o)
		if err != nil {
			t.Fatal(err)
		}
		var h []*record.Record
		for _, r := range readAll(t, e.ob) {
			if r.Kind == record.KindHistoryImport {
				h = append(h, r)
			}
		}
		return sum, h
	}
	sum, h := hist(false)
	if len(h) != 1 || len(sum.HistorySent) != 1 || sum.HistorySent[0] != "~/.claude" {
		t.Fatalf("first run: %+v, %d history records", sum, len(h))
	}
	if h[0].Slot != "~/.claude" || h[0].Seat == nil || h[0].Seat.Account != "a" {
		t.Errorf("envelope %+v", h[0])
	}
	var p record.HistoryImportPayload
	if err := h[0].DecodePayload(&p); err != nil || p.Source != "stats_cache" || !bytes.Contains(p.Raw, []byte("iguana_necktie")) {
		t.Fatalf("payload %+v %v", p, err)
	}
	sum, h = hist(false)
	if len(h) != 1 || len(sum.HistorySkipped) != 1 || len(sum.HistorySent) != 0 {
		t.Fatalf("second run must not resend: %+v, %d records", sum, len(h))
	}
	sum, h = hist(true)
	if len(h) != 2 || len(sum.HistorySent) != 1 {
		t.Fatalf("--force must resend: %+v, %d records", sum, len(h))
	}
	// Without --history nothing is sent.
	e2 := newRunEnv(t)
	if err := os.WriteFile(filepath.Join(e2.slot.Dir, "stats-cache.json"), []byte(stats), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(e2.opts()); err != nil {
		t.Fatal(err)
	}
	for _, r := range readAll(t, e2.ob) {
		if r.Kind == record.KindHistoryImport {
			t.Fatal("history_import without --history")
		}
	}
}

// A record over 1 MiB is split, not dropped.
func TestRunSplitsOversizedRecords(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	proj := filepath.Join(dir, "projects", "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("d", 900)
	var sb strings.Builder
	const n = 1500
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, `{"type":"assistant","timestamp":"2026-10-11T10:00:00.000Z","sessionId":"s1","cwd":"%s%d","requestId":"r%d","message":{"id":"m%d","model":"x","usage":{"input_tokens":1,"output_tokens":2}}}`+"\n", long, i, i, i)
	}
	if err := os.WriteFile(filepath.Join(proj, "s1.jsonl"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	ob := outbox.New(filepath.Join(state, "outbox"))
	sum, err := Run(RunOptions{Home: home, StateDir: state, Slots: []Slot{{Folder: "~/.claude", Dir: dir}}, Outbox: ob, Machine: "m_x", Client: "sy/test"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Rows != n || sum.Records < 2 {
		t.Fatalf("%+v: want %d rows in at least 2 records", sum, n)
	}
	rows := 0
	for _, r := range readAll(t, ob) {
		var p record.TokenRowsPayload
		if err := r.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		rows += len(p.Rows)
	}
	if rows != n {
		t.Fatalf("%d rows survived the split, want %d", rows, n)
	}
}

func TestTildePath(t *testing.T) {
	for _, c := range []struct{ home, in, want string }{
		{"/home/u", "/home/u/api", "~/api"},
		{"/home/u", "/home/u", "~"},
		{"/home/u", "/srv/x", "/srv/x"},
		{"/home/u", "", ""},
		{"/home/u", "relative", "relative"},
		{"/home/u", "/home/user2/x", "/home/user2/x"},
	} {
		if got := tildePath(c.home, c.in); got != c.want {
			t.Errorf("tildePath(%q,%q) = %q, want %q", c.home, c.in, got, c.want)
		}
	}
}

func TestLimitHitTimes(t *testing.T) {
	m := limitHit(LimitHit{SessionID: "s", TS: time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC).UnixMilli(), Type: "five_hour", ResetsAt: 1790000000000})
	if m["ts"] != "2026-10-01T01:02:03.000Z" || m["resets_at"] == nil {
		t.Fatalf("%v", m)
	}
}
