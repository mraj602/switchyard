package api

import "testing"

// Android reports some zones by their old names; the hub must accept them even when the
// host's tzdata leaves them out (Ubuntu 26.04 moved them to tzdata-legacy).
func TestDevValidTZ(t *testing.T) {
	for _, tz := range []string{"Asia/Kolkata", "Asia/Calcutta", "America/Buenos_Aires", "UTC"} {
		if !devValidTZ(tz) {
			t.Errorf("devValidTZ(%q) = false, want true", tz)
		}
	}
	for _, tz := range []string{"", "Mars/Olympus", "../etc/passwd", "Asia/Kolkata/x/y"} {
		if devValidTZ(tz) {
			t.Errorf("devValidTZ(%q) = true, want false", tz)
		}
	}
}
