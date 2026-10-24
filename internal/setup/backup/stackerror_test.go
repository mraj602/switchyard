package backup_test

import (
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/backup"
)

// The refusal reads once: no "setup:" of its own (the command adds "wy setup: ") and the
// advice said one time.
func TestStackErrorReadsOnce(t *testing.T) {
	msg := (&backup.StackError{ID: "20261012T173000Z", Newer: []string{"20261013T090000Z"}}).Error()
	want := "backup 20261012T173000Z is older than 20261013T090000Z, which is still applied: undo that first, or use --force"
	if msg != want {
		t.Fatalf("got %q\nwant %q", msg, want)
	}
	if strings.Count(msg, "--force") != 1 || strings.HasPrefix(msg, "setup:") {
		t.Fatalf("%q", msg)
	}
}
