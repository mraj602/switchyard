package outbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/record"
)

var t0 = time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)

// newBox returns an outbox in a temp dir with a settable clock.
func newBox(t testing.TB) (*Outbox, *time.Time) {
	t.Helper()
	now := t0
	o := New(filepath.Join(t.TempDir(), "outbox"))
	o.Now = func() time.Time { return now }
	return o, &now
}

func rec(kind string, at time.Time, payload string) *record.Record {
	r := record.New(kind, record.Meta{Machine: "m_test", Slot: "~/.claude", Now: at})
	r.Payload = json.RawMessage(payload)
	return r
}

func mustAppend(t testing.TB, o *Outbox, r *record.Record) Result {
	t.Helper()
	res, err := o.Append(r)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func readAll(t testing.TB, o *Outbox, cur Cursor) *Batch {
	t.Helper()
	b, err := o.Read(cur, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ids(b *Batch) []string {
	var out []string
	for _, it := range b.Items {
		out = append(out, it.ID)
	}
	return out
}

func equal(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestAppendAndRead(t *testing.T) {
	o, now := newBox(t)
	var want []string
	for i := 0; i < 5; i++ {
		r := rec("hook_event", *now, fmt.Sprintf(`{"i":%d}`, i))
		want = append(want, r.ID)
		res := mustAppend(t, o, r)
		if res.File != "2026-10-12.jsonl" || res.Spilled || res.ID != r.ID {
			t.Fatalf("result = %+v", res)
		}
	}
	b := readAll(t, o, Cursor{})
	if !equal(ids(b), want) || b.Corrupt != 0 {
		t.Fatalf("read %v, want %v (corrupt %d)", ids(b), want, b.Corrupt)
	}
	// Folder 0700, file 0600.
	if runtimeUnix() {
		if st, _ := os.Stat(o.Dir); st.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v", st.Mode().Perm())
		}
		if st, _ := os.Stat(filepath.Join(o.Dir, "2026-10-12.jsonl")); st.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v", st.Mode().Perm())
		}
	}
	// Every line is one record ending in a newline.
	data, _ := os.ReadFile(filepath.Join(o.Dir, "2026-10-12.jsonl"))
	if data[len(data)-1] != '\n' || bytes.Count(data, []byte("\n")) != 5 {
		t.Fatalf("file shape: %q", data)
	}
}

func runtimeUnix() bool { return os.PathSeparator == '/' }

func TestTooLarge(t *testing.T) {
	o, now := newBox(t)
	r := rec("x", *now, `{"p":"`+strings.Repeat("a", MaxLine)+`"}`)
	if _, err := o.Append(r); err == nil {
		t.Fatal("over-limit record accepted")
	}
	// Exactly at the limit (line including newline) is accepted.
	line := bytes.Repeat([]byte("a"), MaxLine-1)
	if _, err := o.AppendLine(append(line, '\n')); err != nil {
		t.Fatalf("1 MiB line refused: %v", err)
	}
	if _, err := o.AppendLine(append(append(line, 'a'), '\n')); err == nil {
		t.Fatal("1 MiB + 1 line accepted")
	}
}

func TestDayRollover(t *testing.T) {
	o, now := newBox(t)
	mustAppend(t, o, rec("a", *now, `{}`))
	*now = now.Add(24 * time.Hour)
	res := mustAppend(t, o, rec("a", *now, `{}`))
	if res.File != "2026-10-13.jsonl" {
		t.Fatalf("file = %s", res.File)
	}
}

// A clock that jumps backwards keeps writing to the newest file (outbox.md 3 step 2).
func TestClockJumpBackwards(t *testing.T) {
	o, now := newBox(t)
	mustAppend(t, o, rec("a", *now, `{}`))
	*now = now.Add(-72 * time.Hour)
	res := mustAppend(t, o, rec("a", *now, `{}`))
	if res.File != "2026-10-12.jsonl" {
		t.Fatalf("file = %s, want the newest existing day file", res.File)
	}
}

// Truncate mid-line (a crash during write), append again: only the torn record is lost.
func TestTornLine(t *testing.T) {
	o, now := newBox(t)
	r1, r2, r3 := rec("a", *now, `{"n":1}`), rec("a", *now, `{"n":2}`), rec("a", *now, `{"n":3}`)
	for _, r := range []*record.Record{r1, r2, r3} {
		mustAppend(t, o, r)
	}
	path := filepath.Join(o.Dir, "2026-10-12.jsonl")
	st, _ := os.Stat(path)
	l3, _ := r3.Encode()
	if err := os.Truncate(path, st.Size()-int64(len(l3))/2); err != nil { // cut r3 in half
		t.Fatal(err)
	}
	// A reader before repair sees r1, r2 and stops at the torn tail without complaint.
	b := readAll(t, o, Cursor{})
	if !equal(ids(b), []string{r1.ID, r2.ID}) || b.Corrupt != 0 {
		t.Fatalf("before repair: %v corrupt=%d", ids(b), b.Corrupt)
	}
	r4 := rec("a", *now, `{"n":4}`)
	mustAppend(t, o, r4)
	b = readAll(t, o, Cursor{})
	if !equal(ids(b), []string{r1.ID, r2.ID, r4.ID}) {
		t.Fatalf("after repair: %v, want r1 r2 r4", ids(b))
	}
	if b.Corrupt != 1 {
		t.Fatalf("corrupt = %d, want the one torn fragment", b.Corrupt)
	}
	log, err := os.ReadFile(filepath.Join(o.Dir, "corrupt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		File   string
		Offset int64
		Line   string
	}
	if err := json.Unmarshal(bytes.TrimSpace(log), &entry); err != nil {
		t.Fatalf("corrupt.jsonl: %v: %q", err, log)
	}
	if entry.File != "2026-10-12.jsonl" || !strings.HasPrefix(entry.Line, `{"v":1`) || entry.Offset == 0 {
		t.Fatalf("corrupt entry = %+v", entry)
	}
}

func TestReaderStopsAtLastNewline(t *testing.T) {
	o, now := newBox(t)
	r1 := rec("a", *now, `{}`)
	mustAppend(t, o, r1)
	f, _ := os.OpenFile(filepath.Join(o.Dir, "2026-10-12.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T9B4CD","ki`) // a write in progress
	f.Close()
	b := readAll(t, o, Cursor{})
	if !equal(ids(b), []string{r1.ID}) || b.Corrupt != 0 {
		t.Fatalf("read %v corrupt=%d", ids(b), b.Corrupt)
	}
	if b.Next.Offset != b.Items[0].End {
		t.Fatalf("cursor advanced past the partial line: %d", b.Next.Offset)
	}
}

func TestBadLinesGoToCorrupt(t *testing.T) {
	o, now := newBox(t)
	mustAppend(t, o, rec("a", *now, `{}`))
	f, _ := os.OpenFile(filepath.Join(o.Dir, "2026-10-12.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("not json\n{\"no\":\"id\"}\n\n")
	f.Close()
	mustAppend(t, o, rec("a", *now, `{}`))
	b := readAll(t, o, Cursor{})
	if len(b.Items) != 2 || b.Corrupt != 2 {
		t.Fatalf("items=%d corrupt=%d", len(b.Items), b.Corrupt) // the blank line is skipped silently
	}
}

func TestSpillWhenLocked(t *testing.T) {
	o, now := newBox(t)
	o.LockWait = 10 * time.Millisecond
	r1 := rec("a", *now, `{"n":1}`)
	mustAppend(t, o, r1)

	hold, err := os.OpenFile(filepath.Join(o.Dir, "2026-10-12.jsonl"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if ok, err := tryLock(hold); !ok || err != nil {
		t.Fatalf("lock: %v %v", ok, err)
	}
	r2 := rec("a", *now, `{"n":2}`)
	start := time.Now()
	res := mustAppend(t, o, r2)
	if !res.Spilled || !strings.HasPrefix(res.File, "spill-") {
		t.Fatalf("result = %+v", res)
	}
	if d := time.Since(start); d < 5*time.Millisecond || d > time.Second {
		t.Fatalf("waited %v for the lock", d)
	}
	unlock(hold)
	r3 := rec("a", *now, `{"n":3}`)
	if res := mustAppend(t, o, r3); res.Spilled {
		t.Fatal("spilled with a free lock")
	}
	// Order: day files first, then spills.
	b := readAll(t, o, Cursor{})
	if !equal(ids(b), []string{r1.ID, r3.ID, r2.ID}) || !b.Items[2].Spill {
		t.Fatalf("read %v", ids(b))
	}
	// A spill file holds exactly one record, created 0600.
	data, _ := os.ReadFile(filepath.Join(o.Dir, res.File))
	if bytes.Count(data, []byte("\n")) != 1 {
		t.Fatalf("spill file: %q", data)
	}
}

func TestCursor(t *testing.T) {
	o, now := newBox(t)
	if c := o.ReadCursor(); c.File != "" || c.Offset != 0 {
		t.Fatalf("fresh cursor = %+v", c)
	}
	var want []string
	for i := 0; i < 3; i++ {
		r := rec("a", *now, `{}`)
		want = append(want, r.ID)
		mustAppend(t, o, r)
	}
	*now = now.Add(24 * time.Hour)
	for i := 0; i < 3; i++ {
		r := rec("a", *now, `{}`)
		want = append(want, r.ID)
		mustAppend(t, o, r)
	}
	b, err := o.Read(Cursor{}, 4, 0)
	if err != nil || len(b.Items) != 4 || !b.More {
		t.Fatalf("batch: %+v %v", b, err)
	}
	if err := o.WriteCursor(b.Next); err != nil {
		t.Fatal(err)
	}
	cur := o.ReadCursor()
	if cur.File != "2026-10-13.jsonl" || cur.Offset != b.Items[3].End || cur.Updated == "" {
		t.Fatalf("cursor = %+v", cur)
	}
	rest := readAll(t, o, cur)
	if !equal(ids(rest), want[4:]) {
		t.Fatalf("rest = %v, want %v", ids(rest), want[4:])
	}
	// Byte limit: at least one record is returned, never more than the limit allows.
	b, _ = o.Read(Cursor{}, 0, 1)
	if len(b.Items) != 1 {
		t.Fatalf("1-byte limit returned %d", len(b.Items))
	}
	// A cursor past the end of its file (compacted or replaced) resends from the start.
	b, _ = o.Read(Cursor{File: "2026-10-12.jsonl", Offset: 1 << 30}, 0, 0)
	if !equal(ids(b), want) {
		t.Fatalf("stale cursor read %v", ids(b))
	}
	// A broken cursor.json reads as the start.
	os.WriteFile(filepath.Join(o.Dir, "cursor.json"), []byte("{oops"), 0o600)
	if c := o.ReadCursor(); c.File != "" {
		t.Fatalf("broken cursor = %+v", c)
	}
}

func TestStat(t *testing.T) {
	o, now := newBox(t)
	if st, err := o.Stat(); err != nil || st.DayFiles != 0 || !st.OldestUnflushed.IsZero() {
		t.Fatalf("empty: %+v %v", st, err)
	}
	r1 := rec("a", *now, `{}`)
	mustAppend(t, o, r1)
	mustAppend(t, o, rec("a", now.Add(time.Minute), `{}`))
	st, err := o.Stat()
	if err != nil || st.DayFiles != 1 || st.Bytes == 0 || st.UnflushedBytes != st.Bytes {
		t.Fatalf("stat: %+v %v", st, err)
	}
	if !st.OldestUnflushed.Equal(*now) {
		t.Fatalf("oldest = %v, want %v", st.OldestUnflushed, *now)
	}
	// After the first record is flushed, the oldest moves to the second.
	b, _ := o.Read(Cursor{}, 1, 0)
	o.WriteCursor(b.Next)
	st, _ = o.Stat()
	if !st.OldestUnflushed.Equal(now.Add(time.Minute)) || st.UnflushedBytes >= st.Bytes {
		t.Fatalf("after flush: %+v", st)
	}
}

func TestFlushLock(t *testing.T) {
	o, _ := newBox(t)
	l, ok, err := o.TryFlushLock()
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, ok, _ := o.TryFlushLock(); ok {
		t.Fatal("two flushers")
	}
	l.Release()
	l, ok, _ = o.TryFlushLock()
	if !ok {
		t.Fatal("lock not released")
	}
	l.Release()
}

// Many goroutines in one process: the lock serialises them (and any spills are still read).
func TestGoroutines(t *testing.T) {
	o, now := newBox(t)
	var wg sync.WaitGroup
	want := sync.Map{}
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				r := rec("a", *now, `{"pad":"`+strings.Repeat("z", 5000)+`"}`)
				want.Store(r.ID, true)
				if _, err := o.Append(r); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	b := readAll(t, o, Cursor{})
	if len(b.Items) != 400 || b.Corrupt != 0 {
		t.Fatalf("items=%d corrupt=%d", len(b.Items), b.Corrupt)
	}
	for _, it := range b.Items {
		if _, ok := want.Load(it.ID); !ok {
			t.Fatalf("unknown id %s", it.ID)
		}
		want.Delete(it.ID)
	}
}

func TestPrune(t *testing.T) {
	o, now := newBox(t)
	for d := 0; d < 12; d++ {
		mustAppend(t, o, rec("a", *now, `{}`))
		*now = now.Add(24 * time.Hour)
	}
	*now = t0.Add(11 * 24 * time.Hour)
	b := readAll(t, o, Cursor{})
	o.WriteCursor(b.Next) // everything flushed, cursor in the newest file
	os.WriteFile(filepath.Join(o.Dir, "2026-10-12.jsonl.compact.tmp"), []byte("x"), 0o600)
	removed, err := o.Prune(7)
	if err != nil {
		t.Fatal(err)
	}
	days, _, _ := o.list()
	// now = 10-23; keep_days 7: files before 10-16 and before the cursor file go.
	if len(removed) != 4 || days[0] != "2026-10-16.jsonl" {
		t.Fatalf("removed %v, left %v", removed, days)
	}
	if _, err := os.Stat(filepath.Join(o.Dir, "2026-10-12.jsonl.compact.tmp")); err == nil {
		t.Fatal("compact tmp left behind")
	}
}
