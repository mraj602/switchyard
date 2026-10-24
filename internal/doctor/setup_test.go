package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
)

func hasCheck(rep Report, name string) bool {
	for _, c := range rep.Checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

// Without a ledger or backup folder (wy setup never ran) no setup check appears.
func TestSetupChecksSilentBeforeSetup(t *testing.T) {
	e := newFakeHome(t, `{"hooks":{`+allHooks+`}}`)
	rep := Run(context.Background(), e.opts(identity.OSFS{}))
	for _, n := range []string{"setup", "plugin", "sharing", "timer", "mcp", "copies"} {
		if hasCheck(rep, n) {
			t.Errorf("check %q shown before wy setup ran", n)
		}
	}
}

func TestSetupChecks(t *testing.T) {
	e := newFakeHome(t, `{"hooks":{`+allHooks+`},"enabledPlugins":{"watchyard@wy-local":true,"watchyard@other":true}}`)
	st := e.dirs.State.Path
	mkt := filepath.Join(st, "plugin", "marketplace")
	write(t, filepath.Join(mkt, "plugins", "watchyard", "hooks", "hooks.json"),
		`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"[ -x '/nonexistent/wy' ] && '/nonexistent/wy' hook Stop || true"}]}]}}`)
	if err := ledger.Write(st, model.Ledger{
		Plugin: &model.LedgerPlugin{Marketplace: mkt, ID: "watchyard@wy-local"},
		Timer:  true,
	}); err != nil {
		t.Fatal(err)
	}
	// A dangling shared link in ~/.claude-2.
	if err := os.Symlink(filepath.Join(e.home, ".claude", "CLAUDE.md"), filepath.Join(e.home, ".claude-2", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	o := e.opts(identity.OSFS{})
	o.LookPath = func(n string) (string, error) {
		if n == "wy" {
			return "", os.ErrNotExist
		}
		return "/fake/bin/" + n, nil
	}
	rep := Run(context.Background(), o)

	c := wantLevel(t, rep, "plugin", "", Fail)
	if !strings.Contains(c.Detail, "/nonexistent/wy") {
		t.Errorf("plugin: %q", c.Detail)
	}
	wantLevel(t, rep, "wy on PATH", "", Warn)
	wantLevel(t, rep, "plugin", "~/.claude", Warn) // two watchyard plugins enabled
	if d := wantLevel(t, rep, "sharing", "~/.claude-2", Fail).Detail; !strings.Contains(d, "CLAUDE.md") {
		t.Errorf("sharing: %q", d)
	}
	wantLevel(t, rep, "timer", "", Warn)
	if !rep.Failed() {
		t.Error("report not failed")
	}

	// The marketplace folder gone: a failure naming it.
	if err := os.RemoveAll(mkt); err != nil {
		t.Fatal(err)
	}
	rep = Run(context.Background(), o)
	if d := wantLevel(t, rep, "plugin", "", Fail).Detail; !strings.Contains(d, "marketplace folder is missing") {
		t.Errorf("plugin: %q", d)
	}
}
