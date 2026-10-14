package importproto

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
)

const fixtures = "../../../../testdata/prototype"

// buildPrototype makes a prototype usage.db from testdata/prototype (schema.sql plus one
// jsonl file per table) and returns its path and the row count per table.
func buildPrototype(t *testing.T) (string, map[string]int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile(filepath.Join(fixtures, "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(fixtures, "*.jsonl"))
	counts := map[string]int64{}
	for _, f := range files {
		table := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		var n int64
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			dec := json.NewDecoder(strings.NewReader(sc.Text()))
			dec.UseNumber()
			var row map[string]any
			if err := dec.Decode(&row); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			cols := make([]string, 0, len(row))
			for c := range row {
				cols = append(cols, c)
			}
			sort.Strings(cols)
			args := make([]any, len(cols))
			for i, c := range cols {
				switch v := row[c].(type) {
				case json.Number:
					if n, err := v.Int64(); err == nil {
						args[i] = n
					} else {
						args[i], _ = v.Float64()
					}
				default:
					args[i] = v
				}
				cols[i] = `"` + c + `"`
			}
			q := "INSERT OR IGNORE INTO " + table + " (" + strings.Join(cols, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
			if _, err := db.Exec(q, args...); err != nil {
				t.Fatalf("%s: %v", table, err)
			}
		}
		fh.Close()
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	return path, counts
}

func TestImportFixture(t *testing.T) {
	ctx := context.Background()
	src, want := buildPrototype(t)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := func() time.Time { return time.UnixMilli(1791900000000) }
	rep, err := Import(ctx, st, src, Options{MachineName: "fixture", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rejected != 0 || rep.TypeErrors != 0 {
		t.Fatalf("rejected %d, not typed %d: %v", rep.Rejected, rep.TypeErrors, rep.Notes)
	}
	got := map[string]int64{}
	for _, r := range rep.Rows {
		got[r.Hub] = r.Count
		if r.Hub != "-" && r.Hub != "session" && r.Source != want[r.Table] {
			t.Errorf("%s: report says %d source rows, fixture has %d", r.Table, r.Source, want[r.Table])
		}
	}
	for hub, n := range map[string]int64{
		"hook_event":      want["events"],
		"web_usage":       want["snapshots"],
		"token_row":       want["tokens"],
		"limit_hit":       want["limit_hits"],
		"slot":            want["config_dirs"],
		"seat (labelled)": want["accounts"],
	} {
		if got[hub] != n {
			t.Errorf("%s: %d rows, want %d", hub, got[hub], n)
		}
	}
	if got["limit_sample (web)"] == 0 {
		t.Error("no web limit samples built from the snapshots")
	}
	if got["session"] < want["sessions"] {
		t.Errorf("session: %d, want at least %d", got["session"], want["sessions"])
	}
	// Seats carry the prototype labels; the session row carries the prototype facts.
	var label, colour string
	if err := st.R().QueryRow(`SELECT label, colour FROM seat WHERE account_uuid = 'aaaaaaaa-0000-4000-8000-000000000003'`).Scan(&label, &colour); err != nil {
		t.Fatal(err)
	}
	if label != "Account 3" || colour != "#9085e9" {
		t.Errorf("seat 3 = %q %q", label, colour)
	}
	var model string
	if err := st.R().QueryRow(`SELECT coalesce(model, '') FROM session WHERE id = '86ef2cfa-c37e-4d54-8d7d-5fa9ee253cb7'`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != "claude-opus-5-5" {
		t.Errorf("session model = %q", model)
	}

	// Running it again adds nothing.
	again, err := Import(ctx, st, src, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if again.Machine != rep.Machine || again.Duplicates != again.Records || again.Records != rep.Records {
		t.Errorf("rerun: machine %s/%s, %d records, %d duplicates", again.Machine, rep.Machine, again.Records, again.Duplicates)
	}
	for i, r := range again.Rows {
		if r.Count != rep.Rows[i].Count {
			t.Errorf("rerun changed %s: %d -> %d", r.Hub, rep.Rows[i].Count, r.Count)
		}
	}
}

func TestDetID(t *testing.T) {
	a, b := detID(1791900000000, "x"), detID(1791900000000, "x")
	if a != b || len(a) != 26 || a == detID(1791900000000, "y") {
		t.Fatalf("detID: %s %s", a, b)
	}
	if detID(1, "x") >= detID(2, "x") {
		t.Fatal("detID not time ordered")
	}
}
