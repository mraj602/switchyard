package ingest

import (
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
