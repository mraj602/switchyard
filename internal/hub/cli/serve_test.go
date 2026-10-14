package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeRejectsBadPublicURL(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	for _, u := range []string{"hub.example", "https://hub.example/path", "ftp://hub.example"} {
		_, err := run(t, "serve", "--db", db, "--listen", "127.0.0.1:0", "--public-url", u)
		if err == nil || !strings.Contains(err.Error(), "--public-url") {
			t.Errorf("--public-url %q: %v", u, err)
		}
	}
}

func TestRunRoutesPairingCommands(t *testing.T) {
	if _, err := run(t, "pair", "--db", filepath.Join(t.TempDir(), "hub.db")); !errors.Is(err, ErrUsage) {
		t.Errorf("pair without --kind: %v", err)
	}
	_, err := run(t, "owner-link", "--db", filepath.Join(t.TempDir(), "none.db"))
	if err == nil || !strings.Contains(err.Error(), "no hub database") {
		t.Errorf("owner-link without a database: %v", err)
	}
	out, _ := run(t, "--help")
	for _, w := range []string{"pair", "owner-link", "--public-url", "--no-push"} {
		if !strings.Contains(out, w) {
			t.Errorf("usage lacks %q", w)
		}
	}
}
