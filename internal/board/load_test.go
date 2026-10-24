package board

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/status"
)

// loadRecords returns what status.Load returns, as a set, including the cases Load skips:
// an unfinished last line, junk, a spill file, an old day file.
func TestLoadRecordsMatchesStatusLoad(t *testing.T) {
	dir := bigOutbox(t, 1<<20)
	f, err := os.OpenFile(filepath.Join(dir, "2026-10-12.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("not json\n{\"v\":1,\"id\":\"\",\"kind\":\"hook_event\"}\n{\"v\":1,\"id\":\"01JA2XRB3T0M2N4P6Q8R9999999\",\"kind\":\"hook_event\",\"ts\":\"2026-10-12T00:00:01.000Z\"")
	f.Close()
	old := `{"v":1,"id":"01JA2XRB3T0M2N4P6Q8R0000001","kind":"hook_event","ts":"2026-09-01T00:00:00.000Z","machine":"m","payload":{}}` + "\n"
	spill := `{"v":1,"id":"01JA2XRB3T0M2N4P6Q8R0000002","kind":"hook_event","ts":"2026-10-11T00:00:00.000Z","machine":"m","payload":{}}` + "\n"
	os.WriteFile(filepath.Join(dir, "2026-09-01.jsonl"), []byte(old), 0o644)
	os.WriteFile(filepath.Join(dir, "spill-1-abc.jsonl"), []byte(spill), 0o644)
	since := fxNow.Add(-status.Window)
	want, err := status.Load(dir, since)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadRecords(dir, since)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(rs []*record.Record) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(ids(got), ids(want)) || len(got) < 300 {
		t.Fatalf("got %d records, status.Load %d", len(got), len(want))
	}
	if recs, err := loadRecords(filepath.Join(dir, "none"), since); recs != nil || err != nil {
		t.Errorf("missing folder: %v %v", recs, err)
	}
}

// A big outbox folds to the same View through the parallel loader on every run (the order
// of records inside the loader is not defined; the View must not depend on it).
func TestLocalFoldDeterministic(t *testing.T) {
	dir := bigOutbox(t, 1<<20)
	lo := LocalOptions{Options: Options{Now: fxNow, Loc: time.UTC, Machine: Machine{ID: "m_1"}}, Env: fakeEnv(t), OutboxDir: dir}
	a, err := BuildLocal(lo)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		b, _ := BuildLocal(lo)
		if viewKey(a) != viewKey(b) {
			t.Fatal("two folds of the same outbox differ")
		}
	}
	if len(a.Rows) < 30 {
		t.Errorf("%d rows", len(a.Rows))
	}
}
