package main

// End-to-end tests of `sy setup` and `sy account` (docs/phases/3.md WP-F): the sy binary
// against fakeclaude, in a fixture home of internal/setup/detect/detecttest. Nothing here
// touches the real HOME: HOME, SY_HOME and PATH all point into t.TempDir folders.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

var (
	fakeOnce sync.Once
	fakeDir  string
	fakeErr  error
)

// fakeClaudeDir builds test/e2e/setup/fakeclaude as `claude` once per test binary.
func fakeClaudeDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the setup end-to-end tests run on unix")
	}
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sy-fakeclaude-")
		if err != nil {
			fakeErr = err
			return
		}
		out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "claude"), "../../test/e2e/setup/fakeclaude").CombinedOutput()
		if err != nil {
			fakeErr = errors.New("build fakeclaude: " + err.Error() + "\n" + string(out))
			return
		}
		fakeDir = dir
	})
	if fakeErr != nil {
		t.Fatal(fakeErr)
	}
	return fakeDir
}

type setupHome struct {
	root, syHome string
	env          []string
}

func newSetupHome(t *testing.T, h *detecttest.Home) *setupHome {
	t.Helper()
	fake := fakeClaudeDir(t)
	// The fixture's ~/.local/bin/claude is an empty stub; replace it so whichever claude sy
	// finds is fakeclaude.
	b, err := os.ReadFile(filepath.Join(fake, "claude"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Abs(".local/bin/claude"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	sh := &setupHome{root: h.Root, syHome: t.TempDir()}
	sh.env = []string{
		"PATH=" + fake + ":/usr/bin:/bin",
		"HOME=" + h.Root,
		"SY_HOME=" + sh.syHome,
		"SY_NO_SPAWN=1",
		"SHELL=/bin/sh",
		"TERM=dumb",
		"FAKE_CLAUDE_LOG=" + filepath.Join(sh.syHome, "claude.log"),
	}
	return sh
}

// sy runs the binary and returns stdout, stderr and the exit code.
func (s *setupHome) sy(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(syBin, args...)
	cmd.Env = s.env
	cmd.Dir = s.root
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("sy %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return o.String(), e.String(), code
}

func (s *setupHome) mustSy(t *testing.T, args ...string) string {
	t.Helper()
	out, errOut, code := s.sy(t, "", args...)
	if code != 0 {
		t.Fatalf("sy %s: exit %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// snapOpts ignores what fakeclaude rewrites on every run (`.claude.json` state files) and
// the leftovers of a run that are not part of the user's tree.
var snapOpts = testtree.Opts{Ignore: func(rel string) bool {
	return path.Base(rel) == ".claude.json"
}}

type planJSON struct {
	PlanID  string `json:"plan_id"`
	Actions []struct {
		N      int      `json:"n"`
		Op     string   `json:"op"`
		Path   string   `json:"path"`
		Argv   []string `json:"argv"`
		Folder string   `json:"folder"`
	} `json:"actions"`
	Summary map[string]any `json:"summary"`
}

func parsePlan(t *testing.T, out string) planJSON {
	t.Helper()
	var p planJSON
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("plan JSON: %v\n%s", err, out)
	}
	return p
}

func TestSetupDetectJSON(t *testing.T) {
	s := newSetupHome(t, detecttest.Laptop(t))
	out := s.mustSy(t, "setup", "--detect", "--json")
	var st struct {
		V       int `json:"v"`
		Folders []struct {
			Path string `json:"path"`
		} `json:"folders"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("detect JSON: %v\n%s", err, out)
	}
	if len(st.Folders) != 7 {
		t.Fatalf("detect found %d folders, want 7\n%s", len(st.Folders), out)
	}
	if strings.Contains(out, "sk-secret") || strings.Contains(out, "do-not-read") {
		t.Fatal("detect printed a secret")
	}
	// Plain text works too.
	if txt := s.mustSy(t, "setup", "--detect"); !strings.Contains(txt, ".claude-7") {
		t.Fatalf("detect text misses ~/.claude-7:\n%s", txt)
	}
}

func TestSetupLaptopDryRunApplyUndo(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	before := testtree.Take(t, s.root, snapOpts)

	// setup.md 11: five actions, no links changed.
	out := s.mustSy(t, "setup", "--dry-run", "--json", "--hub", "later")
	p := parsePlan(t, out)
	var ops []string
	for _, a := range p.Actions {
		ops = append(ops, a.Op)
	}
	if len(p.Actions) != 5 {
		t.Fatalf("laptop plan has %d actions (%v), want 5\n%s", len(p.Actions), ops, out)
	}
	want := map[string]int{"write_tree": 1, "run": 2, "json_set": 1, "config_set": 1}
	got := map[string]int{}
	for _, o := range ops {
		got[o]++
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("laptop plan ops %v, want %v", got, want)
		}
	}
	if v, ok := p.Summary["links_changed"]; ok && v != float64(0) {
		t.Fatalf("links_changed = %v, want 0", v)
	}
	// A dry run writes nothing.
	testtree.Equal(t, before, testtree.Take(t, s.root, snapOpts))

	// --expect-plan with another id refuses with exit 4 and writes nothing.
	if _, _, code := s.sy(t, "", "setup", "--yes", "--hub", "later", "--expect-plan", "p_0000000000"); code != 4 {
		t.Fatalf("--expect-plan mismatch exit %d, want 4", code)
	}
	testtree.Equal(t, before, testtree.Take(t, s.root, snapOpts))

	// Apply with the id we were shown.
	s.mustSy(t, "setup", "--yes", "--hub", "later", "--expect-plan", p.PlanID)
	for _, a := range p.Actions {
		if a.Op == "write_tree" {
			if _, err := os.Stat(a.Path); err != nil {
				t.Fatalf("write_tree target missing after apply: %v", err)
			}
		}
	}
	settings, err := os.ReadFile(h.Abs(".claude/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "switchyard@sy-local") {
		t.Fatalf("plugin not enabled in ~/.claude/settings.json:\n%s", settings)
	}
	if !strings.Contains(string(settings), "statusline") || !strings.Contains(string(settings), "statusline-command.sh") {
		t.Fatalf("status line not wrapped:\n%s", settings)
	}
	// Every link is still a link.
	for n := 2; n <= 7; n++ {
		for _, it := range detecttest.SharedItems {
			p := h.Abs(".claude-" + string(rune('0'+n)) + "/" + it)
			if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("%s is no longer a link", p)
			}
		}
	}

	// A second run has nothing to do.
	out = s.mustSy(t, "setup", "--dry-run", "--json", "--hub", "later")
	if p2 := parsePlan(t, out); len(p2.Actions) != 0 {
		t.Fatalf("second plan not empty:\n%s", out)
	}
	if out := s.mustSy(t, "setup", "--yes", "--hub", "later"); !strings.Contains(out, "0 changes") {
		t.Fatalf("second apply: %q", out)
	}

	// Undo restores the tree byte for byte.
	list := s.mustSy(t, "setup", "--undo-list", "--json")
	if !strings.Contains(list, `"sets"`) {
		t.Fatalf("undo-list: %s", list)
	}
	s.mustSy(t, "setup", "--undo", "--yes")
	testtree.Equal(t, before, testtree.Take(t, s.root, snapOpts))
}

func TestSetupNeedsPersonWithoutYes(t *testing.T) {
	s := newSetupHome(t, detecttest.Laptop(t))
	before := testtree.Take(t, s.root, snapOpts)
	out, _, code := s.sy(t, "", "setup", "--hub", "later")
	if code != 3 {
		t.Fatalf("non-TTY setup without --yes: exit %d, want 3\n%s", code, out)
	}
	testtree.Equal(t, before, testtree.Take(t, s.root, snapOpts))
	if _, _, code := s.sy(t, "", "setup", "--code", "x"); code != 2 {
		t.Fatalf("unknown flag exit %d, want 2", code)
	}
}

func TestSetupClaudeMissingExits7(t *testing.T) {
	h := detecttest.Fresh(t)
	s := newSetupHome(t, h)
	os.Remove(h.Abs(".local/bin/claude"))
	s.env[0] = "PATH=/nonexistent"
	if _, _, code := s.sy(t, "", "setup", "--yes"); code != 7 {
		t.Fatalf("claude missing: exit %d, want 7", code)
	}
}

func TestAccountAddCreatesNextFolder(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	s.mustSy(t, "setup", "--yes", "--hub", "later")

	out := s.mustSy(t, "account", "list", "--json")
	if !strings.Contains(out, ".claude-7") {
		t.Fatalf("account list: %s", out)
	}
	s.mustSy(t, "account", "add", "work", "--yes")
	dir := h.Abs(".claude-8")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("~/.claude-8 not created: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(dir, "settings.json"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("~/.claude-8/settings.json is not a link: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(s.syHome, "config", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "work") {
		t.Fatalf("label not saved:\n%s", cfg)
	}
}

func TestUninstallAfterSetupAndTick(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	s.mustSy(t, "setup", "--yes", "--hub", "later")
	if _, errOut, code := s.sy(t, "", "tick"); code != 0 {
		t.Fatalf("sy tick: exit %d\n%s", code, errOut)
	}
	out := s.mustSy(t, "uninstall", "--dry-run", "--json")
	if !strings.Contains(out, "uninstall") {
		t.Fatalf("uninstall plan: %s", out)
	}
	s.mustSy(t, "uninstall", "--yes")
	settings, err := os.ReadFile(h.Abs(".claude/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(settings), "sy statusline") {
		t.Fatalf("status line still wrapped:\n%s", settings)
	}
	if !strings.Contains(string(settings), "bash "+h.Root+"/.claude/statusline-command.sh") {
		t.Fatalf("original status line not restored:\n%s", settings)
	}
	if strings.Contains(string(settings), `"switchyard@sy-local": true`) {
		t.Fatalf("plugin still enabled:\n%s", settings)
	}
	for n := 2; n <= 7; n++ {
		p := h.Abs(".claude-" + string(rune('0'+n)) + "/settings.json")
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is no longer a link", p)
		}
	}
}
