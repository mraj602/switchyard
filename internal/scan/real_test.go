package scan

import (
	"database/sql"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestRealParity compares per-day token totals from this scanner over the real
// ~/.claude*/projects with the prototype's usage.db. Read-only: the transcripts are only
// read, no offsets are written, and the DB is opened from a copy in a temp dir.
//
//	SY_REAL_PARITY=1 go test ./internal/scan -run RealParity -v
func TestRealParity(t *testing.T) {
	if os.Getenv("SY_REAL_PARITY") != "1" {
		t.Skip("set SY_REAL_PARITY=1 to compare against the real laptop")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	src := os.Getenv("SY_PROTOTYPE_DB")
	if src == "" {
		src = filepath.Join(home, ".config", "switchyard", "usage.db")
	}
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "usage.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := copyFile(src+suffix, dst+suffix); err != nil && suffix == "" {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", dst)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const day = 86400000
	want := map[int64]int64{}
	rows, err := db.Query(`SELECT CAST(ts / ? AS INT), sum(input + output + cache_5m + cache_1h + cache_read), count(*) FROM tokens GROUP BY 1`, day)
	if err != nil {
		t.Fatal(err)
	}
	var wantRows int64
	for rows.Next() {
		var d, n, c int64
		if err := rows.Scan(&d, &n, &c); err != nil {
			t.Fatal(err)
		}
		want[d] = n
		wantRows += c
	}
	rows.Close()

	sc, err := New(Options{Slots: DiscoverSlots(home)}) // no OffsetsPath: nothing is written
	if err != nil {
		t.Fatal(err)
	}
	res, err := sc.Scan()
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]int64{}
	seen := map[string]TokenRow{}
	for _, b := range res.Batches {
		for _, r := range b.Rows {
			if o, ok := seen[r.Key]; ok { // same response in two files or slots: keep the max
				o.merge(r)
				seen[r.Key] = o
			} else {
				seen[r.Key] = r
			}
		}
	}
	for _, r := range seen {
		got[r.TS/day] += r.Input + r.Output + r.Cache5m + r.Cache1h + r.CacheRead
	}

	var days []int64
	for d := range want {
		days = append(days, d)
	}
	for d := range got {
		if _, ok := want[d]; !ok {
			days = append(days, d)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	// Claude Code deletes transcripts older than ~30 days, but the prototype DB keeps their
	// rows, so older days can only differ in one direction. The 0.1 % check covers days from
	// SY_PARITY_FROM (YYYY-MM-DD, default now minus 30 days) on; older days are only logged.
	from := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")
	if v := os.Getenv("SY_PARITY_FROM"); v != "" {
		from = v
	}
	var sumW, sumG, winW, winG int64
	worst := 0.0
	for _, d := range days {
		w, g := want[d], got[d]
		sumW += w
		sumG += g
		if humanDay(d) >= from {
			winW += w
			winG += g
		}
		diff := 0.0
		if w != 0 {
			diff = 100 * float64(g-w) / float64(w)
		} else if g != 0 {
			diff = 100
		}
		if math.Abs(diff) > math.Abs(worst) {
			worst = diff
		}
		t.Logf("%s  prototype %14d  go %14d  diff %+8.4f %%", humanDay(d), w, g, diff)
	}
	t.Logf("rows: prototype %d, go %d; files scanned %d, bytes %d", wantRows, len(seen), res.Files, res.Bytes)
	t.Logf("total: prototype %d, go %d, diff %+.4f %%; worst day %+.4f %%", sumW, sumG, 100*float64(sumG-sumW)/float64(sumW), worst)
	t.Logf("window from %s: prototype %d, go %d, diff %+.4f %%", from, winW, winG, 100*float64(winG-winW)/float64(max(winW, 1)))
	if winW == 0 {
		t.Fatal("prototype DB has no token rows in the window")
	}
	// The desktop app and Claude Code keep writing while this runs, so the newest day may differ.
	if math.Abs(float64(winG-winW))/float64(winW) > 0.001 {
		t.Errorf("window total differs by more than 0.1 %%")
	}
}

func humanDay(d int64) string {
	return time.Unix(d*86400, 0).UTC().Format("2006-01-02")
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
