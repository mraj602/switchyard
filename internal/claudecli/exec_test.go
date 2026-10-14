package claudecli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBin is the e2e fake `claude` (test/e2e/setup/fakeclaude), built once. Tests that need
// it skip when the go tool is not available.
var (
	fakeBin   string
	fakeBuild error
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "claudecli-fake")
	if err != nil {
		os.Exit(1)
	}
	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	fakeBin = filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", fakeBin, "github.com/mraj602/switchyard/test/e2e/setup/fakeclaude")
	if out, err := cmd.CombinedOutput(); err != nil {
		fakeBuild = errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type rig struct {
	t    *testing.T
	home string
	x    *Exec
	log  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if fakeBuild != nil {
		t.Skipf("fake claude not built: %v", fakeBuild)
	}
	home := t.TempDir()
	log := filepath.Join(home, "calls.jsonl")
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "PATH=" + os.Getenv("PATH"),
		"FAKE_CLAUDE_LOG=" + log}
	if r := os.Getenv("SYSTEMROOT"); r != "" {
		env = append(env, "SYSTEMROOT="+r)
	}
	return &rig{t: t, home: home, x: &Exec{Path: fakeBin, Environ: env}, log: log}
}

func (r *rig) folder(name string) Folder { return Folder{Dir: filepath.Join(r.home, name)} }

func (r *rig) marketplace() string {
	r.t.Helper()
	dir := filepath.Join(r.home, "mkt")
	for p, c := range map[string]string{
		".claude-plugin/marketplace.json":               `{"name":"sy-local","owner":{"name":"x"},"plugins":[{"name":"switchyard","source":"./plugins/switchyard"}]}`,
		"plugins/switchyard/.claude-plugin/plugin.json": `{"name":"switchyard","version":"1.2.3"}`,
		"plugins/switchyard/hooks/hooks.json":           `{"hooks":{}}`,
	} {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	return dir
}

func TestExecVersion(t *testing.T) {
	r := newRig(t)
	v, err := r.x.Version(context.Background())
	if err != nil || v.Version != "2.1.296" || v.Raw != "2.1.296 (Claude Code)" || v.Path != fakeBin {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestExecNotFound(t *testing.T) {
	x := &Exec{Path: filepath.Join(t.TempDir(), "nope")}
	if _, err := x.Version(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if _, err := (&Exec{}).Version(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestExecAuthAndLogin(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	f := r.folder(".claude-2")
	st, err := r.x.AuthStatus(ctx, f)
	if err != nil || st.LoggedIn {
		t.Fatalf("logged out: %+v %v", st, err)
	}
	var out bytes.Buffer
	err = r.x.AuthLogin(ctx, f, Stdio{In: strings.NewReader("me@work.example:Team\n"), Out: &out, Err: &out})
	if err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "Paste code here if prompted > ") {
		t.Fatalf("terminal not handed over: %q", out.String())
	}
	st, err = r.x.AuthStatus(ctx, f)
	if err != nil || !st.LoggedIn || st.Email != "me@work.example" || st.Org != "Team" || st.Plan != "max" || st.Method != "claude.ai" || len(st.Raw) == 0 {
		t.Fatalf("%+v %v", st, err)
	}
	// a failed login is an *Error
	err = r.x.AuthLogin(ctx, f, Stdio{In: strings.NewReader("\n"), Out: &out, Err: &out})
	var ce *Error
	if !errors.As(err, &ce) || ce.ExitCode != 1 {
		t.Fatalf("got %v", err)
	}
}

func TestExecFolderEnvironment(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.x.Agents(ctx, Folder{Dir: filepath.Join(r.home, ".claude"), Unset: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.x.Agents(ctx, r.folder(".claude-2")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"unset":true`) || !strings.Contains(lines[0], `"config_dir":""`) {
		t.Fatalf("default folder must run with the variable removed: %v", lines)
	}
	if !strings.Contains(lines[1], `"config_dir":"`+filepath.ToSlash(r.folder(".claude-2").Dir)+`"`) &&
		!strings.Contains(lines[1], strings.ReplaceAll(r.folder(".claude-2").Dir, `\`, `\\`)) {
		t.Fatalf("folder not passed: %v", lines[1])
	}
}

func TestExecPluginFlow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	owner, linked := r.folder(".claude"), r.folder(".claude-2")
	mkt := r.marketplace()
	if err := os.MkdirAll(linked.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := r.x.PluginMarketplaceAdd(ctx, owner, mkt)
	if err != nil || !res.OK() || len(res.Raw) == 0 || !strings.Contains(res.Message, "sy-local") {
		t.Fatalf("%+v %v", res, err)
	}
	ms, err := r.x.PluginMarketplaceList(ctx, owner)
	if err != nil || len(ms) != 1 || ms[0].Name != "sy-local" || ms[0].Source != mkt {
		t.Fatalf("%+v %v", ms, err)
	}
	if res, err = r.x.PluginInstall(ctx, owner, "switchyard@sy-local"); err != nil || !res.OK() {
		t.Fatalf("%+v %v", res, err)
	}
	ps, err := r.x.PluginList(ctx, owner)
	if err != nil || len(ps) != 1 || ps[0].ID != "switchyard@sy-local" || !ps[0].Enabled || ps[0].Version != "1.2.3" || ps[0].Scope != "user" {
		t.Fatalf("%+v %v", ps, err)
	}
	if _, err = r.x.PluginDisable(ctx, owner, "switchyard@sy-local"); err != nil {
		t.Fatal(err)
	}
	if ps, _ = r.x.PluginList(ctx, owner); ps[0].Enabled {
		t.Fatal("still enabled")
	}
	if _, err = r.x.PluginEnable(ctx, owner, "switchyard@sy-local"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.x.PluginMarketplaceUpdate(ctx, owner, "sy-local"); err != nil {
		t.Fatal(err)
	}
	// a failed command is an *Error with the JSON message
	_, err = r.x.PluginInstall(ctx, owner, "nope@sy-local")
	var ce *Error
	if !errors.As(err, &ce) || ce.ExitCode != 1 || ce.Outcome != "failed" || ce.FailureCode != "not_found" || !strings.Contains(ce.Message, "not found") {
		t.Fatalf("got %#v", err)
	}
	if _, err = r.x.PluginUninstall(ctx, owner, "switchyard@sy-local"); err != nil {
		t.Fatal(err)
	}
	if ps, _ = r.x.PluginList(ctx, owner); len(ps) != 0 {
		t.Fatal(ps)
	}
	if _, err = r.x.PluginMarketplaceRemove(ctx, owner, "sy-local"); err != nil {
		t.Fatal(err)
	}
}

func TestExecAgents(t *testing.T) {
	r := newRig(t)
	r.x.Environ = append(r.x.Environ, `FAKE_AGENTS=[{"id":"s1","status":"idle","extra":1}]`)
	as, err := r.x.Agents(context.Background(), r.folder(".claude"))
	if err != nil || len(as) != 1 || as[0].ID != "s1" || as[0].Status != "idle" || !strings.Contains(string(as[0].Raw), "extra") {
		t.Fatalf("%+v %v", as, err)
	}
}

func TestExecTimeout(t *testing.T) {
	r := newRig(t)
	r.x.Environ = append(r.x.Environ, "FAKE_CLAUDE_SLOW=5000")
	r.x.Timeout = 200 * time.Millisecond
	start := time.Now()
	_, err := r.x.PluginList(context.Background(), r.folder(".claude"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("timeout not enforced")
	}
}

func TestExecStdinIsClosed(t *testing.T) {
	r := newRig(t)
	// auth login would read stdin; through run() (stdin nil) it must see EOF at once and fail
	o, err := r.x.run(context.Background(), &Folder{Dir: filepath.Join(r.home, ".claude-9")}, "auth", "login", "--claudeai")
	if err != nil || o.exit != 1 {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestCheckFeatures(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.x.CheckFeatures(ctx, r.folder(".claude")); err != nil {
		t.Fatal(err)
	}
	r.x.Environ = append(r.x.Environ, "FAKE_CLAUDE_FAIL=marketplace")
	err := r.x.CheckFeatures(ctx, r.folder(".claude"))
	var fe *FeatureError
	if !errors.As(err, &fe) || len(fe.Missing) != 1 {
		t.Fatalf("got %v", err)
	}
	r.x.Environ = append(r.x.Environ, "FAKE_CLAUDE_FAIL=marketplace,auth status")
	if err = r.x.CheckFeatures(ctx, r.folder(".claude")); !errors.As(err, &fe) || len(fe.Missing) != 2 {
		t.Fatalf("got %v", err)
	}
}

func TestJSONLine(t *testing.T) {
	cases := map[string]bool{
		"":                                   false,
		"warning: x\n{\"outcome\":\"ok\"}\n": true,
		"{\n \"a\": 1\n}\n":                  true,
		"not json":                           false,
		"[1,2]":                              true,
	}
	for in, want := range cases {
		if _, ok := jsonLine([]byte(in)); ok != want {
			t.Errorf("jsonLine(%q) = %v", in, ok)
		}
	}
	res, err := result([]string{"plugin", "install"}, output{stdout: []byte("{\"outcome\":\"error\",\"message\":\"boom\"}"), exit: 0})
	var ce *Error
	if !errors.As(err, &ce) || ce.Message != "boom" || res.OK() {
		t.Fatalf("outcome error with exit 0 must fail: %+v %v", res, err)
	}
}

func TestLocate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	if _, err := Locate(home); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := Locate(home)
	if err != nil || p != filepath.Join(bin, name) {
		t.Fatalf("%q %v", p, err)
	}
}

// TestResultParsing feeds recorded Claude Code 2.1.296 outcome lines to result().
func TestResultParsing(t *testing.T) {
	const (
		okInstall = `{"command":"install","outcome":"ok","plugin":"switchyard@sy-local","pluginId":"switchyard@sy-local","scope":"user","message":"Successfully installed plugin: switchyard@sy-local (scope: user)"}`
		okMarket  = `{"command":"marketplace-add","outcome":"ok","marketplace":"sy-local","message":"Successfully added marketplace: sy-local (declared in user settings)"}`
		alEnable  = `{"command":"enable","outcome":"failed","plugin":"switchyard@sy-local","message":"Plugin \"switchyard@sy-local\" is already enabled","failureCode":"already_in_goal_state","alreadyInGoalState":true}`
		alDisable = `{"command":"disable","outcome":"failed","plugin":"switchyard@sy-local","message":"Plugin \"switchyard@sy-local\" is already disabled","failureCode":"already_in_goal_state","alreadyInGoalState":true}`
		notInst   = `{"command":"uninstall","outcome":"failed","plugin":"switchyard@sy-local","scope":"user","message":"Plugin \"switchyard@sy-local\" not found in installed plugins","failureCode":"not_installed"}`
		notFound  = `{"command":"install","outcome":"failed","plugin":"x@sy-local","scope":"user","message":"Plugin \"x\" not found in marketplace \"sy-local\".","failureCode":"not_found"}`
		notConf   = `{"command":"marketplace-remove","marketplace":"sy-local","outcome":"failed","message":"Marketplace 'sy-local' not found","failureCode":"not_configured"}`
	)
	cases := []struct {
		name        string
		stdout      string
		stderr      string
		exit        int
		wantErr     bool
		wantAlready bool
		wantCode    string
	}{
		{"install ok", okInstall, "", 0, false, false, ""},
		{"marketplace add ok", okMarket, "", 0, false, false, ""},
		{"enable already enabled", alEnable, "x Failed to enable plugin", 1, false, true, "already_in_goal_state"},
		{"disable already disabled", alDisable, "", 1, false, true, "already_in_goal_state"},
		{"flag without code", `{"outcome":"failed","alreadyInGoalState":true}`, "", 1, false, true, ""},
		{"code without flag", `{"outcome":"failed","failureCode":"already_in_goal_state"}`, "", 1, false, true, "already_in_goal_state"},
		{"uninstall not installed stays an error here", notInst, "", 1, true, false, "not_installed"},
		{"install not found", notFound, "", 1, true, false, "not_found"},
		{"marketplace remove not configured stays an error here", notConf, "", 1, true, false, "not_configured"},
		{"no json, exit 1", "", "boom", 1, true, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := result([]string{"plugin", "enable", "x"}, output{stdout: []byte(c.stdout), stderr: []byte(c.stderr), exit: c.exit})
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v", err)
			}
			if res.AlreadyInGoalState != c.wantAlready || res.FailureCode != c.wantCode {
				t.Fatalf("%+v", res)
			}
			if !c.wantErr && !res.OK() {
				t.Fatalf("not OK: %+v", res)
			}
			var ce *Error
			if c.wantErr && c.wantCode != "" && (!errors.As(err, &ce) || ce.FailureCode != c.wantCode) {
				t.Fatalf("error lacks failure code: %#v", err)
			}
		})
	}
}

// TestExecIdempotentPluginCommands runs every plugin command twice against the fake claude:
// the second answer is "already done" and must come back as a nil error.
func TestExecIdempotentPluginCommands(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	f := r.folder(".claude")
	const id = "switchyard@sy-local"
	mkt := r.marketplace()
	for i := 0; i < 2; i++ {
		if res, err := r.x.PluginMarketplaceAdd(ctx, f, mkt); err != nil || !res.OK() {
			t.Fatalf("marketplace add #%d: %+v %v", i, res, err)
		}
		if res, err := r.x.PluginInstall(ctx, f, id); err != nil || !res.OK() {
			t.Fatalf("install #%d: %+v %v", i, res, err)
		}
		if res, err := r.x.PluginEnable(ctx, f, id); err != nil || !res.OK() || !res.AlreadyInGoalState {
			t.Fatalf("enable #%d: %+v %v", i, res, err)
		}
	}
	// (enable above is always "already enabled": install enabled it)
	for i := 0; i < 2; i++ {
		res, err := r.x.PluginDisable(ctx, f, id)
		if err != nil || !res.OK() || res.AlreadyInGoalState != (i == 1) {
			t.Fatalf("disable #%d: %+v %v", i, res, err)
		}
	}
	for i := 0; i < 2; i++ {
		res, err := r.x.PluginUninstall(ctx, f, id)
		if err != nil || !res.OK() || res.AlreadyInGoalState != (i == 1) {
			t.Fatalf("uninstall #%d: %+v %v", i, res, err)
		}
	}
	for i := 0; i < 2; i++ {
		res, err := r.x.PluginMarketplaceRemove(ctx, f, "sy-local")
		if err != nil || !res.OK() || res.AlreadyInGoalState != (i == 1) {
			t.Fatalf("marketplace remove #%d: %+v %v", i, res, err)
		}
	}
	// a real failure is still an error, with the failure code
	_, err := r.x.PluginInstall(ctx, f, "nope@sy-local")
	var ce *Error
	if !errors.As(err, &ce) || ce.FailureCode != "not_found" {
		t.Fatalf("got %#v", err)
	}
}
