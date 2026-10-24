package detect_test

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/setup/detect"
	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// recFS records every path handed to the guarded file system.
type recFS struct {
	mu    sync.Mutex
	paths []string
}

func (r *recFS) rec(p string) {
	r.mu.Lock()
	r.paths = append(r.paths, p)
	r.mu.Unlock()
}
func (r *recFS) Open(n string) (io.ReadCloser, error)    { r.rec(n); return identity.OSFS{}.Open(n) }
func (r *recFS) Stat(n string) (fs.FileInfo, error)      { r.rec(n); return identity.OSFS{}.Stat(n) }
func (r *recFS) ReadDir(n string) ([]fs.DirEntry, error) { r.rec(n); return identity.OSFS{}.ReadDir(n) }
func (r *recFS) EvalSymlinks(n string) (string, error) {
	r.rec(n)
	return identity.OSFS{}.EvalSymlinks(n)
}

func folder(t *testing.T, st model.State, name string) model.Folder {
	t.Helper()
	for _, f := range st.Folders {
		if f.Folder == name {
			return f
		}
	}
	t.Fatalf("no folder %s in %v", name, st.Folders)
	return model.Folder{}
}

func TestLaptop(t *testing.T) {
	h := detecttest.Laptop(t)
	rec := &recFS{}
	d := h.Deps()
	d.FS = rec
	st, err := detect.Detect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}

	if len(st.Folders) != 7 {
		t.Fatalf("folders: %d", len(st.Folders))
	}
	for i, want := range []string{"~/.claude", "~/.claude-2", "~/.claude-3", "~/.claude-4", "~/.claude-5", "~/.claude-6", "~/.claude-7"} {
		if st.Folders[i].Folder != want {
			t.Errorf("folder %d = %s want %s", i, st.Folders[i].Folder, want)
		}
	}
	for _, f := range st.Folders {
		if strings.Contains(f.Folder, "api-env") {
			t.Errorf("file .claude-api-env became a folder")
		}
	}
	prim := folder(t, st, "~/.claude")
	if !prim.Primary || !prim.Default || !prim.LoggedIn || prim.Plan != "max" {
		t.Errorf("primary: %+v", prim)
	}
	if f := folder(t, st, "~/.claude-4"); f.LoggedIn {
		t.Errorf("-4 logged in")
	}
	if f := folder(t, st, "~/.claude-3"); !f.LoggedIn || f.Plan != "team" || f.Email != "you@team.example" {
		t.Errorf("-3: %+v", f)
	}
	linked := 0
	for _, f := range st.Folders[1:] {
		for _, it := range detecttest.SharedItems {
			s := f.Items[it]
			if s.State != model.ItemLinked || s.LinkRaw != filepath.Join(h.Root, ".claude", it) {
				t.Errorf("%s %s: %+v", f.Folder, it, s)
			} else {
				linked++
			}
		}
		for _, it := range []string{"agents", "rules", "keybindings.json"} {
			if f.Items[it].State != model.ItemAbsent {
				t.Errorf("%s %s should be absent", f.Folder, it)
			}
		}
	}
	if linked != 48 || len(st.Links) != 48 {
		t.Errorf("links: %d items, %d in baseline", linked, len(st.Links))
	}
	if prim.Items["settings.json"].State != model.ItemFile || prim.Items["settings.json"].SHA256 == "" {
		t.Errorf("primary settings: %+v", prim.Items["settings.json"])
	}

	if len(st.SettingsTargets) != 1 || len(st.SettingsTargets[0].Folders) != 7 {
		t.Fatalf("targets: %+v", st.SettingsTargets)
	}
	tg := st.SettingsTargets[0]
	if tg.StatusLine == nil || tg.StatusLine.Ours || !strings.HasSuffix(tg.StatusLine.Command, "statusline-command.sh") {
		t.Errorf("statusline: %+v", tg.StatusLine)
	}
	if len(tg.EnabledPlugins) != 3 || len(tg.WatchyardPlugins) != 0 || len(tg.ExtraMarketplaces) != 1 {
		t.Errorf("plugins: %+v", tg)
	}
	if len(st.PluginRoots) != 1 {
		t.Fatalf("roots: %+v", st.PluginRoots)
	}
	pr := st.PluginRoots[0]
	if pr.Owner != "~/.claude" || len(pr.Folders) != 7 || len(pr.Plugins) != 3 || !pr.Adopted || pr.FromEnv || pr.Sources["market"] != "someone/market" {
		t.Errorf("root: %+v", pr)
	}
	if !prim.OwnsRoot || folder(t, st, "~/.claude-2").OwnsRoot {
		t.Errorf("OwnsRoot wrong")
	}

	for _, f := range st.Folders {
		if f.Launcher == nil || f.Launcher.Kind != model.LauncherFunction || f.Launcher.Ours {
			t.Errorf("%s launcher: %+v", f.Folder, f.Launcher)
		}
	}
	if !st.MCP.HandMade || st.MCP.Script != "~/.claude/sync-mcp-to-2.sh" {
		t.Errorf("mcp: %+v", st.MCP)
	}
	if st.Claude.Version != "2.1.296" || st.Claude.Path != "~/.local/bin/claude" {
		t.Errorf("claude: %+v", st.Claude)
	}
	if st.Tailscale == nil || !st.Tailscale.Up || st.Hub.URL != nil || st.ConfigPresent {
		t.Errorf("watchyard facts: %+v %+v", st.Tailscale, st.Hub)
	}
	found := false
	for _, n := range st.HomeNames {
		if n == ".claude-api-env" {
			found = true
		}
	}
	if !found {
		t.Errorf("HomeNames: %v", st.HomeNames)
	}

	// No forbidden file is ever handed to the file system, and the one that exists was not read.
	if len(rec.paths) == 0 {
		t.Fatal("recorder saw nothing")
	}
	for _, p := range rec.paths {
		if identity.Forbidden(p) || strings.HasSuffix(p, ".credentials.json") {
			t.Errorf("forbidden path opened: %s", p)
		}
	}
	// Tmp files and the api-env file are not part of any state.
	b, _ := os.ReadFile(filepath.Join(h.Root, ".claude/.credentials.json"))
	if len(b) == 0 {
		t.Fatal("fixture lost")
	}
}

func TestFresh(t *testing.T) {
	h := detecttest.Fresh(t)
	st := h.State()
	if len(st.Folders) != 1 || !st.Folders[0].Primary || len(st.Links) != 0 {
		t.Fatalf("%+v", st.Folders)
	}
	it := st.Folders[0].Items
	if it["settings.json"].State != model.ItemFile || it["CLAUDE.md"].State != model.ItemFile || it["skills"].State != model.ItemAbsent {
		t.Errorf("items: %+v", it)
	}
	if st.MCP.HandMade || len(st.Launchers) != 0 {
		t.Errorf("mcp/launchers: %+v %+v", st.MCP, st.Launchers)
	}
	if st.Share.Primary != "" || st.StateDir == "" || st.Sy == "" {
		t.Errorf("%+v", st)
	}
}

func TestConflictsAndElsewhere(t *testing.T) {
	h := detecttest.Fresh(t)
	b := h.B
	b.File(".claude/skills/a/SKILL.md", "a\n", 0o644)
	b.File(".claude/skills/b/SKILL.md", "b\n", 0o644)
	b.File(".claude/projects/p1/s1.jsonl", "1\n", 0o644)
	b.File(".claude/projects/p1/s2.jsonl", "2\n", 0o644)
	b.File(".claude-2/.claude.json", `{}`, 0o600)
	b.File(".claude-2/CLAUDE.md", "# me\n", 0o644)                  // identical: replace
	b.File(".claude-2/skills/a/SKILL.md", "a\n", 0o644)             // same
	b.File(".claude-2/skills/b/SKILL.md", "DIFFERENT\n", 0o644)     // differs
	b.File(".claude-2/skills/c/SKILL.md", "c\n", 0o644)             // missing in primary
	b.File(".claude-2/projects/p1/s1.jsonl", "1\n", 0o644)          // same
	b.File(".claude-2/projects/p1/s3.jsonl", "3\n", 0o644)          // missing deeper
	b.File(".claude-2/projects/p2/x.jsonl", "x\n", 0o644)           // missing project
	b.File(".claude-2/history.jsonl", "{\"timestamp\":1}\n", 0o644) // primary has none
	b.File(".claude-3/.claude.json", `{}`, 0o600)
	b.File(".elsewhere/skills/x", "x", 0o644)
	b.Symlink(".claude-3/skills", "$ROOT/.elsewhere/skills")
	b.File(".claude-3/CLAUDE.md", "# other\n", 0o644) // differs
	b.File(".claude-3/history.jsonl", "{\"timestamp\":1}\n", 0o644)
	st := h.State()

	f2 := folder(t, st, "~/.claude-2")
	if f2.Items["CLAUDE.md"].Compare != model.CompareSame {
		t.Errorf("CLAUDE.md: %+v", f2.Items["CLAUDE.md"])
	}
	sk := f2.Items["skills"]
	if sk.Compare != model.CompareDiffers || sk.Diff == nil || len(sk.Diff.Missing) != 1 || sk.Diff.Missing[0] != "c" || len(sk.Diff.Differ) != 1 || sk.Diff.Differ[0] != "b" {
		t.Errorf("skills: %+v %+v", sk, sk.Diff)
	}
	pj := f2.Items["projects"]
	if pj.Diff == nil || strings.Join(pj.Diff.Missing, ",") != "p1/s3.jsonl,p2" || len(pj.Diff.Differ) != 0 {
		t.Errorf("projects diff: %+v", pj.Diff)
	}
	// Primary has no history.jsonl: the two folders that have one agree, so -3 is "same".
	if f2.Items["history.jsonl"].Compare != "" || folder(t, st, "~/.claude-3").Items["history.jsonl"].Compare != model.CompareSame {
		t.Errorf("history: %+v / %+v", f2.Items["history.jsonl"], folder(t, st, "~/.claude-3").Items["history.jsonl"])
	}
	f3 := folder(t, st, "~/.claude-3")
	if f3.Items["skills"].State != model.ItemElsewhere || f3.Items["CLAUDE.md"].Compare != model.CompareDiffers {
		t.Errorf("-3: %+v", f3.Items)
	}
}

func TestDuplicatesAndRoots(t *testing.T) {
	h := detecttest.Fresh(t)
	b := h.B
	b.File(".claude/settings.json", `{"enabledPlugins":{"watchyard@wy-local":true,"watchyard@github":true,"other@x":true,"watchyard@off":false},
 "env":{"CLAUDE_CODE_PLUGIN_CACHE_DIR":"`+h.Abs("shared-plugins")+`","SECRET":"hunter2"},
 "statusLine":{"type":"command","command":"/x/wy statusline --wrap 'a'"}}`, 0o644)
	b.File(".claude/skills/watchyard/.claude-plugin/plugin.json", `{"name":"watchyard"}`, 0o644)
	b.File(".claude-2/.claude.json", `{}`, 0o600)
	b.File(".claude-2/plugins/installed_plugins.json", `{"version":2,"plugins":{"z@m":[{}]}}`, 0o644)
	st := h.State()
	tg := st.SettingsTargets[0]
	want := "watchyard@github,watchyard@skills-dir,watchyard@wy-local"
	if strings.Join(tg.WatchyardPlugins, ",") != want {
		t.Errorf("watchyard plugins: %v", tg.WatchyardPlugins)
	}
	if !tg.StatusLine.Ours || len(tg.EnvKeys) != 2 || tg.PluginCacheDir != h.Abs("shared-plugins") {
		t.Errorf("target: %+v", tg)
	}
	if st.Folders[0].PluginRoot != h.Abs("shared-plugins") || !st.PluginRoots[0].FromEnv {
		t.Errorf("roots: %+v", st.PluginRoots)
	}
	// The secret value is nowhere in the state.
	js := mustJSON(t, st)
	if strings.Contains(js, "hunter2") {
		t.Error("env value leaked into State")
	}
	f2 := folder(t, st, "~/.claude-2")
	if f2.PluginRoot != filepath.Join(h.Root, ".claude-2/plugins") || !f2.OwnsRoot {
		t.Errorf("-2 root: %+v", f2)
	}
}

func TestWindowsStyleNoSymlinks(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	h.B.File(".claude-2/settings.json", "{\n  \"theme\": \"dark\"\n}\n", 0o644) // a copy, byte-identical
	d := h.Deps()
	d.GOOS = "windows"
	d.SymlinkProbe = func() bool { return false }
	st, err := detect.Detect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if st.SymlinkOK == nil || *st.SymlinkOK || st.OS != "windows" {
		t.Errorf("symlink ok: %v", st.SymlinkOK)
	}
	if it := folder(t, st, "~/.claude-2").Items["settings.json"]; it.State != model.ItemFile || it.Compare != model.CompareSame {
		t.Errorf("copy: %+v", it)
	}
}

func TestLauncherFilesAndOurs(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	h.B.File(".claude-3/.claude.json", `{}`, 0o600)
	h.B.File(".local/bin/claude-2", "#!/bin/sh\n# sy-launcher v1 slot=~/.claude-2 label=work\nexec claude\n", 0o755)
	h.B.File(".local/bin/claude-3", "#!/bin/sh\nCLAUDE_CONFIG_DIR=$HOME/.claude-3 exec claude \"$@\"\n", 0o755)
	h.B.File(".local/bin/claude-notexec", "x", 0o644)
	st := h.State()
	if l := folder(t, st, "~/.claude-2").Launcher; l == nil || !l.Ours || l.Kind != model.LauncherFile {
		t.Errorf("-2: %+v", l)
	}
	if l := folder(t, st, "~/.claude-3").Launcher; l == nil || l.Ours {
		t.Errorf("-3: %+v", l)
	}
	for _, l := range st.Launchers {
		if l.Name == "claude-notexec" {
			t.Error("non-executable file counted as launcher")
		}
	}
}

func TestShellProbeAndConfig(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	h.B.File(".config/watchyard/config.toml", "version = 1\n[share]\nprimary = \"~/.claude\"\nmcp = \"external\"\n[slots.labels]\n\"~/.claude-2\" = \"work\"\n[hub]\nurl = \"https://hub.example\"\n", 0o600)
	d := h.Deps()
	d.ShellProbe = func(_ context.Context, names []string) (string, error) {
		return "claude is a function\nclaude-2 is aliased to `x'\nbash: type: claude-3: not found\nclaude-9 is a function\n", nil
	}
	d.HubProbe = func(context.Context, string) model.HubState { return model.HubState{Healthy: true} }
	st, err := detect.Detect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if l := folder(t, st, "~/.claude").Launcher; l == nil || l.Kind != model.LauncherFunction {
		t.Errorf("claude: %+v", l)
	}
	if l := folder(t, st, "~/.claude-2").Launcher; l == nil || l.Kind != model.LauncherAlias {
		t.Errorf("-2: %+v", l)
	}
	f2 := folder(t, st, "~/.claude-2")
	if f2.Label != "work" || !st.ConfigPresent || st.Share.MCP != "external" || st.Share.Primary != "~/.claude" {
		t.Errorf("config: %+v %+v", f2, st.Share)
	}
	if st.Hub.URL == nil || *st.Hub.URL != "https://hub.example" || !st.Hub.Healthy {
		t.Errorf("hub: %+v", st.Hub)
	}
}

// Regression: an invalid label in a hand-edited config.toml (newline, path separators) is
// ignored with a warning; valid ones are kept.
func TestInvalidConfigLabelIgnored(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	h.B.File(".claude-3/.claude.json", `{}`, 0o600)
	h.B.File(".config/watchyard/config.toml", "version = 1\n[slots.labels]\n\"~/.claude-2\" = \"x/../../evil\\ntouch /tmp/pwned\"\n\"~/.claude-3\" = \"client\"\n", 0o600)
	st, err := detect.Detect(context.Background(), h.Deps())
	if err != nil {
		t.Fatal(err)
	}
	if l := folder(t, st, "~/.claude-2").Label; l != "" {
		t.Errorf("invalid label kept: %q", l)
	}
	if l := folder(t, st, "~/.claude-3").Label; l != "client" {
		t.Errorf("valid label lost: %q", l)
	}
	if w := strings.Join(st.Warnings, "\n"); !strings.Contains(w, "invalid label") || !strings.Contains(w, "~/.claude-2") {
		t.Errorf("warnings: %v", st.Warnings)
	}
}

func TestNoClaude(t *testing.T) {
	h := detecttest.Fresh(t)
	d := h.Deps()
	d.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := os.Remove(h.Abs(".local/bin/claude")); err != nil {
		t.Fatal(err)
	}
	d.Claude = failingRunner{h.Fake}
	st, err := detect.Detect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if st.Claude.Path != "" {
		t.Errorf("claude found: %+v", st.Claude)
	}
}

// rename.md 2.4: plugins of the project's name before a rename are found apart from the
// current ones.
func TestLegacyPluginsDetected(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	h := detecttest.Fresh(t)
	h.B.File(".claude/settings.json", `{"enabledPlugins":{"oldname@wy-local":true,"oldname-pane@wy-local":true,"oldname@off":false,"watchyard@wy-local":true}}`, 0o644)
	h.B.File(".claude/skills/old/.claude-plugin/plugin.json", `{"name":"oldname"}`, 0o644)
	tg := h.State().SettingsTargets[0]
	if got := strings.Join(tg.LegacyPlugins, ","); got != "oldname-pane@wy-local,oldname@skills-dir,oldname@wy-local" {
		t.Errorf("legacy: %s", got)
	}
	if got := strings.Join(tg.WatchyardPlugins, ","); got != "watchyard@wy-local" {
		t.Errorf("current: %s", got)
	}
}
