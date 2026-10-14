package identity

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampMachineName(t *testing.T) {
	got := ClampMachineName(strings.Repeat("é", 70))
	if n := utf8.RuneCountInString(got); n != MaxMachineName || !utf8.ValidString(got) {
		t.Errorf("clamped to %d runes, valid=%v", n, utf8.ValidString(got))
	}
	if ClampMachineName("short") != "short" {
		t.Error("short name changed")
	}
	h := func() (string, error) { return strings.Repeat("A", 63) + ".local", nil }
	n := shortHostname(h)
	if n != strings.Repeat("a", MaxMachineName) {
		t.Errorf("shortHostname = %q", n)
	}
	if _, err := CleanMachineName(n); err != nil {
		t.Error(err)
	}
}
