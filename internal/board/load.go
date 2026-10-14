package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/record"
)

// loadRecords is status.Load with the JSON decoding spread over the CPUs: the same files
// (day files not older than the cutoff day, and spill files), the same kinds, the same
// skipping of partial last lines, over-long lines and unparsable ones, and the same
// result as a set. It exists for the 200 ms fold budget of a 20 MiB outbox (T3); decoding
// one record costs about 17 µs, so ten thousand events are most of the work. The order of
// the result is not defined.
func loadRecords(dir string, since time.Time) ([]*record.Record, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoffDay := since.UTC().Add(-24 * time.Hour).Format("2006-01-02")
	var names []string
	for _, e := range ents {
		n := e.Name()
		if m := dayFileRe.FindStringSubmatch(n); m != nil {
			if m[1] >= cutoffDay {
				names = append(names, n)
			}
		} else if spillFileRe.MatchString(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	var lines [][]byte
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if errors.Is(err, fs.ErrNotExist) {
			continue // a spill file deleted while we listed
		}
		if err != nil {
			return nil, err
		}
		for len(data) > 0 {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				break // a partial last line is not ours yet
			}
			line := data[:i]
			data = data[i+1:]
			if len(line)+1 <= record.MaxEncodedBytes && wantedLine(line) {
				lines = append(lines, line)
			}
		}
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 || len(lines) < 256 {
		workers = 1
	}
	parts := make([][]*record.Record, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < len(lines); i += workers {
				var r record.Record
				if json.Unmarshal(lines[i], &r) != nil || r.ID == "" {
					continue
				}
				if t, err := r.Time(); err != nil || t.Before(since) {
					continue
				}
				parts[w] = append(parts[w], &r)
			}
		}(w)
	}
	wg.Wait()
	var out []*record.Record
	for _, p := range parts {
		out = append(out, p...)
	}
	return out, nil
}

var (
	dayFileRe   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)
	spillFileRe = regexp.MustCompile(`^spill-\d+-[0-9A-Za-z]+\.jsonl$`)

	wantedKinds = [][]byte{
		[]byte(`"kind":"` + record.KindHookEvent + `"`),
		[]byte(`"kind":"` + record.KindSessionList + `"`),
		[]byte(`"kind":"` + record.KindLimitSample + `"`),
	}
)

// wantedLine is a cheap test on the head of the line: records are encoded with v, id and
// kind first.
func wantedLine(line []byte) bool {
	head := line
	if len(head) > 300 {
		head = head[:300]
	}
	for _, k := range wantedKinds {
		if bytes.Contains(head, k) {
			return true
		}
	}
	return false
}
