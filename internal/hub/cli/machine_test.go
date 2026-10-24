package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

func TestMachineShow(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	m := "m_" + record.NewID(time.Now())
	if out, err := run(t, "token", "create", "--kind", "machine", "--name", "gpu-box", "--machine", m, "--db", db); err != nil {
		t.Fatal(out, err)
	}
	out, err := run(t, "machine", "show", m, "--json", "--db", db)
	if err != nil {
		t.Fatal(out, err)
	}
	var v struct {
		ID, Name      string
		VersionStatus string `json:"version_status"`
		Held          *int64 `json:"held"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.ID != m || v.VersionStatus == "" || v.Held == nil {
		t.Fatalf("%s %v", out, err)
	}
	if out, err := run(t, "machine", "show", m, "--db", db); err != nil || !strings.Contains(out, m) {
		t.Fatalf("%s %v", out, err)
	}
	if out, err := run(t, "machine", "show", "m_nope", "--db", db); err == nil {
		t.Fatalf("unknown machine: %s", out)
	}
	if _, err := run(t, "machine", "--db", db); err != ErrUsage {
		t.Fatalf("no subcommand: %v", err)
	}
}
