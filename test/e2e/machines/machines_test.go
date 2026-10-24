// Package machines is the end-to-end test over real SSH (scenario M3, docs/spec/machines.md): the
// real `wy` binary adds an Ubuntu sshd container as a
// pull machine, the hub pulls records from it, and `wy machine remove` takes it back out.
//
//	go test ./test/e2e/machines          (what `make e2e-machines` runs)
//
// The operator is this machine with a temporary HOME; the hub is `wy hub serve` on a
// loopback port (so the admin channel is `local`); the server is a container whose sshd is
// published on 127.0.0.1. The container needs a runtime: WY_E2E_RUNTIME, else podman, else
// docker. The test skips (never fails) when there is none, when it cannot run containers,
// with -short, on Windows, and when ssh is missing.
//
// Host key checking is never turned off: the harness reads the container's host keys with
// `<runtime> exec` and writes a known_hosts, and the hub pins the keys `wy machine add`
// probes from /etc/ssh on the server. Nothing here touches the real HOME, ~/.ssh,
// CLAUDE_CONFIG_DIR or any systemd/launchd unit (the container has no systemd and the
// operator's wy runs with a temp HOME).
package machines

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

// rig is the whole setup: container, operator home, hub.
type rig struct {
	t       *testing.T
	rt      string // container runtime binary
	tmp     string
	home    string // operator HOME
	env     []string
	wy      string // wy for this machine (the hub and the operator)
	wyLinux string // wy for the container (linux/amd64), installed with --binary
	sshCfg  string
	sshPort string
	cname   string
	hubBase string
	hubLog  *syncBuf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// repoRoot is the module root (go env GOMOD).
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

func goBuild(t *testing.T, root, goos, goarch, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s (%s/%s): %v\n%s", pkg, goos, goarch, err, b)
	}
}

// pickRuntime follows WY_E2E_RUNTIME, then podman, then docker, and skips when the chosen
// one cannot run containers (rootless podman without user namespaces, a stopped docker).
func pickRuntime(t *testing.T) string {
	t.Helper()
	cands := []string{"podman", "docker"}
	if v := os.Getenv("WY_E2E_RUNTIME"); v != "" {
		cands = []string{v}
	}
	for _, c := range cands {
		p, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = exec.CommandContext(ctx, p, "info").Run()
		cancel()
		if err != nil {
			t.Skipf("%s is installed but `%s info` fails (%v): cannot run containers here", c, c, err)
		}
		return p
	}
	t.Skip("no container runtime (podman or docker; WY_E2E_RUNTIME selects one)")
	return ""
}

// cleanEnv is the parent's environment without anything that could point wy or Claude Code
// at a real home, plus a temp HOME.
func cleanEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "WY_") || strings.HasPrefix(k, "XDG_") ||
			k == "HOME" || k == "TZ" || k == "NO_COLOR" || k == "SSH_AUTH_SOCK" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "TZ=UTC", "NO_COLOR=1")
}

// runCmd runs a command and returns stdout, stderr and the exit code (-1 when it did not
// start).
func runCmd(env []string, stdin string, name string, args ...string) (string, string, int) {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	return out.String(), errb.String(), code
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end: builds wy and runs a container")
	}
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX host (ssh, unix paths)")
	}
	for _, tool := range []string{"ssh", "ssh-keygen", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found on PATH", tool)
		}
	}
	r := &rig{t: t, rt: pickRuntime(t), hubLog: &syncBuf{}}
	root := repoRoot(t)
	r.tmp = t.TempDir()
	r.home = filepath.Join(r.tmp, "home")
	if err := os.MkdirAll(r.home, 0o700); err != nil {
		t.Fatal(err)
	}

	// Binaries: wy for the container and for this host (one build when they match), and the
	// fake claude for the container. Built into temp dirs; nothing is committed.
	bin := filepath.Join(r.tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	r.wyLinux = filepath.Join(bin, "wy-linux-amd64")
	goBuild(t, root, "linux", "amd64", r.wyLinux, "./cmd/wy")
	r.wy = r.wyLinux
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		r.wy = filepath.Join(bin, "wy")
		goBuild(t, root, runtime.GOOS, runtime.GOARCH, r.wy, "./cmd/wy")
	}

	// Build context for the image: Containerfile, the test key and the fake claude.
	ctxDir := filepath.Join(r.tmp, "image")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cf, err := os.ReadFile(filepath.Join(root, "test", "e2e", "machines", "Containerfile.sshd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "Containerfile"), cf, 0o644); err != nil {
		t.Fatal(err)
	}
	goBuild(t, root, "linux", "amd64", filepath.Join(ctxDir, "claude"), "./test/e2e/setup/fakeclaude")
	sshDir := filepath.Join(r.tmp, "ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(sshDir, "id_ed25519")
	if _, errb, code := runCmd(os.Environ(), "", "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "wy-e2e", "-f", key); code != 0 {
		t.Fatalf("ssh-keygen: %s", errb)
	}
	pub, _ := os.ReadFile(key + ".pub")
	if err := os.WriteFile(filepath.Join(ctxDir, "authorized_key.pub"), pub, 0o644); err != nil {
		t.Fatal(err)
	}

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	id := hex.EncodeToString(suffix)
	image := "wy-e2e-sshd:" + id
	r.cname = "wy-e2e-srv-" + id
	t.Cleanup(func() {
		// Even on failure: the container and the image go.
		_ = exec.Command(r.rt, "rm", "-f", r.cname).Run()
		_ = exec.Command(r.rt, "rmi", "-f", image).Run()
	})
	if out, err := exec.Command(r.rt, "build", "-q", "-t", image, "-f", filepath.Join(ctxDir, "Containerfile"), ctxDir).CombinedOutput(); err != nil {
		t.Fatalf("%s build: %v\n%s", r.rt, err, out)
	}
	if out, err := exec.Command(r.rt, "run", "-d", "--name", r.cname, "-p", "127.0.0.1::22", image).CombinedOutput(); err != nil {
		t.Fatalf("%s run: %v\n%s", r.rt, err, out)
	}
	out, err := exec.Command(r.rt, "port", r.cname, "22/tcp").Output()
	if err != nil {
		t.Fatalf("%s port: %v", r.rt, err)
	}
	// "127.0.0.1:40123" (podman may also print an IPv6 line): take the first line's port.
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	_, r.sshPort, err = net.SplitHostPort(line)
	if err != nil {
		t.Fatalf("cannot parse published port %q: %v", line, err)
	}

	// known_hosts from the container's own host keys (never disabled, never scanned blind).
	var kh strings.Builder
	for _, typ := range []string{"ed25519", "ecdsa", "rsa"} {
		o, err := exec.Command(r.rt, "exec", r.cname, "cat", "/etc/ssh/ssh_host_"+typ+"_key.pub").Output()
		if err != nil {
			t.Fatalf("reading the %s host key: %v", typ, err)
		}
		f := strings.Fields(string(o))
		if len(f) < 2 {
			t.Fatalf("odd host key file: %q", o)
		}
		fmt.Fprintf(&kh, "[127.0.0.1]:%s %s %s\n", r.sshPort, f[0], f[1])
	}
	known := filepath.Join(sshDir, "known_hosts")
	if err := os.WriteFile(known, []byte(kh.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	r.sshCfg = filepath.Join(sshDir, "ssh_config")
	cfg := fmt.Sprintf(`Host srv
  HostName 127.0.0.1
  Port %s
  User dev
  IdentityFile %s
  IdentitiesOnly yes
  UserKnownHostsFile %s
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  BatchMode yes
  ConnectTimeout 10
`, r.sshPort, key, known)
	if err := os.WriteFile(r.sshCfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// WY_SSH_COMMAND is how the operator's wy reaches the server (OpenSSH reads ~/.ssh from
	// the password database, not $HOME, so a config file is passed explicitly).
	r.env = append(cleanEnv(r.home), "WY_SSH_COMMAND=ssh -F "+r.sshCfg)

	// sshd needs a moment after `run`.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, errb, code := runCmd(r.env, "", "ssh", "-F", r.sshCfg, "srv", "true"); code == 0 {
			break
		} else if time.Now().After(deadline) {
			logs, _ := exec.Command(r.rt, "logs", r.cname).CombinedOutput()
			t.Fatalf("sshd in the container never accepted the test key: %s\ncontainer log:\n%s", errb, logs)
		}
		time.Sleep(500 * time.Millisecond)
	}
	r.startHub()
	return r
}

// startHub runs `wy hub serve` on a free loopback port with the operator's HOME, so the
// operator's `wy hub …` commands (the `local` admin channel) share its database.
func (r *rig) startHub() {
	t := r.t
	cmd := exec.Command(r.wy, "hub", "serve", "--listen", "127.0.0.1:0", "--no-push")
	cmd.Env = r.env
	pr, pw := io.Pipe()
	cmd.Stderr = pw
	cmd.Stdout = r.hubLog
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); pw.Close() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Logf("hub log:\n%s", r.hubLog)
		}
	})
	addr := make(chan string, 1)
	listening := regexp.MustCompile(`listening on http://(\S+)`)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			fmt.Fprintln(r.hubLog, sc.Text())
			if m := listening.FindStringSubmatch(sc.Text()); m != nil {
				select {
				case addr <- m[1]:
				default:
				}
			}
		}
	}()
	select {
	case a := <-addr:
		r.hubBase = "http://" + a
	case err := <-done:
		t.Fatalf("hub exited: %v\n%s", err, r.hubLog)
	case <-time.After(30 * time.Second):
		t.Fatalf("hub did not start:\n%s", r.hubLog)
	}
}

// wyLocal runs the operator's wy; the test fails on a non-zero exit unless want says
// otherwise.
func (r *rig) wyLocal(stdin string, args ...string) (string, string) {
	r.t.Helper()
	out, errb, code := runCmd(r.env, stdin, r.wy, args...)
	if code != 0 {
		r.t.Fatalf("wy %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errb)
	}
	return out, errb
}

// ssh runs a command on the server as dev, through the test ssh config.
func (r *rig) ssh(stdin string, remote ...string) (string, string, int) {
	return runCmd(r.env, stdin, "ssh", append([]string{"-F", r.sshCfg, "srv"}, remote...)...)
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestPullRoundTrip is scenario M3 against one server: add --reach pull, the hub pulls
// records, remove.
func TestPullRoundTrip(t *testing.T) {
	r := newRig(t)

	// 1. One command sets the server up; the hub is local, so --hub-admin local is implied by
	// hub.db existing, but the test passes it to be explicit.
	out, errb := r.wyLocal("", "machine", "add", "srv", "--name", "srv1", "--reach", "pull",
		"--hub-admin", "local", "--binary", r.wyLinux, "--yes", "--json")
	var sum struct {
		Machine  string `json:"machine"`
		Reach    string `json:"reach"`
		PullID   string `json:"pull_target"`
		PullName string `json:"pull_target_name"`
		Verified bool   `json:"verified"`
		Exit     int    `json:"exit_code"`
	}
	for _, ln := range strings.Split(out, "\n") {
		var w struct {
			Summary *json.RawMessage `json:"summary"`
		}
		if json.Unmarshal([]byte(ln), &w) == nil && w.Summary != nil {
			_ = json.Unmarshal(*w.Summary, &sum)
		}
	}
	if sum.Machine == "" || sum.PullID == "" {
		t.Fatalf("no machine/pull target in the add summary:\n%s%s", out, errb)
	}
	if sum.Reach != "pull" || !sum.Verified {
		t.Errorf("summary: reach %q verified %v, want pull, true", sum.Reach, sum.Verified)
	}

	// 2. The binary on the server is byte-identical to the one we built.
	shaOut, _, code := r.ssh("", "sha256sum", ".local/bin/wy")
	if code != 0 {
		t.Fatalf("remote sha256sum: exit %d", code)
	}
	if got, want := strings.Fields(shaOut)[0], sha256File(t, r.wyLinux); got != want {
		t.Errorf("remote wy sha256 %s, want %s", got, want)
	}

	// 3. authorized_keys has the test key and exactly one hub line, restricted to the drain.
	ak, _, _ := r.ssh("", "cat", ".ssh/authorized_keys")
	var pullLines []string
	for _, ln := range strings.Split(ak, "\n") {
		if strings.Contains(ln, "wy-pull:") {
			pullLines = append(pullLines, ln)
		}
	}
	if len(pullLines) != 1 {
		t.Fatalf("want exactly one wy-pull line in authorized_keys, got %d:\n%s", len(pullLines), ak)
	}
	line := pullLines[0]
	if !strings.HasPrefix(line, "restrict,") || !strings.Contains(line, `command="`) ||
		!strings.Contains(line, "/wy outbox drain --stdio") || !strings.HasSuffix(line, "wy-pull:"+sum.PullID) {
		t.Errorf("authorized_keys line is not the restricted drain line for %s:\n%s", sum.PullID, line)
	}
	if !strings.Contains(ak, "wy-e2e") {
		t.Errorf("the test key vanished from authorized_keys:\n%s", ak)
	}

	// 4. Records written on the server (through its own outbox, as a hook would) reach the hub
	// by pull, with reach pull.
	idOut, _, code := r.ssh("", ".local/bin/wy", "machine", "show", "--json")
	if code != 0 {
		t.Fatalf("remote wy machine show: exit %d", code)
	}
	var rm struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(idOut), &rm); err != nil || rm.ID != sum.Machine {
		t.Fatalf("remote machine id %q (%v), want the registered %s", rm.ID, err, sum.Machine)
	}
	var recs strings.Builder
	sessions := []string{"e2e-sess-1", "e2e-sess-2", "e2e-sess-3"}
	for _, sid := range sessions {
		raw, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": "SessionStart", "source": "startup",
			"cwd": "/home/dev/api", "transcript_path": "/home/dev/.claude/projects/x/" + sid + ".jsonl"})
		rec, err := record.NewHookEvent(record.Meta{Machine: sum.Machine, Slot: "~/.claude",
			Seat: &record.Seat{Account: "acc-e2e", Org: "org-e2e", Email: "e2e@example.com"}, Client: "wy/e2e"}, raw, record.HookOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := rec.Encode()
		if err != nil {
			t.Fatal(err)
		}
		recs.Write(b)
		recs.WriteByte('\n')
	}
	if _, errb, code := r.ssh(recs.String(), ".local/bin/wy", "outbox", "append"); code != 0 {
		t.Fatalf("remote wy outbox append: exit %d: %s", code, errb)
	}
	r.wyLocal("", "hub", "pull", "run", sum.PullName, "--wait")

	tok, _ := r.wyLocal("", "hub", "token", "create", "--kind", "desktop", "--machine", sum.Machine, "--name", "e2e-reader", "--json")
	var tk struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(tok), &tk); err != nil || tk.Token == "" {
		t.Fatalf("token create: %q (%v)", tok, err)
	}
	var got map[string]bool
	deadline := time.Now().Add(60 * time.Second)
	for {
		var body struct {
			Sessions []struct {
				ID      string `json:"id"`
				Machine string `json:"machine"`
			} `json:"sessions"`
		}
		r.api(tk.Token, "/v1/sessions?machine="+sum.Machine+"&include_ended=true", &body)
		got = map[string]bool{}
		for _, s := range body.Sessions {
			got[s.ID] = true
		}
		if len(got) >= len(sessions) || time.Now().After(deadline) {
			break
		}
		r.wyLocal("", "hub", "pull", "run", sum.PullName, "--wait")
	}
	if len(got) != len(sessions) {
		list, _ := r.wyLocal("", "hub", "pull", "list")
		t.Fatalf("hub sees sessions %v, want %d from the pull\npull list:\n%s", got, len(sessions), list)
	}
	mj, _ := r.wyLocal("", "hub", "machine", "show", sum.Machine, "--json")
	var m struct {
		Reach *string `json:"reach"`
		Pull  *struct {
			State string `json:"state"`
		} `json:"pull"`
	}
	if err := json.Unmarshal([]byte(mj), &m); err != nil {
		t.Fatal(err)
	}
	if m.Reach == nil || *m.Reach != "pull" {
		t.Errorf("machine reach = %v, want pull\n%s", m.Reach, mj)
	}
	if m.Pull == nil || m.Pull.State == "host_key" || m.Pull.State == "refused" {
		t.Errorf("pull target state is bad: %s", mj)
	}

	// 5. Remove: the authorized_keys line and the pull target are gone, the test key stays.
	r.wyLocal("", "machine", "remove", "srv1")
	ak, _, _ = r.ssh("", "cat", ".ssh/authorized_keys")
	if strings.Contains(ak, "wy-pull:") {
		t.Errorf("the hub's line is still in authorized_keys after remove:\n%s", ak)
	}
	if !strings.Contains(ak, "wy-e2e") {
		t.Errorf("remove took the test key out of authorized_keys:\n%s", ak)
	}
	list, _ := r.wyLocal("", "hub", "pull", "list", "--json")
	if strings.Contains(list, sum.PullID) {
		t.Errorf("pull target %s still listed after remove:\n%s", sum.PullID, list)
	}
	// The server keeps working for the operator (remove does not lock anyone out).
	if _, _, code := r.ssh("", "true"); code != 0 {
		t.Errorf("the operator can no longer ssh to the server after remove")
	}
}

// TestForwardAddRegistersLinkKeys: `wy machine add --reach forward` joins the server through
// an SSH forward to the operator's hub, registers its link keys over the SSH session (reg
// ssh, keys.md 4.2) and pins the hub key there (how ssh); the server then reaches the hub
// with its own token through the forward and finds its keys registered.
func TestForwardAddRegistersLinkKeys(t *testing.T) {
	r := newRig(t)
	r.wyLocal("", "config", "set", "hub.url", r.hubBase)
	out, errb := r.wyLocal("", "machine", "add", "srv", "--name", "srv2", "--reach", "forward",
		"--hub-admin", "local", "--binary", r.wyLinux, "--yes", "--json")
	var sum struct {
		Machine  string `json:"machine"`
		Reach    string `json:"reach"`
		Port     int    `json:"port"`
		Verified bool   `json:"verified"`
		Keys     *struct {
			Device      string `json:"device"`
			SignFP      string `json:"sign_fp"`
			Reg         string `json:"reg"`
			OwnerSigned bool   `json:"owner_signed"`
			HubPin      string `json:"hub_pin"`
		} `json:"keys"`
		Warnings []string `json:"warnings"`
	}
	for _, ln := range strings.Split(out, "\n") {
		var w struct {
			Summary *json.RawMessage `json:"summary"`
		}
		if json.Unmarshal([]byte(ln), &w) == nil && w.Summary != nil {
			_ = json.Unmarshal(*w.Summary, &sum)
		}
	}
	if sum.Reach != "forward" || !sum.Verified || sum.Port == 0 {
		t.Fatalf("summary: reach %q verified %v port %d\n%s%s", sum.Reach, sum.Verified, sum.Port, out, errb)
	}
	if sum.Keys == nil || sum.Keys.Reg != "ssh" || sum.Keys.HubPin != "ssh" || sum.Keys.OwnerSigned || !strings.HasPrefix(sum.Keys.Device, "dev_") {
		t.Fatalf("keys step: %+v\n%s%s", sum.Keys, out, errb)
	}
	// No owner key on this operator, no terminal: the next step is named.
	if !strings.Contains(strings.Join(sum.Warnings, "\n"), "wy keys certify "+sum.Keys.Device) {
		t.Errorf("no certify next step in %q", sum.Warnings)
	}

	// The hub's view: the device's certificate is reg ssh with the server's keys.
	hk, _ := r.wyLocal("", "hub", "keys", "show", "--json")
	var show struct {
		HubKey struct {
			FP string `json:"fp"`
		} `json:"hub_key"`
	}
	if err := json.Unmarshal([]byte(hk), &show); err != nil || show.HubKey.FP == "" {
		t.Fatalf("hub keys show: %s %v", hk, err)
	}
	tok, _ := r.wyLocal("", "hub", "token", "create", "--kind", "desktop", "--machine", sum.Machine, "--name", "e2e-keys", "--json")
	var tk struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(tok), &tk); err != nil {
		t.Fatal(err)
	}
	var ki struct {
		Devices []struct {
			ID   string `json:"id"`
			Keys struct {
				SignFP string `json:"sign_fp"`
				Reg    string `json:"reg"`
			} `json:"keys"`
		} `json:"devices"`
	}
	r.api(tk.Token, "/v1/keys", &ki)
	found := false
	for _, d := range ki.Devices {
		if d.ID == sum.Keys.Device {
			found = d.Keys.Reg == "ssh" && d.Keys.SignFP == sum.Keys.SignFP
		}
	}
	if !found {
		t.Fatalf("hub does not list %s with reg ssh and sign %s: %+v", sum.Keys.Device, sum.Keys.SignFP, ki.Devices)
	}

	// The server pins the hub key over SSH, and through the forward its own token sees the
	// same certificate (wy keys status asks the hub).
	fwd := fmt.Sprintf("127.0.0.1:%d:%s", sum.Port, strings.TrimPrefix(r.hubBase, "http://"))
	st, errs, code := runCmd(r.env, "", "ssh", "-F", r.sshCfg, "-o", "ExitOnForwardFailure=yes", "-R", fwd, "srv", ".local/bin/wy", "keys", "status", "--json")
	if code != 0 {
		t.Fatalf("remote wy keys status: exit %d: %s", code, errs)
	}
	var ks struct {
		HubPin *struct {
			FP  string `json:"fp"`
			How string `json:"how"`
		} `json:"hub_pin"`
		Device    *string `json:"device"`
		SignFP    *string `json:"sign_fp"`
		Reg       *string `json:"reg"`
		Certified *bool   `json:"owner_signed"`
		HubError  *string `json:"hub_error"`
	}
	if err := json.Unmarshal([]byte(st), &ks); err != nil {
		t.Fatalf("remote keys status: %s %v", st, err)
	}
	if ks.HubPin == nil || ks.HubPin.How != "ssh" || ks.HubPin.FP != show.HubKey.FP {
		t.Errorf("remote hub pin %+v, want %s (ssh)", ks.HubPin, show.HubKey.FP)
	}
	if ks.Device == nil || *ks.Device != sum.Keys.Device || ks.SignFP == nil || *ks.SignFP != sum.Keys.SignFP {
		t.Errorf("remote device %v sign %v, want %s %s", ks.Device, ks.SignFP, sum.Keys.Device, sum.Keys.SignFP)
	}
	if ks.HubError != nil || ks.Reg == nil || *ks.Reg != "ssh" || ks.Certified == nil || *ks.Certified {
		t.Errorf("remote view of its certificate: reg %v owner_signed %v hub_error %v\n%s", ks.Reg, ks.Certified, ks.HubError, st)
	}

	// Again: idempotent, the same device and keys.
	out, errb = r.wyLocal("", "machine", "add", "srv", "--name", "srv2", "--reach", "forward",
		"--hub-admin", "local", "--binary", r.wyLinux, "--yes", "--json")
	if !strings.Contains(out, sum.Keys.SignFP) {
		t.Errorf("rerun registered other keys:\n%s%s", out, errb)
	}
}

// api GETs a hub path with a bearer token and decodes the JSON reply.
func (r *rig) api(token, path string, into any) {
	r.t.Helper()
	req, _ := http.NewRequest("GET", r.hubBase+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		r.t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, into); err != nil {
		r.t.Fatalf("GET %s: %v: %s", path, err, b)
	}
}
