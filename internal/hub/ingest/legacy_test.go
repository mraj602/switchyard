package ingest

import (
	"net/http/httptest"
	"testing"

	"github.com/mraj602/watchyard/internal/brand"
)

// rename.md 2.13: a client still on the command's old name reports its version too.
func TestClientVersionAcceptsLegacyCLI(t *testing.T) {
	for _, c := range []string{brand.CLI + "/1.1.0", brand.LegacyCLI + "/1.1.0"} {
		if m := clientVersion.FindStringSubmatch(c); m == nil || m[1] != "1.1.0" {
			t.Errorf("%s: %v", c, m)
		}
	}
	if clientVersion.MatchString("other/1.0") {
		t.Error("other client")
	}
}

// Legacy compat: a machine older than the rename sends X-Sy-Backlog(-Oldest); the new names win.
func TestBacklogHeadersLegacy(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/ingest", nil)
	r.Header.Set("X-Sy-Backlog", "7")                                         // rename:keep
	if got := headerOrLegacy(r, "X-Wy-Backlog", "X-Sy-Backlog"); got != "7" { // rename:keep
		t.Errorf("legacy only: %q", got)
	}
	r.Header.Set("X-Wy-Backlog", "9")
	if got := headerOrLegacy(r, "X-Wy-Backlog", "X-Sy-Backlog"); got != "9" { // rename:keep
		t.Errorf("both: %q", got)
	}
}
