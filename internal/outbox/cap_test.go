package outbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnforceCap(t *testing.T) {
	o, now := newBox(t)
	pad := `{"pad":"` + strings.Repeat("p", 2000) + `"}`
	day := func(n int) time.Time { return t0.Add(time.Duration(n) * 24 * time.Hour) }

	// Three old days, each with every kind, and today's file with token_rows that must stay.
	kept := map[string]bool{}
	for d := 0; d < 3; d++ {
		*now = day(d)
		for _, kind := range []string{"hook_event", "token_rows", "session_list", "limit_sample", "history_import", "slot_info", "web_usage", "future_kind"} {
			r := rec(kind, *now, pad)
			r.Slot = "~/.claude"
			if kind == "hook_event" || kind == "slot_info" || kind == "web_usage" || kind == "future_kind" {
				kept[r.ID] = true
			}
			mustAppend(t, o, r)
		}
	}
	*now = day(3)
	var todayIDs []string
	for i := 0; i < 3; i++ {
		r := rec("token_rows", *now, pad)
		todayIDs = append(todayIDs, r.ID)
		mustAppend(t, o, r)
	}
	before, _ := o.Stat()

	// Under the cap: nothing happens.
	res, err := o.EnforceCap(before.UnflushedBytes)
	if err != nil || len(res.Compacted) != 0 {
		t.Fatalf("under cap: %+v %v", res, err)
	}

	// Put the cursor in the first file, after its first record.
	b, _ := o.Read(Cursor{}, 1, 0)
	o.WriteCursor(b.Next)

	cap := before.UnflushedBytes / 2
	res, err = o.EnforceCap(cap)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped["token_rows"] != 0 || res.Dropped["session_list"]+res.Dropped["limit_sample"]+res.Dropped["history_import"] == 0 ||
		res.Dropped["hook_event"] != 0 || res.Dropped["slot_info"] != 0 ||
		res.Dropped["web_usage"] != 0 || res.Dropped["future_kind"] != 0 {
		t.Fatalf("dropped = %v", res.Dropped)
	}
	after := readAll(t, o, o.ReadCursor())
	got := map[string]bool{}
	for _, it := range after.Items {
		got[it.ID] = true
	}
	for id := range kept {
		// The first file's hook_event was already flushed (before the cursor): compaction
		// removes the flushed prefix, everything else must remain.
		if !got[id] && id != b.Items[0].ID {
			t.Fatalf("a never-dropped record %s is gone", id)
		}
	}
	for _, id := range todayIDs {
		if !got[id] {
			t.Fatal("today's file was compacted")
		}
	}
	// token_rows are never dropped (sy scan will not re-read them): all three days' rows stay
	// (the first day's was flushed before the cursor, so only count what is still unflushed).
	rows := 0
	for _, it := range after.Items {
		if strings.Contains(string(it.Raw), `"kind":"token_rows"`) {
			rows++
		}
	}
	if rows < 2+len(todayIDs) {
		t.Fatalf("token_rows were dropped: %d left", rows)
	}
	// The cursor was in the first file: it is at offset 0 of the compacted file now.
	if c := o.ReadCursor(); c.File != "2026-10-12.jsonl" || c.Offset != 0 {
		t.Fatalf("cursor = %+v", c)
	}
	if st, _ := o.Stat(); st.UnflushedBytes >= before.UnflushedBytes {
		t.Fatalf("unflushed %d did not shrink from %d", st.UnflushedBytes, before.UnflushedBytes)
	}
	// Leaves no tmp file behind and the files still parse.
	if m, _ := filepath.Glob(filepath.Join(o.Dir, "*.compact.tmp")); len(m) != 0 {
		t.Fatalf("tmp left: %v", m)
	}
	if after.Corrupt != 0 {
		t.Fatal("corrupt lines after compaction")
	}
}

func TestEnforceCapOrderAndNeverDrop(t *testing.T) {
	o, now := newBox(t)
	pad := `{"pad":"` + strings.Repeat("p", 1000) + `"}`
	*now = t0
	add := func(kind string, at time.Time) string {
		r := rec(kind, at, pad)
		r.Slot = "~/.claude"
		mustAppend(t, o, r)
		return r.ID
	}
	// Day 1: only hook events and a session_list or two, plus samples in the same hour.
	hook := add("hook_event", *now)
	oldList := add("session_list", now.Add(-5*time.Hour))
	add("limit_sample", *now)
	add("limit_sample", now.Add(time.Minute)) // same session ("") and hour: the older one goes
	*now = t0.Add(48 * time.Hour)
	add("session_list", *now) // newest list of the slot lives in a later file
	add("hook_event", *now)

	// A cap that cannot be reached without breaking the rules: only droppable kinds go.
	res, err := o.EnforceCap(1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped["hook_event"] != 0 {
		t.Fatalf("hook_event dropped: %v", res.Dropped)
	}
	if res.Dropped["session_list"] != 1 || res.Dropped["limit_sample"] != 1 {
		t.Fatalf("dropped = %v (want the stale list and the older sample of the hour)", res.Dropped)
	}
	got := map[string]bool{}
	for _, it := range readAll(t, o, Cursor{}).Items {
		got[it.ID] = true
	}
	if !got[hook] || got[oldList] {
		t.Fatalf("hook kept=%v oldList kept=%v", got[hook], got[oldList])
	}
	_ = os.Remove
}
