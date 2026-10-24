package machineadd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/setup/testtree"
	"github.com/mraj602/watchyard/internal/sshx"
	"github.com/mraj602/watchyard/internal/sshx/sshxtest"
	"github.com/mraj602/watchyard/internal/update"
)

func TestMain(m *testing.M) {
	sshxtest.Main()
	// Never let a test see the person's real folders.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE_") || strings.HasPrefix(k, "WY_") || strings.HasPrefix(k, "XDG_") {
			os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}

const (
	testSecret = "S3cr3tPairingSecret22c" // 22 base64url characters
	testHost   = "me@gpu-box"
	testHubURL = "https://hub.example:8443"
	testMID    = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"
)

// fakeWy is the "wy" machine add copies over in tests: a shell script that answers the
// commands machine add runs and records what it got.
const fakeWy = `#!/bin/sh
case "$1" in
version) echo "${WY_FAKE_REMOTE_VERSION:-0.0.0-dev}";;
machine)
  case "$2" in
  probe) echo "$@" > "$HOME/probe.args"; cat "$HOME/probe.json";;
  authorize-pull) cat > "$HOME/authorize.stdin"; echo "$@" > "$HOME/authorize.args";;
  esac;;
setup) cat > "$HOME/setup.stdin"; echo "$@" > "$HOME/setup.args"; cat "$HOME/setup.json" 2>/dev/null || echo '{"mode":"new"}'; exit "$(cat "$HOME/setup.exit" 2>/dev/null || echo 0)";;
connect) echo "$@" >> "$HOME/connect.args"; exit "$(cat "$HOME/connect.exit" 2>/dev/null || echo 0)";;
direct-push) cat > "$HOME/dp.stdin"; echo "$@" > "$HOME/dp.args";;
keys)
  case "$2" in
  device) echo "$@" >> "$HOME/keysdevice.args"; cat "$HOME/keysdevice.json";;
  pin-ssh) cat > "$HOME/pin.stdin"; cat "$HOME/pin.json" 2>/dev/null || echo '{"hub":"ssh","owner":""}'; exit "$(cat "$HOME/pin.exit" 2>/dev/null || echo 0)";;
  esac;;
esac
`

type fakeAdmin struct {
	mu     sync.Mutex
	calls  [][]string
	stdins [][]byte
	answer func(args []string, stdin []byte) ([]byte, error)
}

func (f *fakeAdmin) Hub(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.stdins = append(f.stdins, stdin)
	f.mu.Unlock()
	return f.answer(args, stdin)
}

func (f *fakeAdmin) Describe() string { return "test admin" }

func (f *fakeAdmin) called(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return c
		}
	}
	return nil
}

func hubAnswers(now time.Time) func(args []string, stdin []byte) ([]byte, error) {
	return func(args []string, stdin []byte) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "pair --kind":
			return []byte(`{"id":"pc_1","code":"123456","secret":"` + testSecret + `","url":"` + testHubURL + `/#/pair?c=x","expires_at":"x"}` + "\n"), nil
		case "machine show":
			return []byte(`{"id":"` + testMID + `","name":"gpu-box","last_seen":"` + now.UTC().Format(time.RFC3339Nano) + `"}`), nil
		case "pull list":
			return []byte("[]\n"), nil
		case "pull add":
			return []byte(`{"target":"pt_01ABC","name":"gpu-box","public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub watchyard-pull@hub"}`), nil
		case "pull remove", "token revoke":
			return []byte("ok\n"), nil
		case "pull run":
			return []byte(`[{"id":"pt_01ABC","name":"gpu-box","target":"me@gpu-box:22","state":"ok"}]`), nil
		case "keys register":
			return fakeRegister(args, stdin)
		case "keys certify":
			return []byte("certified\n"), nil
		}
		return nil, &AdminError{Args: args, Exit: 3, Stderr: "not implemented"}
	}
}

type world struct {
	t      *testing.T
	fake   *sshxtest.Fake
	remote string // fake remote home
	home   string // operator home
	state  string // operator state
	bin    string // the wy file to copy
	admin  *fakeAdmin
	events []Event
	now    time.Time
}

func newWorld(t *testing.T) *world {
	w := &world{t: t, fake: sshxtest.New(t), home: t.TempDir(), state: t.TempDir(), now: time.Now()}
	w.remote = w.fake.RemoteHome(testHost)
	w.bin = filepath.Join(t.TempDir(), "wy")
	if err := os.WriteFile(w.bin, []byte(fakeWy), 0o755); err != nil {
		t.Fatal(err)
	}
	w.admin = &fakeAdmin{answer: hubAnswers(w.now)}
	w.probe(ProbeReport{V: 1, Machine: testMID, Name: "gpu-box", OS: "linux", Arch: "amd64", Free: []int{47830, 47831}, Restrict: true})
	keys := filepath.Join(w.remote, "etc-ssh")
	os.MkdirAll(keys, 0o755)
	os.WriteFile(filepath.Join(keys, "ssh_host_ed25519_key.pub"), []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHost root@gpu-box\n"), 0o644)
	w.deviceReport(testDevice)
	return w
}

func (w *world) probe(p ProbeReport) {
	b, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(w.remote, "probe.json"), b, 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) env() *Env {
	r, err := sshx.FromEnv(os.Getenv)
	if err != nil {
		w.t.Fatal(err)
	}
	return &Env{SSH: r, Admin: w.admin, StateDir: w.state, Home: w.home, SelfVersion: "0.0.0-dev",
		Events: func(e Event) { w.events = append(w.events, e) }, Now: func() time.Time { return w.now },
		HostKeyDir: filepath.Join(w.remote, "etc-ssh"), Command: "wy machine add test"}
}

func (w *world) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(w.remote, rel))
	return string(b)
}

func TestAddDirect(t *testing.T) {
	w := newWorld(t)
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env())
	if err != nil {
		t.Fatalf("Add: %v\nevents: %+v", err, w.events)
	}
	if !sum.Verified || sum.Reach != ReachDirect || sum.Machine != testMID || sum.OS == "" {
		t.Fatalf("summary %+v", sum)
	}
	got, _ := os.ReadFile(filepath.Join(w.remote, ".local/bin/wy"))
	if string(got) != fakeWy {
		t.Fatalf("remote wy not the copied file")
	}
	if fi, _ := os.Stat(filepath.Join(w.remote, ".local/bin/wy")); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("remote wy not executable")
	}
	if s := w.read("setup.stdin"); s != testSecret+"\n" {
		t.Fatalf("setup stdin %q", s)
	}
	args := w.read("setup.args")
	for _, want := range []string{"setup --yes --json --login never --hub join:" + testHubURL + " --code-stdin"} {
		if !strings.Contains(args, want) {
			t.Fatalf("setup args %q, want %q", args, want)
		}
	}
	if !strings.Contains(w.read("probe.args"), "--hub-url "+testHubURL) {
		t.Fatalf("probe args %q", w.read("probe.args"))
	}
	w.fake.AssertNoSecret(testSecret)
	if c := w.admin.called("pair --kind machine"); !strings.Contains(strings.Join(c, " "), "--machine "+testMID) || !strings.Contains(strings.Join(c, " "), "--url "+testHubURL) {
		t.Fatalf("pair call %q", c)
	}
	reg, _ := LoadRegistry(w.state)
	e, ok := reg.Find("gpu-box")
	if !ok || e.SSH != testHost || e.Reach != ReachDirect || e.HubURL != testHubURL {
		t.Fatalf("registry %+v", reg.Machines)
	}
	for _, c := range w.fake.Calls() {
		if !c.G && !contains(c.Argv, "BatchMode=yes") {
			t.Fatalf("non-interactive call without BatchMode: %q", c.Argv)
		}
	}

	// A rerun with the same file and an already connected remote mints nothing and does not copy.
	w.probe(ProbeReport{V: 1, Machine: testMID, Name: "gpu-box", Hub: &ProbeHub{URL: testHubURL, Reachable: true}, Connected: ProbeConnected{URL: testHubURL, Reach: "direct", Token: true}})
	w.admin.calls = nil
	before := len(w.fake.Calls())
	if _, err := Add(context.Background(), Options{Host: testHost, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env()); err != nil {
		t.Fatal(err)
	}
	if w.admin.called("pair") != nil {
		t.Fatal("rerun minted a new code")
	}
	if !strings.Contains(w.read("setup.args"), "--hub keep") {
		t.Fatalf("rerun setup args %q", w.read("setup.args"))
	}
	for _, c := range w.fake.Calls()[before:] {
		if strings.Contains(c.Remote, `cat > "$t"`) {
			t.Fatal("rerun copied the binary again")
		}
	}
}

func TestAddNoClaudeAndVerifyFail(t *testing.T) {
	w := newWorld(t)
	os.WriteFile(filepath.Join(w.remote, "setup.exit"), []byte("7\n"), 0o644)
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin}, w.env())
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitNoClaude || sum.ExitCode != ExitNoClaude {
		t.Fatalf("err %v sum %+v", err, sum)
	}
	if _, ok := mustReg(t, w.state).Find("gpu-box"); !ok {
		t.Fatal("registry not saved after exit 7")
	}

	os.Remove(filepath.Join(w.remote, "setup.exit"))
	w.admin.answer = func(args []string, stdin []byte) ([]byte, error) {
		if args[0] == "machine" {
			return []byte(`{"id":"x","last_seen":"2001-01-01T00:00:00Z"}`), nil
		}
		return hubAnswers(w.now)(args, stdin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = Add(ctx, Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin}, w.env())
	if !errors.As(err, &ee) || ee.Code != ExitVerify {
		t.Fatalf("want exit 6, got %v", err)
	}
}

func TestAddNeedsChannel(t *testing.T) {
	w := newWorld(t)
	e := w.env()
	e.Admin = nil
	_, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin}, e)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitNeedsPerson {
		t.Fatalf("want exit 3, got %v", err)
	}
	// --code-stdin: the digits go to the remote on stdin.
	w.probe(ProbeReport{V: 1, Machine: testMID, Name: "gpu-box"})
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, CodeStdin: true, Code: "123-456"}, e); err != nil {
		t.Fatal(err)
	}
	if w.read("setup.stdin") != "123-456\n" {
		t.Fatalf("stdin %q", w.read("setup.stdin"))
	}
	w.fake.AssertNoSecret("123-456")
}

func TestAddUnreachableNeedsReach(t *testing.T) {
	w := newWorld(t)
	_, err := Add(context.Background(), Options{Host: testHost, Binary: w.bin, Yes: true}, w.env())
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitUsage {
		t.Fatalf("want exit 2, got %v", err)
	}
}

func TestAddDryRunChangesNothing(t *testing.T) {
	w := newWorld(t)
	noCache := testtree.Opts{Ignore: func(rel string) bool { return rel == ".cache" }} // a login shell may make it
	snap := testtree.Take(t, w.remote, noCache)
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachForward, Binary: w.bin, DryRun: true}, w.env()); err != nil {
		t.Fatal(err)
	}
	testtree.Equal(t, snap, testtree.Take(t, w.remote, noCache))
	if len(w.admin.calls) != 0 {
		t.Fatalf("dry run used the admin channel: %q", w.admin.calls)
	}
	if ents, _ := os.ReadDir(w.home); len(ents) != 0 {
		t.Fatal("dry run wrote in the operator's home")
	}
}

func TestAddForward(t *testing.T) {
	w := newWorld(t)
	os.MkdirAll(filepath.Join(w.home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(w.home, ".ssh", "config"), []byte("Host *\n  ServerAliveInterval 60\n"), 0o600)
	before := testtree.Take(t, w.home, testtree.Opts{})
	e := w.env()
	e.OperatorHub = testHubURL
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachForward, Binary: w.bin}, e)
	if err != nil {
		t.Fatalf("Add: %v %+v", err, w.events)
	}
	if sum.Port != 47830 || sum.HubURL != testHubURL {
		t.Fatalf("summary %+v", sum)
	}
	conf, _ := os.ReadFile(filepath.Join(w.home, ".ssh", ForwardConf))
	if !strings.Contains(string(conf), "Host gpu-box\n  RemoteForward 127.0.0.1:47830 hub.example:8443\n") {
		t.Fatalf("conf %q", conf)
	}
	args := w.read("setup.args")
	if !strings.Contains(args, "--hub-dial 127.0.0.1:47830 --reach forward") {
		t.Fatalf("setup args %q", args)
	}
	var setupCall *sshxtest.Call
	for _, c := range w.fake.Calls() {
		if strings.Contains(c.Remote, "'setup'") {
			c := c
			setupCall = &c
		}
	}
	if setupCall == nil || len(setupCall.Fwd) != 1 || setupCall.Fwd[0] != "127.0.0.1:47830:hub.example:8443" || !contains(setupCall.Argv, "ExitOnForwardFailure=yes") {
		t.Fatalf("setup call %+v", setupCall)
	}
	// Remove restores the operator's ~/.ssh exactly.
	if _, err := Remove(context.Background(), "gpu-box", RemoveOptions{}, &ManageEnv{SSH: e.SSH, StateDir: w.state, Home: w.home}); err != nil {
		t.Fatal(err)
	}
	testtree.Equal(t, before, testtree.Take(t, w.home, testtree.Opts{}))
}

func TestAddForwardPortBusyRetries(t *testing.T) {
	w := newWorld(t)
	w.fake.Rules(sshxtest.Rule{Match: "'setup'", ForwardOnly: true, Exit: 255, Stderr: "Error: remote port forwarding failed for listen port 47830\n"})
	e := w.env()
	e.OperatorHub = testHubURL
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachForward, Binary: w.bin}, e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if w.read("setup.stdin") != testSecret+"\n" {
		t.Fatal("the retry without -R did not run setup with the secret")
	}
	w.fake.AssertNoSecret(testSecret)
}

func TestAddPull(t *testing.T) {
	w := newWorld(t)
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachPull, Binary: w.bin, PullFrom: "10.0.0.0/8"}, w.env())
	if err != nil {
		t.Fatalf("Add: %v %+v", err, w.events)
	}
	if !sum.Verified || sum.PullID != "pt_01ABC" {
		t.Fatalf("summary %+v", sum)
	}
	add := strings.Join(w.admin.called("pull add"), " ")
	if !strings.Contains(add, "pull add gpu-box --ssh me@gpu-box --port 22 --machine "+testMID+" --host-keys-stdin") {
		t.Fatalf("pull add %q", add)
	}
	var keys []byte
	for i, c := range w.admin.calls {
		if c[0] == "pull" && c[1] == "add" {
			keys = w.admin.stdins[i]
		}
	}
	if string(keys) != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHost\n" {
		t.Fatalf("host keys on stdin %q", keys)
	}
	if !strings.Contains(w.read("setup.args"), "--hub pull") || strings.Contains(w.read("setup.args"), "code-stdin") {
		t.Fatalf("setup args %q", w.read("setup.args"))
	}
	if !strings.Contains(w.read("authorize.args"), "authorize-pull --key-stdin --target pt_01ABC --from 10.0.0.0/8") ||
		!strings.HasPrefix(w.read("authorize.stdin"), "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub") {
		t.Fatalf("authorize %q %q", w.read("authorize.args"), w.read("authorize.stdin"))
	}
	if w.admin.called("pull run gpu-box --wait") == nil {
		t.Fatal("no first pull")
	}
	// remove: the remote line, the pull target (with --revoke).
	res, err := Remove(context.Background(), "gpu-box", RemoveOptions{Revoke: true}, &ManageEnv{SSH: w.env().SSH, Admin: w.admin, StateDir: w.state, Home: w.home})
	if err != nil || !res.PullLine || !res.PullTarget || !res.Revoked {
		t.Fatalf("remove %+v %v", res, err)
	}
	if !strings.Contains(w.read("authorize.args"), "--remove --target pt_01ABC") {
		t.Fatalf("authorize %q", w.read("authorize.args"))
	}
	if w.admin.called("pull remove gpu-box --revoke") == nil {
		t.Fatal("no pull remove")
	}
	if len(mustReg(t, w.state).Machines) != 0 {
		t.Fatal("registry entry left")
	}
}

func mustReg(t *testing.T, dir string) *Registry {
	r, err := LoadRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// --- transfer -------------------------------------------------------------------------

func TestPushRefusesPathLikeNames(t *testing.T) {
	w := newWorld(t)
	r := remote{ssh: w.env().SSH, host: testHost}
	for _, name := range []string{"../evil", "a/b", ".hidden", "has space"} {
		if _, err := r.push(context.Background(), w.bin, "update", name, ""); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestPushHashMismatchReplacesNothing(t *testing.T) {
	w := newWorld(t)
	os.MkdirAll(filepath.Join(w.remote, ".local/bin"), 0o755)
	os.WriteFile(filepath.Join(w.remote, ".local/bin/wy"), []byte("old"), 0o755)
	r := remote{ssh: w.env().SSH, host: testHost}
	w.fake.Rules(sshxtest.Rule{Match: `cat > "$t"`, Stdout: "path=x\nsha=" + strings.Repeat("ab", 32) + "  -\n"})
	if _, err := r.push(context.Background(), w.bin, "bin", "wy", ""); err == nil || !strings.Contains(err.Error(), "nothing was replaced") {
		t.Fatalf("want mismatch, got %v", err)
	}
	if w.read(".local/bin/wy") != "old" {
		t.Fatal("mismatch replaced the binary")
	}
	// No hash tool and no version to check: refused.
	w.fake.Rules(sshxtest.Rule{Match: `cat > "$t"`, Stdout: "path=x\nsha=none\n"})
	if _, err := r.push(context.Background(), w.bin, "bin", "wy", ""); err == nil {
		t.Fatal("unchecked copy accepted")
	}
	// The real script on the fake remote: verified and moved.
	w.fake.Rules()
	p, err := r.push(context.Background(), w.bin, "update", "wy-1.2.3", "")
	if err != nil || !p.Verified || p.Path != filepath.Join(w.remote, ".local/state/watchyard/update/wy-1.2.3") {
		t.Fatalf("push %+v %v", p, err)
	}
	ents, _ := os.ReadDir(filepath.Join(w.remote, ".local/state/watchyard/update"))
	if len(ents) != 1 {
		t.Fatalf("leftover temporary files: %v", ents)
	}
}

func TestFleetRemote(t *testing.T) {
	w := newWorld(t)
	fr := FleetRemote{SSH: w.env().SSH}
	m := update.FleetMachine{Name: "gpu-box", Target: testHost}
	goos, goarch, err := fr.Platform(context.Background(), m)
	if err != nil || goos == "" || goarch == "" {
		t.Fatalf("platform %s %s %v", goos, goarch, err)
	}
	p, err := fr.Push(context.Background(), m, w.bin, "wy-new")
	if err != nil || !strings.HasSuffix(p, "/wy-new") {
		t.Fatalf("push %q %v", p, err)
	}
	out, code, err := fr.Run(context.Background(), m, []string{"cat", p})
	if err != nil || code != 0 || out != fakeWy {
		t.Fatalf("run %q %d %v", out, code, err)
	}
	reg := mustReg(t, w.state)
	reg.Upsert(Entry{Name: "hub", Machine: "m_h", SSH: "hub-host"}, w.now)
	reg.Upsert(Entry{Name: "gpu-box", Machine: testMID, SSH: testHost, OS: "linux", Arch: "amd64"}, w.now)
	reg.Save()
	ms, err := FleetRegistry{StateDir: w.state, HubAdmin: "ssh -p 22 hub-host wy"}.Machines()
	if err != nil || len(ms) != 2 || !ms[1].Hub || ms[0].Hub || ms[0].Target != testHost {
		t.Fatalf("fleet %+v %v", ms, err)
	}
}

func TestPlatform(t *testing.T) {
	for in, want := range map[string]string{"Linux x86_64\n": "linux/amd64", "Linux aarch64": "linux/arm64", "Darwin arm64": "darwin/arm64", "Darwin x86_64": "darwin/amd64"} {
		o, a, err := Platform(in)
		if err != nil || o+"/"+a != want {
			t.Errorf("%q: %s/%s %v", in, o, a, err)
		}
	}
	if _, _, err := Platform("MINGW64_NT-10.0 x86_64"); !errors.Is(err, ErrWindows) {
		t.Errorf("windows: %v", err)
	}
	if _, _, err := Platform("Linux mips"); err == nil {
		t.Error("mips accepted")
	}
}

// --- forward block --------------------------------------------------------------------

func TestForwardIdempotentAndUndoneExactly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string // "" = no ~/.ssh at all
	}{{"no ssh folder", ""}, {"existing config", "Host *\n  ForwardAgent no\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			home, state := t.TempDir(), t.TempDir()
			if tc.config != "" {
				testtree.New(t, home).Dir(".ssh", 0o700).File(".ssh/config", tc.config, 0o644).
					Mtime(".ssh/config", time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
			}
			before := testtree.Take(t, home, testtree.Opts{DirMtimes: false})
			f := Forward{Host: "gpu-box", Machine: testMID, Name: "gpu-box", Port: 47830, Target: "hub.example:443"}
			r1, err := WriteForward(home, state, f, "test", time.Now())
			if err != nil || !r1.Changed || r1.Set == "" {
				t.Fatalf("first write %+v %v", r1, err)
			}
			mid := testtree.Take(t, home, testtree.Opts{})
			r2, err := WriteForward(home, state, f, "test", time.Now())
			if err != nil || r2.Changed {
				t.Fatalf("second write %+v %v", r2, err)
			}
			testtree.Equal(t, mid, testtree.Take(t, home, testtree.Opts{}))
			cfg, _ := os.ReadFile(filepath.Join(home, ".ssh/config"))
			if !strings.HasPrefix(string(cfg), IncludeLine) || !strings.HasSuffix(string(cfg), tc.config) {
				t.Fatalf("config %q", cfg)
			}
			if fi, _ := os.Stat(filepath.Join(home, ".ssh/config")); tc.config != "" && fi.Mode().Perm() != 0o644 {
				t.Fatalf("mode changed: %v", fi.Mode())
			}
			done, err := RemoveForward(home, state, testMID, r1.Set, "test", time.Now())
			if err != nil || !done {
				t.Fatalf("remove %v %v", done, err)
			}
			testtree.Equal(t, before, testtree.Take(t, home, testtree.Opts{}))
		})
	}
}

func TestForwardRemoveAfterOtherEdits(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	testtree.New(t, home).Dir(".ssh", 0o700).File(".ssh/config", "Host a\n  User x\n", 0o600)
	f := Forward{Host: "gpu-box", Machine: testMID, Name: "gpu-box", Port: 47831, Target: "hub.example:443"}
	r, err := WriteForward(home, state, f, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other := Forward{Host: "nas", Machine: "m_other", Name: "nas", Port: 47832, Target: "hub.example:443"}
	if _, err := WriteForward(home, state, other, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	// The person edits config afterwards: remove takes only our block out.
	cfgPath := filepath.Join(home, ".ssh/config")
	cfg, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, append(cfg, "Host b\n"...), 0o600)
	if done, err := RemoveForward(home, state, testMID, r.Set, "test", time.Now()); err != nil || !done {
		t.Fatalf("remove %v %v", done, err)
	}
	conf, _ := os.ReadFile(filepath.Join(home, ".ssh", ForwardConf))
	if string(conf) != other.Block() {
		t.Fatalf("conf %q", conf)
	}
	cfg2, _ := os.ReadFile(cfgPath)
	if string(cfg2) != IncludeLine+"Host a\n  User x\nHost b\n" {
		t.Fatalf("config %q", cfg2)
	}
	if done, err := RemoveForward(home, state, "m_other", "", "test", time.Now()); err != nil || !done {
		t.Fatalf("remove other %v %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", ForwardConf)); !os.IsNotExist(err) {
		t.Fatal("empty watchyard.conf left")
	}
	cfg3, _ := os.ReadFile(cfgPath)
	if string(cfg3) != "Host a\n  User x\nHost b\n" {
		t.Fatalf("config %q", cfg3)
	}
}

func TestForwardAdopted(t *testing.T) {
	cfg := sshx.ParseConfig([]byte("host gpu-box\nremoteforward [127.0.0.1]:47833 hub.example:443\n"))
	if p, ok := Adopted(cfg, "hub.example:443", ""); !ok || p != 47833 {
		t.Fatalf("adopted %d %v", p, ok)
	}
	if _, ok := Adopted(cfg, "other:443", ""); ok {
		t.Fatal("adopted a forward to another target")
	}
}

// --- authorize-pull -------------------------------------------------------------------

func TestAuthorizePullTwiceOneLine(t *testing.T) {
	home := t.TempDir()
	orig := "# my keys\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMine me@laptop\n\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ== other\n"
	testtree.New(t, home).Dir(".ssh", 0o700).File(".ssh/authorized_keys", orig, 0o640)
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub watchyard-pull@hub"
	line, key, err := PullKeyLine(pub, "/home/me/.local/bin/wy", "pt_01ABC", "10.0.0.0/8", true)
	if err != nil {
		t.Fatal(err)
	}
	want := `restrict,from="10.0.0.0/8",command="/home/me/.local/bin/wy outbox drain --stdio" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub watchyard-pull@hub wy-pull:pt_01ABC`
	if line != want {
		t.Fatalf("line\n%s\nwant\n%s", line, want)
	}
	for i := 0; i < 2; i++ {
		if _, err := AuthorizePull(home, line, key, "pt_01ABC"); err != nil {
			t.Fatal(err)
		}
	}
	// A new target id with the same key replaces it too.
	line2, _, _ := PullKeyLine(pub, "/home/me/.local/bin/wy", "pt_01ABC", "", false)
	if _, err := AuthorizePull(home, line2, key, "pt_01ABC"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".ssh/authorized_keys"))
	if string(b) != orig+line2+"\n" {
		t.Fatalf("authorized_keys\n%s", b)
	}
	if !strings.HasPrefix(line2, LegacyRestrict+",command=") {
		t.Fatalf("legacy %q", line2)
	}
	if fi, _ := os.Stat(filepath.Join(home, ".ssh/authorized_keys")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if ch, err := RemovePull(home, "pt_01ABC"); err != nil || !ch {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(home, ".ssh/authorized_keys"))
	if string(b) != orig {
		t.Fatalf("after remove\n%q", b)
	}
}

func TestAuthorizePullNewFile(t *testing.T) {
	home := t.TempDir()
	line, key, _ := PullKeyLine("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub", "/u/wy", "pt_1", "", true)
	if _, err := AuthorizePull(home, line, key, "pt_1"); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(home, ".ssh"))
	fk, _ := os.Stat(filepath.Join(home, ".ssh/authorized_keys"))
	if fi.Mode().Perm() != 0o700 || fk.Mode().Perm() != 0o600 {
		t.Fatalf("modes %v %v", fi.Mode(), fk.Mode())
	}
	for _, bad := range [][4]string{
		{"not a key", "/u/wy", "pt_1", ""},
		{"ssh-ed25519 AAAA", "relative/wy", "pt_1", ""},
		{"ssh-ed25519 AAAA", "/u/wy", "xx", ""},
		{"ssh-ed25519 AAAA", "/u/wy", "pt_1", `1.2.3.4",command="x`},
		{"ssh-ed25519 AAAA", "/u/s y", "pt_1", ""},
	} {
		if _, _, err := PullKeyLine(bad[0], bad[1], bad[2], bad[3], true); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// --- admin, registry, repoint, list, probe --------------------------------------------

func TestPrefixAdmin(t *testing.T) {
	if _, err := NewPrefixAdmin("ssh -o StrictHostKeyChecking=no hub-host wy"); !errors.Is(err, sshx.ErrForbidden) {
		t.Fatalf("forbidden prefix: %v", err)
	}
	if h := AdminSSHHost("ssh -p 2222 -o BatchMode=yes hub-host wy"); h != "hub-host" {
		t.Fatalf("host %q", h)
	}
	if h := AdminSSHHost("docker compose exec hub wy"); h != "" {
		t.Fatalf("host %q", h)
	}
	p, err := NewPrefixAdmin("echo wy")
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Hub(context.Background(), []string{"pull", "list", "--json"}, nil)
	if err != nil || string(out) != "wy hub pull list --json\n" {
		t.Fatalf("out %q %v", out, err)
	}
	if _, err := p.Hub(context.Background(), []string{"x;rm"}, nil); err == nil {
		t.Fatal("unsafe word accepted")
	}
	f, _ := NewPrefixAdmin("false")
	_, err = f.Hub(context.Background(), []string{"pull"}, nil)
	var ae *AdminError
	if !errors.As(err, &ae) || ae.Exit != 1 {
		t.Fatalf("want AdminError, got %v", err)
	}
	// An ssh prefix quotes the appended words for the remote shell; secrets stay on stdin.
	fake := sshxtest.New(t)
	fake.RemoteHome("hub-host")
	sp, err := NewPrefixAdmin(strings.Join(fake.Command(), " ") + " hub-host echo")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := sp.Hub(context.Background(), []string{"pair", "--name", "a b"}, []byte(testSecret)); err == nil {
		t.Fatalf("a word with a space must be refused, got %q", out)
	}
	out, err = sp.Hub(context.Background(), []string{"pair", "--kind", "machine"}, []byte(testSecret))
	if err != nil || strings.TrimSpace(string(out)) != "hub pair --kind machine" {
		t.Fatalf("ssh prefix %q %v", out, err)
	}
	fake.AssertNoSecret(testSecret)
}

func TestMintAndOrigin(t *testing.T) {
	a := &fakeAdmin{answer: hubAnswers(time.Now())}
	c, err := MintMachineCode(context.Background(), a, "gpu-box", testMID, "")
	if err != nil || c.Secret != testSecret || c.Origin() != testHubURL {
		t.Fatalf("%+v %v", c, err)
	}
	a.answer = func([]string, []byte) ([]byte, error) { return []byte(`{"code":"1"}`), nil }
	if _, err := MintMachineCode(context.Background(), a, "x", "m", ""); err == nil {
		t.Fatal("no secret accepted")
	}
}

func TestRegistry(t *testing.T) {
	d := t.TempDir()
	r := mustReg(t, d)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.Upsert(Entry{Name: "b", Machine: "m_b", SSH: "me@b", Port: 47830}, now)
	r.Upsert(Entry{Name: "a", Machine: "m_a", SSH: "a"}, now)
	r.Upsert(Entry{Name: "b2", Machine: "m_b", SSH: "me@b", Port: 47831}, now.Add(time.Hour))
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	r2 := mustReg(t, d)
	if len(r2.Machines) != 2 || r2.Machines[0].Name != "a" {
		t.Fatalf("%+v", r2.Machines)
	}
	e, ok := r2.Find("me@b")
	if !ok || e.Name != "b2" || e.AddedAt != "2026-01-01T00:00:00Z" || e.UpdatedAt != "2026-01-01T01:00:00Z" {
		t.Fatalf("%+v", e)
	}
	if u := r2.UsedPorts("m_a", "me@b"); !u[47831] || len(u) != 1 {
		t.Fatalf("ports %v", u)
	}
	if fi, _ := os.Stat(RegistryPath(d)); fi.Mode().Perm() != 0o600 {
		t.Fatal("registry mode")
	}
	if !r2.Remove("a") || r2.Remove("a") {
		t.Fatal("remove")
	}
}

func TestRepointAndList(t *testing.T) {
	w := newWorld(t)
	os.MkdirAll(filepath.Join(w.remote, ".local/bin"), 0o755)
	os.WriteFile(filepath.Join(w.remote, ".local/bin/wy"), []byte(fakeWy), 0o755)
	other := w.fake.RemoteHome("nas")
	os.MkdirAll(filepath.Join(other, ".local/bin"), 0o755)
	os.WriteFile(filepath.Join(other, ".local/bin/wy"), []byte(fakeWy), 0o755)
	os.WriteFile(filepath.Join(other, "connect.exit"), []byte("1"), 0o644)
	r := mustReg(t, w.state)
	r.Upsert(Entry{Name: "gpu-box", Machine: testMID, SSH: testHost, Reach: ReachDirect, HubURL: "https://old"}, w.now)
	r.Upsert(Entry{Name: "nas", Machine: "m_nas", SSH: "nas", Reach: ReachForward}, w.now)
	r.Upsert(Entry{Name: "pi", Machine: "m_pi", SSH: "pi", Reach: ReachPull, PullTarget: "pt_1"}, w.now)
	r.Save()
	me := &ManageEnv{SSH: w.env().SSH, Admin: w.admin, StateDir: w.state, Home: w.home}
	rows, err := Repoint(context.Background(), "https://new.example", nil, me)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitFailed {
		t.Fatalf("want exit 1 for nas, got %v", err)
	}
	st := map[string]string{}
	for _, row := range rows {
		st[row.Name] = row.Status
	}
	if st["gpu-box"] != "ok" || st["nas"] != "fail" || st["pi"] != "skip" {
		t.Fatalf("rows %+v", rows)
	}
	if strings.TrimSpace(w.read("connect.args")) != "connect https://new.example --keep-token" {
		t.Fatalf("connect %q", w.read("connect.args"))
	}
	if e, _ := mustReg(t, w.state).Find("gpu-box"); e.HubURL != "https://new.example" {
		t.Fatalf("registry %+v", e)
	}
	if _, err := Repoint(context.Background(), "https://x", []string{"nope"}, me); err == nil {
		t.Fatal("unknown machine accepted")
	}

	lrows, err := List(context.Background(), me)
	if err != nil || len(lrows) != 3 {
		t.Fatalf("list %v %v", lrows, err)
	}
	if lrows[0].HubView == nil || lrows[0].Name != "gpu-box" {
		t.Fatalf("list %+v", lrows[0])
	}
}

func TestProbe(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	testtree.New(t, home).Dir(".claude", 0o700).File(".claude.json", "{}", 0o600)
	o := ProbeOptions{StateDir: state, ConfigPath: filepath.Join(t.TempDir(), "config.toml"), Home: home, HubURL: testHubURL,
		Ports: [2]int{47830, 47833}, Getenv: func(k string) string {
			if k == "SHELL" {
				return "/bin/zsh"
			}
			return ""
		},
		Listen: func(p int) error {
			if p == 47830 {
				return errors.New("busy")
			}
			return nil
		},
		Health: func(ctx context.Context, url string) (client.Health, error) {
			return client.Health{Version: "0.5.0"}, nil
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "ssh" {
				return []byte("OpenSSH_7.1p2, OpenSSL\n"), nil
			}
			return nil, errors.New("no")
		}}
	r, err := Probe(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Machine == "" || r.Shell != "/bin/zsh" || r.Restrict || !r.Hub.Reachable || r.Hub.Version != "0.5.0" ||
		len(r.Free) != 3 || r.Free[0] != 47831 || r.Connected.Token {
		t.Fatalf("%+v", r)
	}
	r2, _ := Probe(context.Background(), o)
	if r2.Machine != r.Machine {
		t.Fatal("machine id changed between probes")
	}
	if p, err := ParsePorts("47830-47849"); err != nil || p != [2]int{47830, 47849} {
		t.Fatal(p, err)
	}
	if _, err := ParsePorts("1-1000"); err == nil {
		t.Fatal("too wide accepted")
	}
	if !supportsRestrict("OpenSSH_9.6p1") || supportsRestrict("OpenSSH_6.9") {
		t.Fatal("restrict")
	}
}

func TestParsePreProbe(t *testing.T) {
	out := "home=/home/me\nshell=/bin/bash\nclaude=/home/me/.local/bin/claude\nwy=0.4.0\nwysha=ABCDEF  -\nhostkey=ssh-ed25519 AAAA root@x\nhostkey=ecdsa-sha2-nistp256 BBBB\nend=1\n"
	p, err := parsePreProbe([]byte(out))
	if err != nil || p.Wy != "0.4.0" || p.WySHA256 != "abcdef" || len(p.HostKeys) != 2 || p.HostKeys[0] != "ssh-ed25519 AAAA" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := parsePreProbe([]byte("home=/x\n")); err == nil {
		t.Fatal("unfinished probe accepted")
	}
}

var _ = bytes.Equal
