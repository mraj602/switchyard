package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type harness struct {
	t    *testing.T
	home string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, home: t.TempDir()}
}

func (h *harness) path(rel string) string { return filepath.Join(h.home, filepath.FromSlash(rel)) }

func (h *harness) write(rel, content string) {
	h.t.Helper()
	p := h.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) read(rel string) string {
	h.t.Helper()
	b, err := os.ReadFile(h.path(rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// run executes the fake as folder dir ("" = CLAUDE_CONFIG_DIR unset) with extra env.
func (h *harness) run(dir string, env map[string]string, stdin string, args ...string) (int, string, string) {
	h.t.Helper()
	var out, errb bytes.Buffer
	a := &app{
		env: func(k string) string {
			if k == "HOME" {
				return h.home
			}
			if k == "CLAUDE_CONFIG_DIR" {
				return dir
			}
			return env[k]
		},
		stdin: strings.NewReader(stdin), stdout: &out, stderr: &errb,
		now: func() time.Time { return time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC) },
	}
	code := a.run(args)
	return code, out.String(), errb.String()
}

func (h *harness) mustRun(dir string, env map[string]string, args ...string) string {
	h.t.Helper()
	code, out, errb := h.run(dir, env, "", args...)
	if code != 0 {
		h.t.Fatalf("claude %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errb)
	}
	return out
}

func (h *harness) marketplace() string {
	h.write("mkt/.claude-plugin/marketplace.json",
		`{"name":"sy-local","owner":{"name":"x"},"plugins":[{"name":"switchyard","source":"./plugins/switchyard"}]}`)
	h.write("mkt/plugins/switchyard/.claude-plugin/plugin.json", `{"name":"switchyard","version":"9.9.9"}`)
	h.write("mkt/plugins/switchyard/hooks/hooks.json", `{"hooks":{}}`)
	return h.path("mkt")
}

func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

func TestVersionAndLog(t *testing.T) {
	h := newHarness(t)
	log := h.path("log.jsonl")
	env := map[string]string{"FAKE_CLAUDE_LOG": log}
	if out := h.mustRun("", env, "--version"); strings.TrimSpace(out) != "2.1.296 (Claude Code)" {
		t.Fatalf("version %q", out)
	}
	h.mustRun(h.path(".claude-2"), env, "agents", "--json")
	lines := strings.Split(strings.TrimSpace(h.read("log.jsonl")), "\n")
	if len(lines) != 2 {
		t.Fatalf("log: %v", lines)
	}
	var e struct {
		ConfigDir string   `json:"config_dir"`
		Unset     bool     `json:"unset"`
		Args      []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil || !e.Unset || e.ConfigDir != "" {
		t.Fatalf("first line %s", lines[0])
	}
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil || e.Unset || e.ConfigDir != h.path(".claude-2") || e.Args[0] != "agents" {
		t.Fatalf("second line %s", lines[1])
	}
	// every run created and rewrote the state files
	if _, err := os.Stat(h.path(".claude.json")); err != nil {
		t.Fatal("default state file missing")
	}
	if !strings.Contains(h.read(".claude-2/.claude.json"), `"numStartups": 1`) {
		t.Fatal(h.read(".claude-2/.claude.json"))
	}
	h.mustRun(h.path(".claude-2"), nil, "--version")
	if !strings.Contains(h.read(".claude-2/.claude.json"), `"numStartups": 2`) {
		t.Fatal("state file not rewritten on the second run")
	}
}

func TestAuthLoginAndStatus(t *testing.T) {
	h := newHarness(t)
	dir := h.path(".claude-2")
	code, out, _ := h.run(dir, nil, "", "auth", "status", "--json")
	if code == 0 || jsonOf(t, out)["loggedIn"] != false {
		t.Fatalf("logged out status: %d %s", code, out)
	}
	code, out, _ = h.run(dir, nil, "me@work.example:Team\n", "auth", "login", "--claudeai")
	if code != 0 || !strings.Contains(out, "Paste code here if prompted > ") || !strings.Contains(out, "https://claude.ai/oauth/authorize") {
		t.Fatalf("login: %d %s", code, out)
	}
	st := jsonOf(t, h.mustRun(dir, nil, "auth", "status", "--json"))
	if st["loggedIn"] != true || st["email"] != "me@work.example" || st["orgName"] != "Team" || st["authMethod"] != "claude.ai" {
		t.Fatalf("status %v", st)
	}
	for _, k := range []string{"apiProvider", "analyticsDisabled", "projectsDirectory", "configDirectory", "orgId", "subscriptionType"} {
		if _, ok := st[k]; !ok {
			t.Errorf("status lacks %s", k)
		}
	}
	if !strings.Contains(h.read(".claude-2/.claude.json"), `"oauthAccount"`) {
		t.Fatal("oauthAccount not written")
	}
	if _, err := os.Stat(h.path(".claude-2/.credentials.json")); err != nil {
		t.Fatal(err)
	}
	// state without credentials is logged out
	if err := os.Remove(h.path(".claude-2/.credentials.json")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.run(dir, nil, "", "auth", "status", "--json"); code == 0 {
		t.Fatal("still logged in without credentials")
	}
	// bad code
	if code, _, _ := h.run(dir, nil, "\n", "auth", "login", "--claudeai"); code == 0 {
		t.Fatal("empty code accepted")
	}
	// the default view uses ~/.claude.json
	h.run("", nil, "x@y.example\n", "auth", "login", "--claudeai")
	if !strings.Contains(h.read(".claude.json"), "x@y.example") {
		t.Fatal("default login must write ~/.claude.json")
	}
	if _, err := os.Stat(h.path(".claude/.credentials.json")); err != nil {
		t.Fatal(err)
	}
}

func skipNoSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
}

func TestPluginLifecycleWithLinks(t *testing.T) {
	skipNoSymlink(t)
	h := newHarness(t)
	mkt := h.marketplace()
	A, B, C := h.path(".claude"), h.path(".claude-2"), h.path(".claude-3")
	h.write(".claude/settings.json", "{\n  \"theme\": \"dark\"\n}\n")
	h.write(".claude/plugins/.keep", "")
	if err := os.MkdirAll(B, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(A, "plugins"), filepath.Join(B, "plugins")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(A, "settings.json"), filepath.Join(B, "settings.json")); err != nil {
		t.Fatal(err)
	}
	h.write(".claude-3/settings.json", "{\"model\":\"opus\"}\n")
	if err := os.Symlink(filepath.Join(A, "plugins"), filepath.Join(C, "plugins")); err != nil {
		t.Fatal(err)
	}

	// owner folder (~/.claude with the variable unset)
	r := jsonOf(t, h.mustRun("", nil, "plugin", "marketplace", "add", mkt, "--json"))
	if r["outcome"] != "ok" {
		t.Fatal(r)
	}
	r = jsonOf(t, h.mustRun("", nil, "plugin", "install", "switchyard@sy-local", "--json"))
	if r["outcome"] != "ok" {
		t.Fatal(r)
	}
	settings := h.read(".claude/settings.json")
	for _, want := range []string{`"theme": "dark"`, `"enabledPlugins"`, `"switchyard@sy-local": true`, `"extraKnownMarketplaces"`, `"source": "directory"`} {
		if !strings.Contains(settings, want) {
			t.Fatalf("settings lack %s:\n%s", want, settings)
		}
	}
	if strings.Index(settings, "theme") > strings.Index(settings, "enabledPlugins") {
		t.Fatal("insertion order not kept")
	}
	if !strings.Contains(h.read(".claude/plugins/installed_plugins.json"), A+"/plugins/cache/sy-local/switchyard/9.9.9") {
		t.Fatal(h.read(".claude/plugins/installed_plugins.json"))
	}
	if _, err := os.Stat(h.path(".claude/plugins/cache/sy-local/switchyard/9.9.9/hooks/hooks.json")); err != nil {
		t.Fatal(err)
	}

	// install again from a linked folder whose (shared) settings already enable it: a no-op,
	// the first installer's path stays (2.1.296), the link is kept.
	before, _ := os.Stat(filepath.Join(A, "settings.json"))
	h.mustRun(B, nil, "plugin", "install", "switchyard@sy-local", "--json")
	if !strings.Contains(h.read(".claude/plugins/installed_plugins.json"), A+"/plugins/cache/sy-local/switchyard/9.9.9") {
		t.Fatal("installPath moved on a no-op install:\n" + h.read(".claude/plugins/installed_plugins.json"))
	}
	if fi, err := os.Lstat(filepath.Join(B, "settings.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("settings link broken")
	}
	after, _ := os.Stat(filepath.Join(A, "settings.json"))
	if !os.SameFile(before, after) {
		t.Fatal("a no-op install must not rewrite settings")
	}

	// enable in a folder with its own settings and a shared root
	h.mustRun(C, nil, "plugin", "enable", "switchyard@sy-local", "--json")
	if !strings.Contains(h.read(".claude-3/settings.json"), `"switchyard@sy-local": true`) || !strings.Contains(h.read(".claude-3/settings.json"), `"model"`) {
		t.Fatal(h.read(".claude-3/settings.json"))
	}
	// disable
	h.mustRun(C, nil, "plugin", "disable", "switchyard@sy-local", "--json")
	if !strings.Contains(h.read(".claude-3/settings.json"), `"switchyard@sy-local": false`) {
		t.Fatal(h.read(".claude-3/settings.json"))
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(h.mustRun(C, nil, "plugin", "list", "--json")), &list); err != nil || len(list) != 1 || list[0]["enabled"] != false {
		t.Fatalf("list: %v %v", list, err)
	}

	// uninstall from B removes the record for every folder sharing the root; C's settings
	// still say (false here) and A's still say true.
	h.mustRun(B, nil, "plugin", "uninstall", "switchyard@sy-local", "--json")
	if err := json.Unmarshal([]byte(h.mustRun("", nil, "plugin", "list", "--json")), &list); err != nil || len(list) != 0 {
		t.Fatalf("record survived uninstall: %v", list)
	}
	h.mustRun(B, nil, "plugin", "marketplace", "remove", "sy-local", "--json")
	if strings.Contains(h.read(".claude/plugins/known_marketplaces.json"), "sy-local") {
		t.Fatal("marketplace not removed")
	}
	// enable of a plugin neither installed nor in a known marketplace fails with an outcome line
	if code, out, _ := h.run(C, nil, "", "plugin", "enable", "switchyard@sy-local", "--json"); code == 0 || jsonOf(t, out)["outcome"] != "failed" {
		t.Fatal("enable of an unknown plugin must fail")
	}
	// removing it again: failed / not_configured
	if code, out, _ := h.run(B, nil, "", "plugin", "marketplace", "remove", "sy-local", "--json"); code == 0 || jsonOf(t, out)["failureCode"] != "not_configured" {
		t.Fatal("second remove must fail with not_configured")
	}
}

func TestPluginRootFromSettingsEnvAndProcessEnv(t *testing.T) {
	h := newHarness(t)
	mkt := h.marketplace()
	shared := h.path("shared-plugins")
	h.write(".claude-4/settings.json", `{"env":{"CLAUDE_CODE_PLUGIN_CACHE_DIR":"`+filepath.ToSlash(shared)+`"}}`)
	D := h.path(".claude-4")
	h.mustRun(D, nil, "plugin", "marketplace", "add", mkt, "--json")
	h.mustRun(D, nil, "plugin", "install", "switchyard@sy-local", "--json")
	if _, err := os.Stat(filepath.Join(shared, "installed_plugins.json")); err != nil {
		t.Fatal("settings env root not honoured")
	}
	if _, err := os.Stat(h.path(".claude-4/plugins")); err == nil {
		t.Fatal("own plugins/ must stay unused")
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(h.mustRun(D, nil, "plugin", "list", "--json")), &list); err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	// process env wins
	other := h.path("other-root")
	env := map[string]string{"CLAUDE_CODE_PLUGIN_CACHE_DIR": other}
	if err := json.Unmarshal([]byte(h.mustRun(D, env, "plugin", "list", "--json")), &list); err != nil || len(list) != 0 {
		t.Fatalf("process env root not honoured: %v", list)
	}
}

func TestFaultInjection(t *testing.T) {
	skipNoSymlink(t)
	h := newHarness(t)
	mkt := h.marketplace()
	// FAIL
	for _, spec := range []string{"plugin install", "install", "plugin"} {
		code, out, _ := h.run(h.path(".claude-2"), map[string]string{"FAKE_CLAUDE_FAIL": spec}, "", "plugin", "install", "switchyard@sy-local", "--json")
		if code != 1 || jsonOf(t, out)["outcome"] != "error" {
			t.Fatalf("FAIL=%s: %d %s", spec, code, out)
		}
	}
	if code, _, _ := h.run(h.path(".claude-2"), map[string]string{"FAKE_CLAUDE_FAIL": "plugin install"}, "", "plugin", "list", "--json"); code != 0 {
		t.Fatal("FAIL=plugin install must not fail list")
	}
	if code, _, _ := h.run("", map[string]string{"FAKE_CLAUDE_FAIL": "auth status"}, "", "auth", "status", "--json"); code != 1 {
		t.Fatal("auth status not failed")
	}
	if code, _, _ := h.run("", map[string]string{"FAKE_CLAUDE_FAIL": "marketplace add"}, "", "plugin", "marketplace", "add", mkt, "--json"); code != 1 {
		t.Fatal("marketplace add not failed")
	}
	// BREAK_LINK
	h.write("real/settings.json", "{}\n")
	if err := os.MkdirAll(h.path(".claude-2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(h.path("real/settings.json"), h.path(".claude-2/settings.json")); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FAKE_CLAUDE_BREAK_LINK": "1"}
	h.mustRun(h.path(".claude-2"), env, "plugin", "marketplace", "add", mkt, "--json")
	if fi, err := os.Lstat(h.path(".claude-2/settings.json")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("link should have been replaced by a regular file")
	}
	if h.read("real/settings.json") != "{}\n" {
		t.Fatal("the link target must be untouched")
	}
	// SLOW
	start := time.Now()
	h.mustRun("", map[string]string{"FAKE_CLAUDE_SLOW": "150"}, "--version")
	if time.Since(start) < 140*time.Millisecond {
		t.Fatal("not slowed")
	}
}

func TestAgents(t *testing.T) {
	h := newHarness(t)
	if out := h.mustRun("", nil, "agents", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatal(out)
	}
	out := h.mustRun("", map[string]string{"FAKE_AGENTS": `[{"id":"s1","status":"idle"}]`}, "agents", "--json")
	if strings.TrimSpace(out) != `[{"id":"s1","status":"idle"}]` {
		t.Fatal(out)
	}
}

func TestHelpAndUnknown(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("", nil, "plugin", "marketplace", "--help")
	if !strings.Contains(out, "\n  add ") {
		t.Fatal(out)
	}
	if code, _, _ := h.run("", nil, "", "bogus"); code == 0 {
		t.Fatal("unknown command accepted")
	}
}
