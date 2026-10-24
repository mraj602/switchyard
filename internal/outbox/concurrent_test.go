package outbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

const (
	childDirEnv = "WY_OUTBOX_TEST_CHILD_DIR"
	childIDEnv  = "WY_OUTBOX_TEST_CHILD_ID"
	childCount  = 200
	procCount   = 50
)

// TestMain turns the test binary into the appender child of TestConcurrentProcesses when
// the environment says so.
func TestMain(m *testing.M) {
	if dir := os.Getenv(childDirEnv); dir != "" {
		id, _ := strconv.Atoi(os.Getenv(childIDEnv))
		if err := childAppend(dir, id); err != nil {
			fmt.Fprintln(os.Stderr, "child:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// padSize gives the mixed record sizes: mostly small, some medium, a few up to 256 KiB.
func padSize(rng *rand.Rand) int {
	switch p := rng.Intn(100); {
	case p < 80:
		return 50 + rng.Intn(500)
	case p < 95:
		return 1000 + rng.Intn(8000)
	case p < 99:
		return 10000 + rng.Intn(60000)
	default:
		return 200000 + rng.Intn(256*1024-200000)
	}
}

type childPayload struct {
	Proc int    `json:"proc"`
	Seq  int    `json:"seq"`
	N    int    `json:"n"`
	Pad  string `json:"pad"`
}

func childAppend(dir string, id int) error {
	o := New(dir) // real fsync, real 50 ms lock wait
	rng := rand.New(rand.NewSource(int64(id) + 1))
	for seq := 0; seq < childCount; seq++ {
		n := padSize(rng)
		pad := make([]byte, n)
		for i := range pad {
			pad[i] = byte('a' + rng.Intn(26))
		}
		r := record.New("test_load", record.Meta{Machine: "m_test", Slot: "~/.claude"})
		payload, _ := json.Marshal(childPayload{id, seq, n, string(pad)})
		r.Payload = payload
		if _, err := o.Append(r); err != nil {
			return fmt.Errorf("proc %d seq %d: %w", id, seq, err)
		}
	}
	return nil
}

// 50 processes append 200 records each, mixed sizes up to 256 KiB: every line parses,
// nothing is lost or duplicated.
func TestConcurrentProcesses(t *testing.T) {
	procs, per := procCount, childCount
	if testing.Short() {
		t.Skip("spawns 50 processes")
	}
	dir := filepath.Join(t.TempDir(), "outbox")
	var wg sync.WaitGroup
	outs := make([]bytes.Buffer, procs)
	errs := make([]error, procs)
	start := time.Now()
	for p := 0; p < procs; p++ {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), childDirEnv+"="+dir, childIDEnv+"="+strconv.Itoa(p))
		cmd.Stderr = &outs[p]
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[p] = cmd.Run()
		}()
	}
	wg.Wait()
	for p := range errs {
		if errs[p] != nil {
			t.Fatalf("child %d: %v: %s", p, errs[p], outs[p].String())
		}
	}
	t.Logf("%d processes x %d records in %v", procs, per, time.Since(start).Round(time.Millisecond))

	// 1. Raw check, independent of the reader: every file is whole lines, each valid JSON.
	ents, _ := os.ReadDir(dir)
	var spills, lines int
	for _, e := range ents {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(e.Name(), "spill-") {
			spills++
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			t.Fatalf("%s does not end with a newline", e.Name())
		}
		for _, ln := range bytes.Split(data[:len(data)-1], []byte("\n")) {
			if !json.Valid(ln) {
				t.Fatalf("%s: invalid JSON line of %d bytes: %.80q", e.Name(), len(ln), ln)
			}
			lines++
		}
	}
	if lines != procs*per {
		t.Fatalf("%d lines in files, want %d", lines, procs*per)
	}
	t.Logf("%d files, %d of them spills", len(ents), spills)

	// 2. Through the reader: every (proc, seq) exactly once, payload intact, no corrupt log.
	b, err := New(dir).Read(Cursor{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if b.Corrupt != 0 {
		t.Fatalf("%d corrupt lines", b.Corrupt)
	}
	seen := map[[2]int]bool{}
	ids := map[string]bool{}
	for _, it := range b.Items {
		var r record.Record
		if err := json.Unmarshal(it.Raw, &r); err != nil {
			t.Fatal(err)
		}
		var p childPayload
		if err := r.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if len(p.Pad) != p.N {
			t.Fatalf("proc %d seq %d: pad is %d bytes, want %d (interleaved write?)", p.Proc, p.Seq, len(p.Pad), p.N)
		}
		k := [2]int{p.Proc, p.Seq}
		if seen[k] || ids[r.ID] {
			t.Fatalf("duplicate record %v / %s", k, r.ID)
		}
		seen[k], ids[r.ID] = true, true
	}
	if len(seen) != procs*per {
		t.Fatalf("read %d distinct records, want %d", len(seen), procs*per)
	}
	if _, err := os.Stat(filepath.Join(dir, "corrupt.jsonl")); err == nil {
		t.Fatal("corrupt.jsonl exists")
	}
}
