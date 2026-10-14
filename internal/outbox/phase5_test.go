package outbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func p5line(id string, v int, ts string) string {
	return fmt.Sprintf(`{"v":%d,"id":%q,"kind":"hook_event","ts":%q,"machine":"m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K","payload":{}}`, v, id, ts)
}

func p5id(i int) string { return fmt.Sprintf("01JA2XQ8R7Y5V0Z6M3K1T%05d", i) }

func p5write(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadVStopsBeforeTooNewRecord(t *testing.T) {
	dir := t.TempDir()
	ob := New(dir)
	p5write(t, dir, "2026-10-12.jsonl",
		p5line(p5id(0), 1, "2026-10-12T10:00:00Z"),
		p5line(p5id(1), 1, "2026-10-12T10:00:01Z"),
		p5line(p5id(2), 2, "2026-10-12T10:00:02Z"),
		p5line(p5id(3), 1, "2026-10-12T10:00:03Z"),
		p5line(p5id(4), 3, "2026-10-12T10:00:04Z"))
	p5write(t, dir, "spill-1-abc.jsonl", p5line(p5id(5), 1, "2026-10-12T10:00:05Z"))

	b, err := ob.ReadV(Cursor{}, 100, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Items) != 2 || b.Items[1].ID != p5id(1) {
		t.Fatalf("items %v", b.Items)
	}
	if b.Held != 4 || b.HeldNeeds != 3 {
		t.Fatalf("held %d needs %d, want 4 (the v2 record and everything after it) and 3", b.Held, b.HeldNeeds)
	}
	// Next stays before the held record: it is the end of record 1.
	if b.Next.File != "2026-10-12.jsonl" || b.Next.Offset != b.Items[1].End {
		t.Fatalf("next %+v, want the end of the second record", b.Next)
	}
	// Once the cursor is there, nothing is sent, all is held.
	b2, err := ob.ReadV(b.Next, 100, 0, 1)
	if err != nil || len(b2.Items) != 0 || b2.Held != 4 || b2.Next != b.Next {
		t.Fatalf("second read: %+v %v", b2, err)
	}
	// The hub learns v2: the v2 record goes, the v3 one is held.
	b3, _ := ob.ReadV(b.Next, 100, 0, 2)
	if len(b3.Items) != 2 || b3.Items[0].ID != p5id(2) || b3.Items[1].ID != p5id(3) || b3.Held != 2 {
		t.Fatalf("after v2: items %d held %d", len(b3.Items), b3.Held)
	}
	// No limit (Read) sends everything.
	all, _ := ob.Read(Cursor{}, 100, 0)
	if len(all.Items) != 6 || all.Held != 0 {
		t.Fatalf("Read: %d items, held %d", len(all.Items), all.Held)
	}
}

func TestReadVHeldSpillRecord(t *testing.T) {
	dir := t.TempDir()
	ob := New(dir)
	p5write(t, dir, "2026-10-12.jsonl", p5line(p5id(0), 1, "2026-10-12T10:00:00Z"))
	p5write(t, dir, "spill-1-abc.jsonl", p5line(p5id(1), 2, "2026-10-12T10:00:05Z"))
	b, _ := ob.ReadV(Cursor{}, 10, 0, 1)
	if len(b.Items) != 1 || b.Held != 1 {
		t.Fatalf("items %d held %d", len(b.Items), b.Held)
	}
	if _, err := os.Stat(filepath.Join(dir, "spill-1-abc.jsonl")); err != nil {
		t.Fatal("a held spill file must stay")
	}
}

func TestApplyMovesCursorOverAnsweredPrefixOnly(t *testing.T) {
	dir := t.TempDir()
	ob := New(dir)
	p5write(t, dir, "2026-10-12.jsonl",
		p5line(p5id(0), 1, "2026-10-12T10:00:00Z"),
		p5line(p5id(1), 1, "2026-10-12T10:00:01Z"),
		p5line(p5id(2), 1, "2026-10-12T10:00:02Z"))
	b, _ := ob.Read(Cursor{}, 10, 0)
	cur := Cursor{V: 1, Failures: 3, RetryAfter: "x"}
	ap := ob.Apply(&cur, b, Answer{Accepted: []string{p5id(0), p5id(2)}, Duplicates: []string{p5id(2)}}, nil)
	if ap.Missing != 1 || ap.Accepted != 2 || ap.Duplicates != 1 {
		t.Fatalf("%+v", ap)
	}
	if cur.Offset != b.Items[0].End || cur.Failures != 3 {
		t.Fatalf("cursor %+v: must stop after the first record and keep the failure state", cur)
	}
	cur = Cursor{V: 1, Failures: 3, RetryAfter: "x"}
	ap = ob.Apply(&cur, b, Answer{Accepted: []string{p5id(0), p5id(1)}, Rejected: []Rejected{{ID: p5id(2), Reason: "bad_envelope", Detail: "d"}}}, nil)
	if ap.Missing != 0 || ap.Rejected != 1 || cur.Offset != b.Next.Offset || cur.Failures != 0 || cur.RetryAfter != "" {
		t.Fatalf("%+v %+v", ap, cur)
	}
	rej, _ := os.ReadFile(filepath.Join(dir, "rejected.jsonl"))
	if !strings.Contains(string(rej), p5id(2)) || !strings.Contains(string(rej), "bad_envelope: d") {
		t.Fatalf("rejected.jsonl: %s", rej)
	}
}

func TestBacklogCountsPastCursor(t *testing.T) {
	dir := t.TempDir()
	ob := New(dir)
	p5write(t, dir, "2026-10-12.jsonl",
		p5line(p5id(0), 1, "2026-10-12T10:00:00Z"),
		p5line(p5id(1), 1, "2026-10-12T09:00:00Z"),
		p5line(p5id(2), 1, "2026-10-12T10:00:02Z"))
	n, oldest, err := ob.Backlog(Cursor{})
	if err != nil || n != 3 || oldest != "2026-10-12T09:00:00Z" {
		t.Fatalf("%d %q %v", n, oldest, err)
	}
	b, _ := ob.Read(Cursor{}, 2, 0)
	n, oldest, _ = ob.Backlog(b.Next)
	if n != 1 || oldest != "2026-10-12T10:00:02Z" {
		t.Fatalf("%d %q", n, oldest)
	}
}
