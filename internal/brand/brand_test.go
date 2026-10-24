package brand

import (
	"strings"
	"testing"
)

func TestNames(t *testing.T) {
	if Slug == "" || Name == "" {
		t.Fatal("empty name")
	}
	for _, c := range Slug {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			t.Fatalf("slug %q must be [a-z0-9]", Slug)
		}
	}
}

// The prefix is the upper-cased command name: "wy_HOME" would never be read.
func TestEnvPrefixLiteral(t *testing.T) {
	if EnvPrefix != "WY_" || EnvPrefix != strings.ToUpper(CLI)+"_" {
		t.Fatalf("EnvPrefix = %q", EnvPrefix)
	}
	if Marketplace != "wy-local" {
		t.Fatalf("Marketplace = %q", Marketplace)
	}
}
