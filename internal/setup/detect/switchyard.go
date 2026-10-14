package detect

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/setup/model"
)

// readLedger reads <state>/setup/ledger.json; nil when absent or unreadable.
func readLedger(path string) *model.Ledger {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var l model.Ledger
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	return &l
}

// incompleteSets lists backup ids whose manifest says in-progress (setup.md 7.6).
func incompleteSets(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "manifest.json"))
		if err != nil {
			continue
		}
		var m struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(b, &m) == nil && m.Status == model.StatusInProgress {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// realTailscale runs `tailscale status --json` (3 s) and keeps the self DNS name.
func realTailscale(ctx context.Context) *model.Tailscale {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return nil
	}
	return parseTailscale(out)
}

func parseTailscale(out []byte) *model.Tailscale {
	var s struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	if json.Unmarshal(out, &s) != nil {
		return nil
	}
	return &model.Tailscale{
		Up:      s.BackendState == "Running",
		DNSName: strings.TrimSuffix(s.Self.DNSName, "."),
	}
}
