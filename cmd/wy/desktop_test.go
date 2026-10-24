package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/desktop"
	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/service"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// deskSandbox points every folder at a temp WY_HOME (never the real home or
// CLAUDE_CONFIG_DIR) and returns its folders.
func deskSandbox(t *testing.T) paths.Dirs {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WY_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("WY_CONFIG_DIR", "")
	t.Setenv("WY_STATE_DIR", "")
	t.Setenv("WY_DATA_DIR", "")
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.State.Path, home) {
		t.Fatalf("state %s is outside the sandbox %s", d.State.Path, home)
	}
	return d
}

type fakeService struct {
	st      model.ServiceStatus
	started bool
}

func (f *fakeService) Status(context.Context, string) (model.ServiceStatus, error) { return f.st, nil }
func (f *fakeService) Start(context.Context, string) error {
	if !f.st.Installed {
		return service.ErrNotInstalled
	}
	f.started, f.st.Active = true, true
	return nil
}
func (f *fakeService) Stop(context.Context, string) error { return nil }

// noRealService keeps every test off the real service manager.
func noRealService(t *testing.T, fake *fakeService) {
	t.Helper()
	desktopEnvHook = func(e *desktop.Env) { e.Service = fake }
	t.Cleanup(func() { desktopEnvHook = nil })
}

func runDesk(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	old := desktopStdin
	desktopStdin = strings.NewReader(stdin)
	defer func() { desktopStdin = old }()
	var out, errb bytes.Buffer
	code := dispatch(append([]string{"desktop"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func connectSandbox(t *testing.T, d paths.Dirs, url string, kv ...string) {
	t.Helper()
	if err := os.MkdirAll(d.State.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(d.Config.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Set("hub.url", []string{url}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if err := f.Set(kv[i], []string{kv[i+1]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(filepath.Join(d.State.Path, client.TokenFile), []byte("sy_m_"+strings.Repeat("x", 43)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopInfoUnconnectedExits0(t *testing.T) {
	deskSandbox(t)
	noRealService(t, &fakeService{})
	code, out, errb := runDesk(t, "", "info", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	var info desktop.Info
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatal(err, out)
	}
	if info.V != 1 || info.Hub.Connected || info.Hub.URL != nil || info.Admin != nil || !strings.HasPrefix(info.Machine.ID, "m_") {
		t.Fatalf("%+v", info)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("--json is one line: %q", out)
	}
}

func TestDesktopUsage(t *testing.T) {
	deskSandbox(t)
	for _, args := range [][]string{{}, {"nope"}, {"hub"}, {"hub", "restart"}, {"info", "extra"}, {"token", "--bogus"}} {
		if code, _, _ := runDesk(t, "", args...); code != 2 {
			t.Errorf("wy desktop %v: exit %d, want 2", args, code)
		}
	}
	if code, _, _ := runDesk(t, "", "token", "--name", "bad/name", "--json"); code != 2 {
		// not connected is checked after the name, so a bad name is usage
		t.Errorf("bad name: exit %d, want 2", code)
	}
}

func TestDesktopHubServiceThroughFake(t *testing.T) {
	d := deskSandbox(t)
	fake := &fakeService{st: model.ServiceStatus{Installed: true}}
	noRealService(t, fake)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"version":"0.6.0","schema":5,"time":"x"}`))
	}))
	t.Cleanup(hub.Close)
	connectSandbox(t, d, hub.URL)
	code, out, errb := runDesk(t, "", "hub", "start", "--json")
	if code != 0 || !fake.started {
		t.Fatalf("exit %d %s", code, errb)
	}
	var st desktop.HubService
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.Service != "running" || st.Unit == "" || !st.Health {
		t.Fatalf("%s %v", out, err)
	}
	fake.st.Installed = false
	if code, _, _ := runDesk(t, "", "hub", "start", "--json"); code != 4 {
		t.Errorf("start, not installed: exit %d, want 4", code)
	}
	if code, out, _ := runDesk(t, "", "hub", "status", "--json"); code != 0 || !strings.Contains(out, `"missing"`) {
		t.Errorf("status, not installed: exit %d %s", code, out)
	}
}

// startLocalHub serves the hub database of the sandbox over HTTP: the admin channel (`local`)
// and the HTTP side see the same file, as on a machine that runs `wy hub serve`.
func startLocalHub(t *testing.T, d paths.Dirs) *httptest.Server {
	t.Helper()
	if err := os.MkdirAll(d.Data.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(d.Data.Path, "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(api.Config{Store: st, PingEvery: time.Hour})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close(); st.Close() })
	return ts
}

func meScopes(t *testing.T, url, token string) []string {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var me struct {
		Device struct {
			Kind   string   `json:"kind"`
			Scopes []string `json:"scopes"`
		} `json:"device"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&me) != nil || me.Device.Kind != "desktop" {
		t.Fatalf("/v1/me: %d %+v", resp.StatusCode, me)
	}
	return me.Device.Scopes
}

// token through a local hub (admin channel `local`) returns a working sy_d_ token; --code-stdin
// claims a desktop code and refuses a phone code with exit 3; info sees a local hub.
func TestDesktopTokenThroughLocalHub(t *testing.T) {
	d := deskSandbox(t)
	noRealService(t, &fakeService{st: model.ServiceStatus{Installed: true, Active: true}})
	hub := startLocalHub(t, d)
	connectSandbox(t, d, hub.URL)

	code, out, errb := runDesk(t, "", "info", "--json")
	var info desktop.Info
	if code != 0 || json.Unmarshal([]byte(out), &info) != nil || !info.Hub.Local || !info.Hub.Reachable || info.Admin == nil || *info.Admin != "local" ||
		info.Hub.Service == nil || *info.Hub.Service != "running" {
		t.Fatalf("info: %d %s %s", code, out, errb)
	}

	code, out, errb = runDesk(t, "", "token", "--json", "--name", "desk-test")
	var tok desktop.Token
	if code != 0 || json.Unmarshal([]byte(out), &tok) != nil || !strings.HasPrefix(tok.Token, "sy_d_") || tok.Hub.URL != hub.URL {
		t.Fatalf("token: %d %s %s", code, out, errb)
	}
	if got := strings.Join(meScopes(t, hub.URL, tok.Token), ","); got != "write,read" {
		t.Fatalf("scopes %q", got)
	}

	pair := func(kind string) map[string]any {
		var o, e bytes.Buffer
		url := hub.URL
		if kind == "phone" {
			url = "https://hub.example.ts.net" // a phone code needs https; the claim still goes to hub.url
		}
		if c := dispatch([]string{"hub", "pair", "--kind", kind, "--url", url, "--json"}, &o, &e); c != 0 {
			t.Fatalf("hub pair --kind %s: %d %s", kind, c, e.String())
		}
		var m map[string]any
		if err := json.Unmarshal(o.Bytes(), &m); err != nil {
			t.Fatal(err, o.String())
		}
		return m
	}
	desk := pair("desktop")
	code, out, errb = runDesk(t, desk["code"].(string)+"\n", "token", "--code-stdin", "--json", "--name", "by-code")
	if code != 0 || json.Unmarshal([]byte(out), &tok) != nil || !strings.HasPrefix(tok.Token, "sy_d_") {
		t.Fatalf("token --code-stdin: %d %s %s", code, out, errb)
	}
	meScopes(t, hub.URL, tok.Token)
	// The same code works once.
	if code, _, _ := runDesk(t, desk["code"].(string)+"\n", "token", "--code-stdin", "--json"); code != 3 {
		t.Errorf("used code: exit %d, want 3", code)
	}
	phone := pair("phone")
	if code, out, errb := runDesk(t, phone["code"].(string)+"\n", "token", "--code-stdin", "--json"); code != 3 || out != "" {
		t.Errorf("phone code: exit %d (want 3), stdout %q stderr %s", code, out, errb)
	}

	// owner-link through the local admin channel.
	code, out, errb = runDesk(t, "", "owner-link", "--json")
	var link desktop.OwnerLink
	if code != 0 || json.Unmarshal([]byte(out), &link) != nil || !strings.Contains(link.URL, "#/owner?s=") || link.ExpiresAt == "" {
		t.Fatalf("owner-link: %d %s %s", code, out, errb)
	}
}

// A command-prefix admin channel is ssh in BatchMode, and no secret is ever in argv. A fake ssh
// records its arguments and answers like the hub's `wy hub ...`.
func TestDesktopPrefixAdminBatchModeNoSecretInArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh is a shell script")
	}
	d := deskSandbox(t)
	noRealService(t, &fakeService{})
	connectSandbox(t, d, "https://hub.example.ts.net", "machines.hub_admin", "ssh hub-host wy")
	bin := t.TempDir()
	rec := filepath.Join(bin, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + rec + "\ncase \"$*\" in\n" +
		"  *token*create*) echo '{\"device\":\"dev_1\",\"token\":\"sy_d_SECRETSECRETSECRETSECRETSECRETSECRETSECRET1\",\"scopes\":[\"write\",\"read\"]}' ;;\n" +
		"  *owner-link*) echo '{\"id\":\"pc_1\",\"url\":\"https://hub.example.ts.net/#/owner?s=ONETIMESECRET\",\"secret\":\"ONETIMESECRET\",\"expires_at\":\"2026-10-13T10:00:00.000Z\"}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The name the app sends (hub/index.js pair): it must pass the ssh word check.
	code, out, errb := runDesk(t, "", "token", "--json", "--name", "Watchyard-desktop-laptop")
	if code != 0 || !strings.Contains(out, "sy_d_SECRET") {
		t.Fatalf("token: %d %s %s", code, out, errb)
	}
	code, out, errb = runDesk(t, "", "owner-link", "--json")
	if code != 0 || !strings.Contains(out, "ONETIMESECRET") || strings.Contains(out, `"secret"`) {
		t.Fatalf("owner-link: %d %s %s", code, out, errb)
	}
	argv, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	if len(lines) != 2 {
		t.Fatalf("ssh ran %d times: %q", len(lines), argv)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "-o BatchMode=yes hub-host") {
			t.Errorf("ssh argv does not start with BatchMode: %q", l)
		}
	}
	if strings.Contains(string(argv), "SECRET") {
		t.Fatalf("a secret is in argv: %s", argv)
	}
}

// A machines.hub_admin prefix wy refuses does not break `info` or `hub status`: info reports
// it in admin_error, and only token and owner-link (which need the channel) exit 4 with it.
func TestDesktopBadHubAdminInfoStillWorks(t *testing.T) {
	d := deskSandbox(t)
	noRealService(t, &fakeService{})
	connectSandbox(t, d, "https://hub.example.ts.net", "machines.hub_admin", "ssh -o StrictHostKeyChecking=no hub-host wy")
	code, out, errb := runDesk(t, "", "info", "--json")
	var info desktop.Info
	if code != 0 || json.Unmarshal([]byte(out), &info) != nil {
		t.Fatalf("info: %d %s %s", code, out, errb)
	}
	if info.AdminError == nil || *info.AdminError == "" || !info.Hub.Connected {
		t.Fatalf("info: %s", out)
	}
	if code, _, errb := runDesk(t, "", "hub", "status", "--json"); code != 0 {
		t.Fatalf("hub status: %d %s", code, errb)
	}
	for _, args := range [][]string{{"token", "--json"}, {"owner-link", "--json"}} {
		if code, _, errb := runDesk(t, "", args...); code != 4 || !strings.Contains(errb, "machines.hub_admin") {
			t.Errorf("%v: exit %d %s", args, code, errb)
		}
	}
}
