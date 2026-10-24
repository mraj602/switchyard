package outbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// token_rows are never dropped by the cap: a dropped record could not be re-read, because
// wy scan has already committed its transcript offsets.
func TestCapNeverDropsTokenRows(t *testing.T) {
	o, now := newBox(t)
	pad := `{"pad":"` + strings.Repeat("p", 2000) + `"}`
	for d := 0; d < 4; d++ {
		*now = t0.Add(time.Duration(d) * 24 * time.Hour)
		for i := 0; i < 3; i++ {
			mustAppend(t, o, rec("token_rows", *now, pad))
		}
	}
	res, err := o.EnforceCap(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Dropped) != 0 || len(res.Compacted) != 0 {
		t.Fatalf("token_rows touched: %+v", res)
	}
	if got := len(readAll(t, o, Cursor{}).Items); got != 12 {
		t.Fatalf("%d of 12 token_rows left", got)
	}
}

// Compaction moves the cursor to (file, 0) before the rename, so a crash in between can
// only resend records, never skip them.
func TestCompactionWritesCursorBeforeRename(t *testing.T) {
	o, now := newBox(t)
	pad := `{"pad":"` + strings.Repeat("p", 1000) + `"}`
	*now = t0
	for i := 0; i < 3; i++ {
		mustAppend(t, o, rec("session_list", t0.Add(-5*time.Hour), pad))
	}
	*now = t0.Add(48 * time.Hour)
	mustAppend(t, o, rec("session_list", *now, pad))
	b, err := o.Read(Cursor{}, 1, 0)
	if err != nil || len(b.Items) != 1 {
		t.Fatal(err)
	}
	if err := o.WriteCursor(b.Next); err != nil { // one record of the first file is flushed
		t.Fatal(err)
	}
	if b.Next.Offset == 0 {
		t.Fatal("setup: cursor offset 0")
	}
	first := b.Next.File
	called := false
	o.beforeCompactRename = func() {
		called = true
		if c := o.ReadCursor(); c.File != first || c.Offset != 0 {
			t.Errorf("cursor at the rename = %+v, want {%s 0}", c, first)
		}
	}
	res, err := o.EnforceCap(1)
	if err != nil || len(res.Compacted) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if !called {
		t.Fatal("rename seam not reached")
	}
}

// A corrupt line is copied to corrupt.jsonl once, however many failed flushes re-read it
// (each flush is a new process).
func TestCorruptLineLoggedOnce(t *testing.T) {
	o, _ := newBox(t)
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	good := `{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T00001","kind":"hook_event","ts":"2026-10-12T10:00:00.000Z"}`
	data := "this is not json\n" + good + "\n"
	if err := os.WriteFile(filepath.Join(o.Dir, "2026-10-12.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		fresh := New(o.Dir) // a new process every time
		b, err := fresh.Read(Cursor{}, 10, 1<<20)
		if err != nil || len(b.Items) != 1 {
			t.Fatalf("read %d: %v %v", i, b, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(o.Dir, "corrupt.jsonl"))
	if n := strings.Count(string(raw), "\n"); n != 1 {
		t.Fatalf("corrupt.jsonl has %d entries, want 1:\n%s", n, raw)
	}
	// A second corrupt line later in the file is new and is logged.
	data += "also bad\n"
	if err := os.WriteFile(filepath.Join(o.Dir, "2026-10-12.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(o.Dir).Read(Cursor{}, 10, 1<<20); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(o.Dir, "corrupt.jsonl"))
	if n := strings.Count(string(raw), "\n"); n != 2 {
		t.Fatalf("corrupt.jsonl has %d entries, want 2:\n%s", n, raw)
	}
}

// A spill file whose writer crashed before the newline is logged and deleted once old; a
// fresh one (maybe still being written) is left alone.
func TestTornSpillFileIsReaped(t *testing.T) {
	o, _ := newBox(t)
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	torn := filepath.Join(o.Dir, "spill-111-01JA2XQ8R7Y5V0Z6M3K1TORN1.jsonl")
	fresh := filepath.Join(o.Dir, "spill-222-01JA2XQ8R7Y5V0Z6M3K1FRESH1.jsonl")
	for _, p := range []string{torn, fresh} {
		if err := os.WriteFile(p, []byte(`{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T0`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(torn, t0.Add(-time.Hour), t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, t0.Add(-time.Minute), t0.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Read(Cursor{}, 10, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(torn); err == nil {
		t.Error("torn spill file was not deleted")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a young spill file must be left alone")
	}
	raw, _ := os.ReadFile(filepath.Join(o.Dir, "corrupt.jsonl"))
	if !strings.Contains(string(raw), "spill-111") {
		t.Errorf("not logged to corrupt.jsonl: %q", raw)
	}
}
