package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/pluginmkt"
)

// The contract test runs one script of plugin commands against the real `claude` and against
// this fake, each in its own throwaway HOME, and compares the resulting trees after
// normalising times, versions and paths. Run it by hand before tagging and whenever Claude
// Code updates:
//
//	SY_E2E_REAL_CLAUDE=1 go test ./test/e2e/setup/fakeclaude -run Contract -v
//
// SY_E2E_KEEP_DIR=<dir> keeps each throwaway HOME (plus a .log of every command) under dir.
//
// SY_E2E_REAL_CLAUDE_BIN names the real binary (default: claude on PATH, which must be the
// executable, not a shell function). The real claude only ever sees the throwaway HOME and a
// CLAUDE_CONFIG_DIR inside it; no real ~/.claude* folder is touched. The result is recorded
// in docs/spec/setup.md with the Claude Code version.

// cleanEnv is the whole environment given to either binary.
func cleanEnv(home, configDir string, extra ...string) []string {
	env := []string{
		"HOME=" + home, "USERPROFILE=" + home, "PATH=" + os.Getenv("PATH"), "TERM=dumb",
		"DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
	}
	if r := os.Getenv("SYSTEMROOT"); r != "" {
		env = append(env, "SYSTEMROOT="+r)
	}
	if configDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	}
	return append(env, extra...)
}

type scriptRun struct {
	t     *testing.T
	bin   string
	home  string
	steps []step
}

type step struct {
	name string
	tree map[string]string
}

func (r *scriptRun) p(rel string) string { return filepath.Join(r.home, filepath.FromSlash(rel)) }

// claude runs the binary as folder rel ("" = ~/.claude with the variable unset).
func (r *scriptRun) claude(folder string, args ...string) []byte {
	r.t.Helper()
	dir := ""
	if folder != "" {
		dir = r.p(folder)
	}
	cmd := exec.Command(r.bin, args...)
	cmd.Env = cleanEnv(r.home, dir)
	// A neutral working folder: with cwd = HOME the real claude reads ~/.claude/settings.json
	// as the *project* settings too, so `enable` in another folder says "already enabled".
	cmd.Dir = r.p("work")
	if err := os.MkdirAll(cmd.Dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if os.Getenv("SY_E2E_KEEP_DIR") != "" {
		// debug aid: a log of every command and its output, next to the kept HOME
		if f, ferr := os.OpenFile(r.home+".log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
			fmt.Fprintf(f, "$ [%s] claude %s\n(exit err: %v)\n%s\n", folder, strings.Join(args, " "), err, out)
			f.Close()
		}
	}
	if err != nil {
		r.t.Fatalf("%s: claude %s (folder %q): %v\n%s", r.bin, strings.Join(args, " "), folder, err, out)
	}
	return out
}

// try runs a command that may fail and returns its exit code, normalised --json stdout line
// and stderr text, so the outcome shapes of the failure paths are compared too.
func (r *scriptRun) try(folder string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.bin, args...)
	cmd.Env = cleanEnv(r.home, r.p(folder))
	cmd.Dir = r.p("work")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			r.t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	if os.Getenv("SY_E2E_KEEP_DIR") != "" {
		if f, ferr := os.OpenFile(r.home+".log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
			fmt.Fprintf(f, "$ [%s] claude %s\n(exit %d)\nstdout: %sstderr: %s\n", folder, strings.Join(args, " "), code, so.String(), se.String())
			f.Close()
		}
	}
	return fmt.Sprintf("exit %d\n%s\nstderr: %s", code, normalizeJSONText(so.Bytes(), r.home),
		strings.TrimSpace(strings.ReplaceAll(se.String(), r.home, "<HOME>")))
}

func (r *scriptRun) write(rel, content string) {
	r.t.Helper()
	p := r.p(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *scriptRun) link(rel, target string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(r.p(rel)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Symlink(r.p(target), r.p(rel)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *scriptRun) snap(name string, outs map[string][]byte) {
	r.t.Helper()
	tree := snapshot(r.t, r.home)
	for k, v := range outs {
		tree["out:"+k] = normalizeJSONText(v, r.home)
	}
	r.steps = append(r.steps, step{name, tree})
}

// snapText is snap for outputs that are already normalised text.
func (r *scriptRun) snapText(name string, outs map[string][]byte) {
	r.t.Helper()
	tree := snapshot(r.t, r.home)
	for k, v := range outs {
		tree["out:"+k] = string(v)
	}
	r.steps = append(r.steps, step{name, tree})
}

var timeRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

// normalise rewrites a decoded JSON value: home paths, times and versions.
func normalise(v any, home string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			switch k {
			case "version", "gitCommitSha":
				if _, ok := e.(string); ok {
					x[k] = "<version>"
					continue
				}
			case "installedAt", "lastUpdated", "expiresAt":
				x[k] = "<time>"
				continue
			}
			x[k] = normalise(e, home)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = normalise(e, home)
		}
		return x
	case string:
		s := strings.ReplaceAll(x, filepath.ToSlash(home), "<HOME>")
		s = strings.ReplaceAll(s, home, "<HOME>")
		if timeRE.MatchString(s) {
			return "<time>"
		}
		return s
	}
	return v
}

func normalizeJSONText(b []byte, home string) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		// not JSON (a message line): compare nothing
		return "<not json>"
	}
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	_ = e.Encode(normalise(v, home))
	return strings.TrimSpace(buf.String())
}

// snapshot lists every path under home (except the marketplace source, which the script
// made itself): links with their normalised target, JSON records and settings normalised,
// other files by name only. State files (.claude.json) are listed by presence only: the real
// one holds a lot the fake does not model.
func snapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "mkt") || strings.HasPrefix(rel, "sybin") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, "/backups") || strings.Contains(rel, "/backups/") || rel == "work" {
			// the real claude keeps timestamped .claude.json backups; the fake does not
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			tree[rel] = "link -> " + strings.ReplaceAll(filepath.ToSlash(l), filepath.ToSlash(home), "<HOME>")
		case d.IsDir():
			tree[rel] = "dir"
		case strings.HasPrefix(filepath.Base(rel), ".claude.json"):
			tree[rel] = "file (state)"
		case strings.HasSuffix(rel, ".json") && !strings.Contains(rel, "/cache/"):
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			tree[rel] = normalizeJSONText(b, home)
		default:
			tree[rel] = "file"
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// runScript is the contract script: marketplace add, install from the owner folder, install
// again from a linked folder, enable in a folder with its own settings, uninstall, and a
// plugin root named in settings env.
func runScript(t *testing.T, bin string) []step {
	t.Helper()
	r := &scriptRun{t: t, bin: bin, home: t.TempDir()}
	if keep := os.Getenv("SY_E2E_KEEP_DIR"); keep != "" {
		// debug aid: keep the throwaway HOME (and a command log beside it) for inspection
		if err := os.MkdirAll(keep, 0o755); err != nil {
			t.Fatal(err)
		}
		d, err := os.MkdirTemp(keep, filepath.Base(filepath.Dir(bin))[:0]+"home-")
		if err != nil {
			t.Fatal(err)
		}
		r.home = d
		t.Logf("kept HOME: %s", d)
	}
	// resolve symlinks in the temp path (macOS /var -> /private/var) so both sides agree
	if h, err := filepath.EvalSymlinks(r.home); err == nil {
		r.home = h
	}
	// The marketplace is the one sy writes, so the contract covers what sy really installs.
	mkt := r.p("mkt")
	if err := pluginmkt.New(nil).WriteMarketplace(mkt, r.p("sybin/sy"), "0.0.1"); err != nil {
		t.Fatal(err)
	}
	const id = "switchyard@sy-local"

	// folders: A = ~/.claude (variable unset), B links settings and plugins to A, C links
	// plugins to A but has its own settings, D names a plugin root in settings env.
	r.write(".claude/settings.json", "{\n  \"theme\": \"dark\"\n}\n")
	if err := os.MkdirAll(r.p(".claude/plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.link(".claude-2/settings.json", ".claude/settings.json")
	r.link(".claude-2/plugins", ".claude/plugins")
	r.write(".claude-3/settings.json", "{\n  \"editorMode\": \"vim\"\n}\n")
	r.link(".claude-3/plugins", ".claude/plugins")
	r.write(".claude-4/settings.json", fmt.Sprintf("{\n  \"env\": {\"CLAUDE_CODE_PLUGIN_CACHE_DIR\": %q}\n}\n", filepath.ToSlash(r.p("shared-plugins"))))
	r.write(".claude-5/settings.json", "{\n  \"theme\": \"light\"\n}\n")
	r.link(".claude-5/plugins", ".claude/plugins")
	r.snap("0 setup", nil)

	r.claude("", "plugin", "marketplace", "add", mkt, "--json")
	r.snap("1 marketplace add (owner)", map[string][]byte{"marketplace list": r.claude("", "plugin", "marketplace", "list", "--json")})

	r.claude("", "plugin", "install", id, "--json")
	r.snap("2 install (owner)", map[string][]byte{"list A": r.claude("", "plugin", "list", "--json")})

	r.claude(".claude-2", "plugin", "install", id, "--json")
	r.snap("3 install again (linked folder)", map[string][]byte{"list B": r.claude(".claude-2", "plugin", "list", "--json")})

	// install where the record exists but this folder's own settings lack enabledPlugins
	r.claude(".claude-5", "plugin", "install", id, "--json")
	r.snap("3b install (own settings, already installed)", map[string][]byte{"list E": r.claude(".claude-5", "plugin", "list", "--json")})

	r.claude(".claude-3", "plugin", "enable", id, "--json")
	r.snap("4 enable (own settings)", map[string][]byte{"list C": r.claude(".claude-3", "plugin", "list", "--json")})

	r.claude(".claude-2", "plugin", "uninstall", id, "--json")
	r.snap("5 uninstall (linked folder)", map[string][]byte{"list A": r.claude("", "plugin", "list", "--json")})

	r.claude(".claude-4", "plugin", "marketplace", "add", mkt, "--json")
	r.claude(".claude-4", "plugin", "install", id, "--json")
	r.snap("6 settings-env plugin root", map[string][]byte{"list D": r.claude(".claude-4", "plugin", "list", "--json")})

	// The failure and already-done paths, in folder D (own root, own settings): every command
	// that setup.md 5.2 relies on being idempotent.
	d := ".claude-4"
	outs := map[string][]byte{}
	for i, c := range [][]string{
		{"enable", id},
		{"disable", id},
		{"disable", id},
		{"enable", id},
		{"install", id},
		{"marketplace", "add", mkt},
		{"uninstall", id},
		{"uninstall", id},
		{"enable", id},
		{"install", "nothere@sy-local"},
		{"install", "switchyard@nomkt"},
		{"marketplace", "remove", "sy-local"},
		{"marketplace", "remove", "sy-local"},
	} {
		args := append([]string{"plugin"}, c...)
		args = append(args, "--json")
		outs[fmt.Sprintf("7.%02d %s", i, strings.ReplaceAll(strings.Join(c, " "), mkt, "<mkt>"))] = []byte(r.try(d, args...))
	}
	r.snapText("7 idempotence and failures", outs)
	return r.steps
}

func diffSteps(a, b []step) []string {
	var out []string
	if len(a) != len(b) {
		return []string{fmt.Sprintf("different number of steps: %d vs %d", len(a), len(b))}
	}
	for i := range a {
		keys := map[string]bool{}
		for k := range a[i].tree {
			keys[k] = true
		}
		for k := range b[i].tree {
			keys[k] = true
		}
		var ks []string
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			x, xok := a[i].tree[k]
			y, yok := b[i].tree[k]
			switch {
			case !xok:
				out = append(out, fmt.Sprintf("[%s] only in fake: %s\n%s", a[i].name, k, indent(y)))
			case !yok:
				out = append(out, fmt.Sprintf("[%s] only in real: %s\n%s", a[i].name, k, indent(x)))
			case x != y:
				out = append(out, fmt.Sprintf("[%s] %s differs\n--- real ---\n%s\n--- fake ---\n%s", a[i].name, k, indent(x), indent(y)))
			}
		}
	}
	return out
}

func indent(s string) string { return "    " + strings.ReplaceAll(s, "\n", "\n    ") }

// buildFake compiles this package as `claude` into a temp folder.
func buildFake(t *testing.T) string {
	t.Helper()
	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	out, err := exec.Command("go", "build", "-o", bin, "github.com/mraj602/switchyard/test/e2e/setup/fakeclaude").CombinedOutput()
	if err != nil {
		t.Skipf("cannot build the fake: %v\n%s", err, out)
	}
	return bin
}

// TestContractScriptOnFake runs the contract script on the fake alone and checks the
// properties setup.md 5.2 and 13 state, so the script itself stays honest without the real
// binary.
func TestContractScriptOnFake(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script uses symlinks")
	}
	steps := runScript(t, buildFake(t))
	final := steps[len(steps)-1].tree
	for _, want := range []string{
		".claude-2/settings.json", ".claude-2/plugins", "shared-plugins/installed_plugins.json",
	} {
		if _, ok := final[want]; !ok {
			t.Errorf("final tree lacks %s", want)
		}
	}
	if !strings.HasPrefix(final[".claude-2/settings.json"], "link -> ") {
		t.Error("settings link was not kept")
	}
	s3 := steps[3].tree
	if !strings.Contains(s3["out:list B"], "<HOME>/.claude/plugins/cache/sy-local/switchyard") {
		t.Errorf("the first installer stays (no rewrite):\n%s", s3["out:list B"])
	}
	if strings.Contains(steps[6].tree["out:list A"], "switchyard@sy-local") {
		t.Error("uninstall must remove the record for every folder sharing the root")
	}
	if !strings.Contains(steps[6].tree[".claude-3/settings.json"], `"switchyard@sy-local": true`) {
		t.Error("another folder's settings must keep their enabledPlugins entry after an uninstall elsewhere")
	}
}

// TestContract compares the fake with the real claude. It runs only with SY_E2E_REAL_CLAUDE=1.
func TestContract(t *testing.T) {
	if os.Getenv("SY_E2E_REAL_CLAUDE") != "1" {
		t.Skip("set SY_E2E_REAL_CLAUDE=1 to compare the fake with the real claude (run by hand before tagging)")
	}
	real := os.Getenv("SY_E2E_REAL_CLAUDE_BIN")
	if real == "" {
		var err error
		if real, err = exec.LookPath("claude"); err != nil {
			t.Skip("claude not found (set SY_E2E_REAL_CLAUDE_BIN)")
		}
	}
	vcmd := exec.Command(real, "--version")
	vcmd.Env = cleanEnv(t.TempDir(), "")
	if v, err := vcmd.Output(); err == nil {
		t.Logf("real claude: %s", strings.TrimSpace(string(v)))
	}
	realSteps := runScript(t, real)
	fakeSteps := runScript(t, buildFake(t))
	if diffs := diffSteps(realSteps, fakeSteps); len(diffs) > 0 {
		t.Fatalf("the fake differs from the real claude in %d places:\n\n%s", len(diffs), strings.Join(diffs, "\n\n"))
	}
}
