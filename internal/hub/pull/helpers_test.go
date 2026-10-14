package pull_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/outbox"
)

func outboxAt(t *testing.T, name string) *outbox.Outbox {
	t.Helper()
	return outbox.New(filepath.Join(t.TempDir(), name, "outbox"))
}

// writeRecs writes n records for machine into ob's day file.
func writeRecs(t *testing.T, ob *outbox.Outbox, machine string, n int) {
	t.Helper()
	if err := os.MkdirAll(ob.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(recLine(i, 1, machine) + "\n")
	}
	if err := os.WriteFile(filepath.Join(ob.Dir, "2026-10-12.jsonl"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
