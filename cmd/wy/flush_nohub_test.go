package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/outbox"
)

// With no hub configured the outbox must still be capped, or it grows for ever.
func TestFlushWithoutHubStillEnforcesTheCap(t *testing.T) {
	deps, _, _ := sandbox(t)
	d := deps.dirs
	if err := os.MkdirAll(d.Config.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Config.Path, "config.toml"), []byte("[outbox]\nmax_mb = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(d.State.Path, "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("p", 2000)
	n := 0
	write := func(day, ts string, lines int) {
		var sb strings.Builder
		for i := 0; i < lines; i++ {
			n++
			fmt.Fprintf(&sb, `{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T%05d","kind":"session_list","ts":%q,"machine":"m_x","slot":"~/.claude","payload":{"pad":%q}}`+"\n", n, ts, pad)
		}
		if err := os.WriteFile(filepath.Join(dir, day+".jsonl"), []byte(sb.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("2020-01-01", "2020-01-01T10:00:00.000Z", 250)
	write("2020-01-02", "2020-01-02T10:00:00.000Z", 250)
	write("2020-01-03", "2020-01-03T10:00:00.000Z", 250)
	write("2020-01-04", "2020-01-04T10:00:00.000Z", 5) // newest file: never compacted
	before, _ := outbox.New(dir).Stat()
	if before.UnflushedBytes <= 1<<20 {
		t.Fatalf("setup: only %d bytes", before.UnflushedBytes)
	}

	var out, errb bytes.Buffer
	if err := runFlush([]string{"--quiet"}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	after, _ := outbox.New(dir).Stat()
	if after.UnflushedBytes > 1<<20 {
		t.Fatalf("outbox still %d bytes after the cap run (was %d)", after.UnflushedBytes, before.UnflushedBytes)
	}
	// Rate limited: a second run within the window does not run the cap again (and costs nothing).
	if _, err := os.Stat(filepath.Join(dir, "cap.stamp")); err != nil {
		t.Errorf("no cap.stamp: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cursor.json")); err == nil {
		t.Error("no hub, no cursor")
	}
}
