package update

import "testing"

func TestArchiveName(t *testing.T) {
	r := Release{Version: "0.5.0"}
	for goos, want := range map[string]string{
		"linux":   "wy_0.5.0_linux_arm64.tar.gz",
		"darwin":  "wy_0.5.0_darwin_arm64.tar.gz",
		"windows": "wy_0.5.0_windows_arm64.zip",
	} {
		if got := r.ArchiveName(goos, "arm64"); got != want {
			t.Errorf("%s: %s, want %s", goos, got, want)
		}
	}
}
