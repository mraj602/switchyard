package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
)

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{"90d": 90 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "36h": 36 * time.Hour, "0d": 0} {
		if got, err := ParseAge(in); err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "d", "-3d", "ninety", "-1h"} {
		if _, err := ParseAge(bad); err == nil {
			t.Errorf("ParseAge(%q) accepted", bad)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:8787": true, "localhost:1": true, "[::1]:8787": true,
		"0.0.0.0:8787": false, ":8787": false, "192.168.1.5:8787": false} {
		if isLoopback(addr) != want {
			t.Errorf("isLoopback(%q) = %v", addr, !want)
		}
	}
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := Run(args, &out, &errb)
	return out.String() + errb.String(), err
}

func TestTokenLifecycleBackupPrune(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "hub.db")
	out, err := run(t, "token", "create", "--kind", "phone", "--name", "pixel", "--db", db, "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var c struct {
		Device, Token string
		Scopes        []string
	}
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatal(out, err)
	}
	if !auth.LooksLikeToken(c.Token) || !strings.HasPrefix(c.Token, "sy_p_") {
		t.Fatalf("token %q", c.Token)
	}
	if out, err := run(t, "token", "create", "--kind", "machine", "--name", "laptop", "--db", db); err == nil {
		t.Errorf("machine token without --machine accepted: %s", out)
	}
	out, err = run(t, "token", "list", "--db", db)
	if err != nil || !strings.Contains(out, "pixel") || strings.Contains(out, c.Token) {
		t.Fatalf("list (must not show the token): %s %v", out, err)
	}
	if out, err := run(t, "token", "revoke", "pixel", "--db", db, "--yes"); err != nil {
		t.Fatal(out, err)
	}
	out, _ = run(t, "token", "list", "--db", db)
	if strings.Contains(out, "pixel") {
		t.Errorf("revoked device still listed: %s", out)
	}
	out, _ = run(t, "token", "list", "--db", db, "--all")
	if !strings.Contains(out, "pixel") {
		t.Errorf("--all hides revoked: %s", out)
	}

	to := filepath.Join(dir, "copy.db")
	if out, err := run(t, "backup", "--db", db, "--to", to); err != nil {
		t.Fatal(out, err)
	}
	if fi, err := os.Stat(to); err != nil || fi.Size() == 0 {
		t.Fatalf("backup: %v", err)
	}
	out, err = run(t, "prune", "--db", db, "--older-than", "90d", "--dry-run")
	if err != nil || !strings.Contains(out, "would remove 0 records") {
		t.Fatalf("prune: %s %v", out, err)
	}
	out, err = run(t, "refold", "--db", db, "--dry-run")
	if err != nil || !strings.Contains(out, "0 sessions") || !strings.Contains(out, "would change: 0 states") {
		t.Fatalf("refold: %s %v", out, err)
	}
	if _, err := run(t, "refold", "extra", "--db", db); !errors.Is(err, ErrUsage) {
		t.Errorf("refold with an argument: %v", err)
	}
	if _, err := run(t, "prune", "--db", db); !errors.Is(err, ErrUsage) {
		t.Errorf("prune without age: %v", err)
	}
	if _, err := run(t, "bogus"); !errors.Is(err, ErrUsage) {
		t.Errorf("unknown command: %v", err)
	}
	if out, err := run(t, "serve", "--db", db, "--listen", "0.0.0.0:0"); err == nil || !strings.Contains(out+err.Error(), "insecure-lan") {
		t.Errorf("non-loopback without --insecure-lan: %s %v", out, err)
	}
}
