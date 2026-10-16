package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/machineadd"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/service"
	"github.com/mraj602/switchyard/internal/setup/model"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var machineIDRE = regexp.MustCompile(`m_[0-9A-Z]{26}`)

// golden compares v, as indented JSON with the random machine id replaced, to testdata/<name>.
func golden(t *testing.T, name string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := machineIDRE.ReplaceAllString(string(b), "m_MACHINE") + "\n"
	p := filepath.Join("testdata", name)
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/desktop -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

type fakeAdmin struct {
	calls [][]string
	stdin [][]byte
	out   string
	err   error
	name  string
}

func (f *fakeAdmin) Hub(_ context.Context, args []string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, args)
	f.stdin = append(f.stdin, stdin)
	return []byte(f.out), f.err
}
func (f *fakeAdmin) Describe() string { return f.name }

type fakeCtl struct {
	st      model.ServiceStatus
	started bool
	stopped bool
	err     error
}

func (f *fakeCtl) Status(context.Context, string) (model.ServiceStatus, error) { return f.st, nil }
func (f *fakeCtl) Start(context.Context, string) error {
	if f.err != nil {
		return f.err
	}
	f.started, f.st.Active = true, true
	return nil
}
func (f *fakeCtl) Stop(context.Context, string) error {
	if f.err != nil {
		return f.err
	}
	f.stopped, f.st.Active = true, false
	return nil
}

// sandboxEnv is an Env on temp folders with nothing connected.
func sandboxEnv(t *testing.T) Env {
	t.Helper()
	root := t.TempDir()
	d := paths.Dirs{}
	d.Config.Path, d.State.Path, d.Data.Path = filepath.Join(root, "config"), filepath.Join(root, "state"), filepath.Join(root, "data")
	for _, p := range []string{d.Config.Path, d.State.Path, d.Data.Path} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	machine := `{"id":"m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K","name":"laptop","created":"2026-10-13T09:00:00.000Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(d.State.Path, "machine.json"), []byte(machine), 0o600); err != nil {
		t.Fatal(err)
	}
	return Env{Dirs: d, Version: "0.6.0", GOOS: "linux", Sleep: func(time.Duration) {}}
}

func connect(t *testing.T, e *Env, url string) {
	t.Helper()
	e.Config = config.Defaults()
	e.Config.Hub.URL = url
	if err := os.WriteFile(filepath.Join(e.Dirs.State.Path, "token"), []byte("sy_m_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func healthHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"ok":true,"version":"0.6.0","schema":5,"time":"2026-10-13T10:00:00Z"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInfoNotConnectedIsOK(t *testing.T) {
	e := sandboxEnv(t)
	e.Config = config.Defaults()
	e.Service = &fakeCtl{}
	info, err := RunInfo(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if info.Hub.Connected || info.Hub.URL != nil || info.Hub.Reachable || info.Hub.Service != nil || info.Admin != nil {
		t.Fatalf("%+v", info)
	}
	golden(t, "info_not_connected.golden", info)
}

func TestInfoConnectedLocalHub(t *testing.T) {
	e := sandboxEnv(t)
	srv := healthHub(t)
	connect(t, &e, srv.URL) // http://127.0.0.1:port
	if err := os.WriteFile(filepath.Join(e.Dirs.Data.Path, "hub.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.Service = &fakeCtl{st: model.ServiceStatus{Installed: true, Active: true}}
	info, err := RunInfo(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Hub.Connected || !info.Hub.Reachable || !info.Hub.Local || info.Hub.Service == nil || *info.Hub.Service != ServiceRunning ||
		info.Admin == nil || *info.Admin != AdminLocal || info.Hub.Version == nil || *info.Hub.Version != "0.6.0" {
		t.Fatalf("%+v", info)
	}
	// The port differs per run; the shape is what the golden pins.
	info.Hub.URL = strp("http://127.0.0.1:8787")
	golden(t, "info_local.golden", info)
}

// A hub.db left on the laptop after the hub moved is not the admin channel: no "local".
func TestInfoRemoteHubStaleLocalDB(t *testing.T) {
	e := sandboxEnv(t)
	connect(t, &e, "https://hub.example.ts.net")
	if err := os.WriteFile(filepath.Join(e.Dirs.Data.Path, "hub.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.Service = &fakeCtl{st: model.ServiceStatus{Installed: true}}
	e.HTTP = func(time.Duration) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("down") })}, nil
	}
	info, err := RunInfo(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if info.Hub.Local || info.Admin != nil {
		t.Fatalf("stale hub.db taken as the hub: local=%v admin=%v", info.Hub.Local, info.Admin)
	}
}

func TestHubHere(t *testing.T) {
	for raw, want := range map[string]bool{"": true, "http://127.0.0.1:8787": true, "http://localhost:9": true, "http://[::1]:8787": true,
		"https://hub.example.ts.net": false, "http://100.64.0.8:8787": false} {
		if got := HubHere(raw); got != want {
			t.Errorf("HubHere(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestInfoRemoteHubAdminPrefixAndDial(t *testing.T) {
	e := sandboxEnv(t)
	connect(t, &e, "https://hub.example.ts.net")
	e.Config.Hub.Dial = "127.0.0.1:47830"
	e.Config.Machines.HubAdmin = "ssh oci sy"
	e.AdminName = "ssh oci sy"
	e.HTTP = func(time.Duration) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("down") })}, nil
	}
	e.Service = &fakeCtl{}
	info, err := RunInfo(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if info.Hub.Reachable || info.Hub.Local || info.Hub.Service != nil || info.Hub.Dial == nil || *info.Hub.Dial != "127.0.0.1:47830" ||
		info.Admin == nil || *info.Admin != "ssh oci sy" {
		t.Fatalf("%+v", info)
	}
	// forward mode: the app is refused token and owner-link.
	if _, err := RunToken(context.Background(), e, TokenOptions{}); codeOf(err) != ExitNotHere {
		t.Fatalf("token with hub.dial: %v", err)
	}
	if _, err := RunOwnerLink(context.Background(), e); codeOf(err) != ExitNotHere {
		t.Fatalf("owner-link with hub.dial: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func codeOf(err error) int {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	if err == nil {
		return 0
	}
	return -1
}

func TestTokenThroughAdminChannel(t *testing.T) {
	e := sandboxEnv(t)
	connect(t, &e, "https://hub.example.ts.net")
	a := &fakeAdmin{out: `{"device":"dev_01JA","token":"sy_d_` + strings.Repeat("A", 43) + `","scopes":["write","read"]}` + "\n", name: "ssh oci sy"}
	e.Admin = a
	tok, err := RunToken(context.Background(), e, TokenOptions{Name: "my-laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.calls) != 1 {
		t.Fatalf("%d admin calls", len(a.calls))
	}
	args := a.calls[0]
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "token create --kind desktop --machine m_") || !strings.Contains(joined, "--name my-laptop") || !strings.HasSuffix(joined, "--json") {
		t.Fatalf("admin args: %q", args)
	}
	if strings.Contains(joined, tok.Token) {
		t.Fatal("the token is in argv")
	}
	tok.Token = "sy_d_TOKEN"
	golden(t, "token.golden", tok)
}

func TestTokenNeedsAdminAndConnection(t *testing.T) {
	e := sandboxEnv(t)
	e.Config = config.Defaults()
	if _, err := RunToken(context.Background(), e, TokenOptions{}); codeOf(err) != ExitNotHere {
		t.Fatalf("not connected: %v", err)
	}
	connect(t, &e, "https://hub.example.ts.net")
	if _, err := RunToken(context.Background(), e, TokenOptions{}); codeOf(err) != ExitNotHere {
		t.Fatalf("no admin channel: %v", err)
	}
	for _, bad := range []string{"bad/name", "Switchyard desktop laptop"} {
		if _, err := RunToken(context.Background(), e, TokenOptions{Name: bad}); codeOf(err) != ExitUsage {
			t.Fatalf("bad name %q: %v", bad, err)
		}
	}
	e.Admin = &fakeAdmin{err: &machineadd.AdminError{Args: []string{"token"}, Exit: 255, Stderr: "ssh: connect to host oci: Connection refused"}}
	if _, err := RunToken(context.Background(), e, TokenOptions{}); codeOf(err) != ExitUnreachable {
		t.Fatalf("ssh failure: %v", err)
	}
	e.Admin = &fakeAdmin{err: &machineadd.AdminError{Args: []string{"token"}, Exit: 3, Stderr: "not implemented yet"}}
	if _, err := RunToken(context.Background(), e, TokenOptions{}); codeOf(err) != ExitNotHere {
		t.Fatalf("old hub: %v", err)
	}
}

func TestTokenCodeStdinClaims(t *testing.T) {
	var body map[string]any
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/pair/claim" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"nope","code":"conflict"}`))
			return
		}
		_, _ = w.Write([]byte(`{"device":"dev_01JA","kind":"desktop","name":"x","scopes":["write","read"],"token":"sy_d_` +
			strings.Repeat("B", 43) + `","machine":"m_x","hub":{"url":"https://hub.example.ts.net","version":"0.6.0"}}`))
	}))
	t.Cleanup(srv.Close)

	for _, c := range []struct{ in, field string }{{"482-913\n", "code"}, {"Q2xhdWRlIGlzIG5vdCBoZX\nignored\n", "secret"}} {
		e := sandboxEnv(t)
		connect(t, &e, srv.URL)
		e.Stdin = strings.NewReader(c.in)
		tok, err := RunToken(context.Background(), e, TokenOptions{CodeStdin: true, Name: "laptop"})
		if err != nil {
			t.Fatal(err)
		}
		if body["expect_kind"] != "desktop" || body[c.field] != strings.SplitN(c.in, "\n", 2)[0] || !strings.HasPrefix(body["machine"].(string), "m_") {
			t.Fatalf("claim body %v", body)
		}
		if tok.Device != "dev_01JA" || !strings.HasPrefix(tok.Token, "sy_d_") || tok.Hub.URL != srv.URL || len(tok.Scopes) != 2 {
			t.Fatalf("%+v", tok)
		}
	}
	for st, want := range map[int]int{409: ExitRefused, 401: ExitRefused, 429: ExitRefused, 400: ExitRefused, 503: ExitUnreachable} {
		status = st
		e := sandboxEnv(t)
		connect(t, &e, srv.URL)
		e.Stdin = strings.NewReader("482913\n")
		if _, err := RunToken(context.Background(), e, TokenOptions{CodeStdin: true}); codeOf(err) != want {
			t.Errorf("claim %d: exit %d (%v), want %d", st, codeOf(err), err, want)
		}
	}
	// No hub at all: unreachable; an empty stdin: usage.
	e := sandboxEnv(t)
	connect(t, &e, "http://127.0.0.1:1")
	e.Stdin = strings.NewReader("482913\n")
	if _, err := RunToken(context.Background(), e, TokenOptions{CodeStdin: true}); codeOf(err) != ExitUnreachable {
		t.Errorf("hub down: %v", err)
	}
	e.Stdin = strings.NewReader("\n")
	if _, err := RunToken(context.Background(), e, TokenOptions{CodeStdin: true}); codeOf(err) != ExitUsage {
		t.Errorf("empty stdin: %v", err)
	}
}

func TestOwnerLink(t *testing.T) {
	e := sandboxEnv(t)
	connect(t, &e, "https://hub.example.ts.net")
	if _, err := RunOwnerLink(context.Background(), e); codeOf(err) != ExitNotHere {
		t.Fatalf("no admin: %v", err)
	}
	a := &fakeAdmin{out: `{"id":"pc_1","url":"https://hub.example.ts.net/#/owner?s=SECRET","secret":"SECRET","expires_at":"2026-10-13T10:10:00.000Z"}` + "\n"}
	e.Admin = a
	l, err := RunOwnerLink(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.calls[0], " "); got != "owner-link --url https://hub.example.ts.net --json" {
		t.Fatalf("admin args %q", got)
	}
	b, _ := json.Marshal(l)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), `"id"`) {
		t.Fatalf("extra fields leak: %s", b)
	}
	golden(t, "owner_link.golden", l)

	// A plain http LAN hub needs --insecure only when sy itself was told so.
	connect(t, &e, "http://192.168.1.5:8787")
	a.calls = nil
	_, _ = RunOwnerLink(context.Background(), e)
	if strings.Contains(strings.Join(a.calls[0], " "), "--insecure") {
		t.Fatal("--insecure without insecure_lan")
	}
	e.Config.Hub.InsecureLAN = true
	_, _ = RunOwnerLink(context.Background(), e)
	if !strings.HasSuffix(strings.Join(a.calls[1], " "), "--insecure") && !strings.Contains(strings.Join(a.calls[1], " "), "--insecure") {
		t.Fatal("--insecure missing")
	}
}

func TestHubStatusStartStop(t *testing.T) {
	srv := healthHub(t)
	e := sandboxEnv(t)
	connect(t, &e, srv.URL)
	ctl := &fakeCtl{st: model.ServiceStatus{Installed: true}}
	e.Service = ctl
	ctx := context.Background()

	st, err := RunHub(ctx, e, ActionStatus)
	if err != nil || st.Service != ServiceStopped || st.Unit != "switchyard-hub.service" {
		t.Fatalf("%+v %v", st, err)
	}
	golden(t, "hub_stopped.golden", st)
	if st, err = RunHub(ctx, e, ActionStart); err != nil || st.Service != ServiceRunning || !ctl.started {
		t.Fatalf("start: %+v %v", st, err)
	}
	golden(t, "hub_running.golden", st)
	if st, err = RunHub(ctx, e, ActionStop); err != nil || st.Service != ServiceStopped || !ctl.stopped {
		t.Fatalf("stop: %+v %v", st, err)
	}

	// Not installed: status says missing (exit 0), start and stop are exit 4.
	e.Service = &fakeCtl{err: service.ErrNotInstalled}
	st, err = RunHub(ctx, e, ActionStatus)
	if err != nil || st.Service != ServiceMissing {
		t.Fatalf("missing: %+v %v", st, err)
	}
	golden(t, "hub_missing.golden", st)
	for _, a := range []string{ActionStart, ActionStop} {
		if _, err := RunHub(ctx, e, a); codeOf(err) != ExitNotHere {
			t.Fatalf("%s not installed: %v", a, err)
		}
	}
	if _, err := RunHub(ctx, e, "restart"); codeOf(err) != ExitUsage {
		t.Fatalf("bad action: %v", err)
	}
}

func TestBatchMode(t *testing.T) {
	for in, want := range map[string]string{
		"ssh oci sy":                  "ssh -o BatchMode=yes oci sy",
		"/usr/bin/ssh -p 2222 oci sy": "/usr/bin/ssh -o BatchMode=yes -p 2222 oci sy",
		"ssh -o BatchMode=yes oci sy": "ssh -o BatchMode=yes oci sy",
		"docker exec -T hub sy":       "docker exec -T hub sy",
	} {
		p, err := machineadd.NewPrefixAdmin(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(BatchMode(p).Words, " "); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestInfoJSONHasNoSecrets(t *testing.T) {
	e := sandboxEnv(t)
	connect(t, &e, "https://hub.example.ts.net")
	e.Service = &fakeCtl{}
	info, _ := RunInfo(context.Background(), e)
	b, _ := json.Marshal(info)
	if bytes.Contains(b, []byte("sy_m_")) {
		t.Fatalf("token in info: %s", b)
	}
}

// fakeSSH puts an `ssh` shell script first on PATH that records its argv and answers the
// hub's token and owner-link commands; it returns the argv record file.
func fakeSSH(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a shell script")
	}
	bin := t.TempDir()
	rec := filepath.Join(bin, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + rec + "\ncase \"$*\" in\n" +
		"  *token*create*) echo '{\"device\":\"dev_1\",\"token\":\"sy_d_" + strings.Repeat("A", 43) + "\",\"scopes\":[\"write\",\"read\"]}' ;;\n" +
		"  *) echo 'unexpected' >&2; exit 9 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return rec
}

// The app's pairing through an ssh admin channel: the real PrefixAdmin (in BatchMode, as
// osDesktopEnv makes it) with a fake ssh. The device name the app sends must pass the remote
// word check; a name with spaces used to fail there on every pairing.
func TestTokenThroughSSHPrefixAdmin(t *testing.T) {
	rec := fakeSSH(t)
	e := sandboxEnv(t)
	connect(t, &e, "https://hub.example.ts.net")
	p, err := machineadd.NewPrefixAdmin("ssh oci sy")
	if err != nil {
		t.Fatal(err)
	}
	e.Admin, e.AdminName = BatchMode(p), "ssh oci sy"
	tok, err := RunToken(context.Background(), e, TokenOptions{Name: "Switchyard-desktop-laptop"})
	if err != nil {
		t.Fatalf("pairing through ssh: %v", err)
	}
	if tok.Device != "dev_1" || !strings.HasPrefix(tok.Token, "sy_d_") {
		t.Fatalf("%+v", tok)
	}
	argv, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(argv), "-o BatchMode=yes oci sy 'hub' 'token' 'create'") || !strings.Contains(string(argv), "'--name' 'Switchyard-desktop-laptop'") {
		t.Fatalf("ssh argv %q", argv)
	}
}

// A failure that is not the admin command's own exit (here: the channel refuses an argument
// word) is exit 1 with its real message, not exit 4 "no admin channel".
func TestAdminFailureNotAdminErrorIsExit1(t *testing.T) {
	err := adminErr(errors.New(`"a b" cannot be a remote command word`))
	if codeOf(err) != 1 || !strings.Contains(err.Error(), "remote command word") {
		t.Fatalf("%v (code %d)", err, codeOf(err))
	}
	// The real PrefixAdmin with a program that does not exist: exit 1, the exec error.
	e := sandboxEnv(t)
	connect(t, &e, "https://hub.example.ts.net")
	p, perr := machineadd.NewPrefixAdmin(filepath.Join(t.TempDir(), "no-such-ssh") + " oci sy")
	if perr != nil {
		t.Fatal(perr)
	}
	e.Admin = p
	_, err = RunToken(context.Background(), e, TokenOptions{Name: "Switchyard-desktop-laptop"})
	if codeOf(err) != 1 || strings.Contains(err.Error(), "no admin channel") {
		t.Fatalf("missing program: %v (code %d)", err, codeOf(err))
	}
}

// A bad machines.hub_admin is reported by info; only token and owner-link need the channel.
func TestBadHubAdminOnlyBreaksTheActionsThatNeedIt(t *testing.T) {
	e := sandboxEnv(t)
	connect(t, &e, "https://hub.example.ts.net")
	e.Config.Machines.HubAdmin = "ssh -o StrictHostKeyChecking=no oci sy"
	e.AdminName = e.Config.Machines.HubAdmin
	_, e.AdminErr = machineadd.NewPrefixAdmin(e.Config.Machines.HubAdmin)
	if e.AdminErr == nil {
		t.Fatal("the prefix should be refused")
	}
	e.HTTP = func(time.Duration) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("down") })}, nil
	}
	e.Service = &fakeCtl{}
	info, err := RunInfo(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if info.AdminError == nil || *info.AdminError == "" || info.Admin == nil || !info.Hub.Connected {
		t.Fatalf("%+v", info)
	}
	if _, err := RunToken(context.Background(), e, TokenOptions{}); codeOf(err) != ExitNotHere || !strings.Contains(err.Error(), "machines.hub_admin cannot be used") {
		t.Fatalf("token: %v", err)
	}
	if _, err := RunOwnerLink(context.Background(), e); codeOf(err) != ExitNotHere || !strings.Contains(err.Error(), "machines.hub_admin cannot be used") {
		t.Fatalf("owner-link: %v", err)
	}
	if _, err := RunHub(context.Background(), e, ActionStatus); err != nil {
		t.Fatalf("hub status: %v", err)
	}
}

// A hub that drops the first /health (a slow tiny VM) is still reachable: info tries twice, with
// the 8 s timeout rather than 3 s.
func TestInfoHealthRetriesOnceWithLongTimeout(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"version":"0.6.0","schema":5,"time":"2026-10-13T10:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	e := sandboxEnv(t)
	connect(t, &e, srv.URL)
	var asked []time.Duration
	e.HTTP = func(d time.Duration) (*http.Client, error) { asked = append(asked, d); return srv.Client(), nil }
	info, err := RunInfo(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Hub.Reachable || hits.Load() != 2 {
		t.Fatalf("reachable=%v hits=%d", info.Hub.Reachable, hits.Load())
	}
	for _, d := range asked {
		if d != 8*time.Second {
			t.Fatalf("health timeout %v, want 8s", d)
		}
	}
}

// Two failures in a row are unreachable, and it is exactly two tries.
func TestInfoHealthGivesUpAfterTwoTries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	e := sandboxEnv(t)
	connect(t, &e, srv.URL)
	info, err := RunInfo(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if info.Hub.Reachable || hits.Load() != 2 {
		t.Fatalf("reachable=%v hits=%d", info.Hub.Reachable, hits.Load())
	}
}
