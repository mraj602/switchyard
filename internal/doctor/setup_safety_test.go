package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// A refused MCP pass (it would have removed every server) is a warning naming
// the folders, and the removal log is shown; a set in progress while setup.lock
// is held is a run still going, not an interrupted one.
func TestSetupChecksMCPGuardAndLiveRun(t *testing.T) {
	e := newFakeHome(t, `{"hooks":{`+allHooks+`}}`)
	st := e.dirs.State.Path
	if err := ledger.Write(st, model.Ledger{V: 1}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(st, "share", "mcp-guard.json"), `{"time":"2026-10-13T10:00:00Z","removed":["fs","gh"],"by":["~/.claude-2"]}`)
	write(t, filepath.Join(st, "share", "mcp-removed.jsonl"), `{"time":"2026-10-12T10:00:00Z","name":"old","by":"~/.claude-3"}`+"\n")
	rep := Run(context.Background(), e.opts(identity.OSFS{}))
	if d := wantLevel(t, rep, "mcp", "", Warn).Detail; !strings.Contains(d, "fs, gh") || !strings.Contains(d, "~/.claude-2") {
		t.Errorf("guard: %q", d)
	}
	found := false
	for _, c := range rep.Checks {
		found = found || (c.Name == "mcp" && strings.Contains(c.Detail, "old (gone from ~/.claude-3"))
	}
	if !found {
		t.Error("removal log not reported")
	}

	write(t, filepath.Join(st, "backup", "20261013-100000", "manifest.json"), `{"v":1,"id":"20261013-100000","status":"in-progress","entries":[]}`)
	unlock, err := apply.Lock(st)
	if err != nil {
		t.Fatal(err)
	}
	rep = Run(context.Background(), e.opts(identity.OSFS{}))
	unlock()
	for _, c := range rep.Checks {
		if c.Name == "setup" && strings.Contains(c.Detail, "interrupted") {
			t.Fatalf("a live run reported as interrupted: %+v", c)
		}
	}
	rep = Run(context.Background(), e.opts(identity.OSFS{}))
	if d := wantLevel(t, rep, "setup", "", Fail).Detail; !strings.Contains(d, "interrupted") {
		t.Errorf("free lock: %q", d)
	}
}
