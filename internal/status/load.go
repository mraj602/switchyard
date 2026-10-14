package status

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/mraj602/switchyard/internal/record"
)

var (
	dayFileRe   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)
	spillFileRe = regexp.MustCompile(`^spill-\d+-[0-9A-Za-z]+\.jsonl$`)
)

// kinds that the local view reads; every other line (token_rows is most of an outbox) is
// skipped without being decoded.
var wantedKinds = [][]byte{
	[]byte(`"kind":"` + record.KindHookEvent + `"`),
	[]byte(`"kind":"` + record.KindSessionList + `"`),
	[]byte(`"kind":"` + record.KindLimitSample + `"`),
}

// Load reads the outbox in dir (flushed or not) and returns the hook_event, session_list and
// limit_sample records whose ts is at or after since, in no particular order. It never
// writes: unparsable lines are skipped silently (sy flush owns corrupt.jsonl), and bytes
// after the last newline of a file (a write in progress) are ignored, as in outbox.md
// section 4. A missing folder is an empty outbox.
func Load(dir string, since time.Time) ([]*record.Record, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoffDay := since.UTC().Add(-24 * time.Hour).Format("2006-01-02") // a day file may hold late records
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
	var out []*record.Record
	for _, n := range names {
		recs, err := readFile(filepath.Join(dir, n), since)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func readFile(path string, since time.Time) ([]*record.Record, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // a spill file deleted while we listed
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, record.MaxEncodedBytes+4096)
	var out []*record.Record
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, io.EOF) {
			return out, nil // a partial last line is not ours yet
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				// Longer than any legal line: skip the rest of it.
				for errors.Is(err, bufio.ErrBufferFull) {
					_, err = br.ReadSlice('\n')
				}
				continue
			}
			return out, err
		}
		if !wanted(line) {
			continue
		}
		var r record.Record
		if json.Unmarshal(line, &r) != nil || r.ID == "" {
			continue
		}
		if t, err := r.Time(); err != nil || t.Before(since) {
			continue
		}
		out = append(out, &r)
	}
}

// wanted is a cheap prefix test: records are encoded with v, id, kind first.
func wanted(line []byte) bool {
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
