package termctl

import (
	"testing"

	"github.com/mraj602/switchyard/internal/brand"
)

// rename.md 2.8: the allow-list opens tab configs with the legacy prefix only after a rename.
func TestLegacyTabConfigURL(t *testing.T) {
	u := "warp://tab_config/oldname-work"
	if ValidTabConfigURL(u) {
		t.Fatal("legacy prefix allowed before a rename")
	}
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	if !ValidTabConfigURL(u) || !ValidTabConfigURL("warp://tab_config/"+brand.Slug+"-work") {
		t.Fatal("not allowed")
	}
	if ValidTabConfigURL("warp://tab_config/oldname-") || ValidTabConfigURL("warp://tab_config/other-work") {
		t.Fatal("too wide")
	}
}
