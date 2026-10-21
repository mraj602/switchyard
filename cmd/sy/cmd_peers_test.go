//go:build fakeinbox && peerstest

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/identity"
	peerspkg "github.com/mraj602/switchyard/internal/peers"
	"github.com/mraj602/switchyard/internal/peers/fakeinbox"
)

// peersWorld is a temp HOME with two accounts, a fake /proc, and two fake inboxes in a temp
// runtime dir. The real /run/user/*/cc-socks is never looked at.
type peersWorld struct {
	t           *testing.T
	base        string
	home, state string
	runtime     string
	procRoot    string
	a, b        *fakeinbox.Server
	noHubWiden  bool
	startPID    int
	enabled     bool
}

func peersWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (w *peersWorld) proc(pid, ppid int, comm, exe, env string, start int) {
	d := filepath.Join(w.procRoot, fmt.Sprint(pid))
	peersWriteFile(w.t, filepath.Join(d, "stat"), fmt.Sprintf("%d (%s) S %d %s%d 0 0\n", pid, comm, ppid, strings.Repeat("0 ", 17), start))
	peersWriteFile(w.t, filepath.Join(d, "comm"), comm+"\n")
	peersWriteFile(w.t, filepath.Join(d, "cmdline"), comm+"\x00")
	peersWriteFile(w.t, filepath.Join(d, "environ"), env+"\x00")
	if exe != "" {
		if err := os.Symlink(exe, filepath.Join(d, "exe")); err != nil {
			w.t.Fatal(err)
		}
	}
}

func newPeersWorld(t *testing.T) *peersWorld { return newPeersWorldAt(t, "") }

// newPeersWorldAt is newPeersWorld with the state folder of another fixture (the signed
// matrix tests share it with the keys rig); with a state folder the unsigned test allow file
// is not used and the matrix is the signed document in <state>/keys/matrix.json.
func newPeersWorldAt(t *testing.T, state string) *peersWorld {
	base, err := os.MkdirTemp("", "sypc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	signed := state != ""
	if state == "" {
		state = filepath.Join(base, "s")
	}
	w := &peersWorld{t: t, base: base, home: filepath.Join(base, "h"), state: state,
		runtime: filepath.Join(base, "r"), procRoot: filepath.Join(base, "p"), enabled: true, startPID: 501}
	os.MkdirAll(filepath.Join(w.runtime, "cc-socks"), 0o700)
	slotA, slotB := filepath.Join(w.home, ".claude-2"), filepath.Join(w.home, ".claude-5")
	for slot, acc := range map[string]string{slotA: "acc-work", slotB: "acc-personal"} {
		peersWriteFile(t, filepath.Join(slot, ".claude.json"), fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":"o"}}`, acc))
		os.MkdirAll(filepath.Join(slot, "sessions"), 0o700)
	}
	sockA, sockB := filepath.Join(w.runtime, "cc-socks", "500.sock"), filepath.Join(w.runtime, "cc-socks", "600.sock")
	reg := func(slot string, pid int, id, name, sock string, start int) {
		b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id, "cwd": filepath.Join(w.home, name), "procStart": fmt.Sprint(start),
			"messagingSocketPath": sock, "name": name, "status": "idle"})
		peersWriteFile(t, filepath.Join(slot, "sessions", fmt.Sprint(pid)+".json"), string(b))
	}
	reg(slotA, 500, "sess-a", "api", sockA, 4242)
	reg(slotB, 600, "sess-b", "worker", sockB, 5252)
	w.proc(500, 1, "claude", "/x/claude/versions/2.1.300", "CLAUDE_CONFIG_DIR="+slotA, 4242)
	w.proc(600, 1, "claude", "/x/claude/versions/2.1.300", "CLAUDE_CONFIG_DIR="+slotB, 5252)
	w.proc(501, 500, "bash", "/usr/bin/bash", "CLAUDECODE=1", 4300)
	w.proc(700, 1, "bash", "/usr/bin/bash", "CLAUDECODE=1", 1) // a terminal's shell, not under claude
	for _, id := range []string{"sess-a", "sess-b"} {
		peersWriteFile(t, filepath.Join(w.state, "peers", "sessions", id+".json"),
			fmt.Sprintf(`{"v":1,"session":%q,"pid":1,"config_dir":"x","mode":"bypassPermissions","mode_at":null,"human_at":null,"ended_at":null}`, id))
	}
	srvA, err := fakeinbox.Listen(fakeinbox.Config{Path: sockA, SessionID: "sess-a", Mode: peerspkg.ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	w.b, err = fakeinbox.Listen(fakeinbox.Config{Path: sockB, SessionID: "sess-b", Mode: peerspkg.ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	w.a = srvA
	t.Cleanup(func() { srvA.Close(); w.b.Close() })
	allow := filepath.Join(base, "allow.json")
	if signed {
		allow = ""
	} else {
		peersWriteFile(t, allow, `{"same_account":true,"max_turns":30,"allow":[["acc-work","acc-personal"]]}`)
	}
	t.Setenv(peerspkg.TestAllowEnv, allow)

	old := peersEnv
	t.Cleanup(func() { peersEnv = old })
	peersEnv = func() (peerEnv, error) {
		return peerEnv{Enabled: w.enabled, NoHubWiden: w.noHubWiden, Env: peerspkg.Env{
			Home: w.home, State: w.state, Runtime: w.runtime,
			Machine:    identity.Machine{ID: "m_T", Name: "laptop"},
			SlotLabels: map[string]string{"~/.claude-2": "work", "~/.claude-5": "personal"},
			Proc:       peerspkg.LinuxProc{Root: w.procRoot}, StartPID: w.startPID, FS: identity.OSFS{},
			Now: time.Now,
		}}, nil
	}
	return w
}

func runPeersSy(args ...string) (code int, out, errs string) {
	var o, e bytes.Buffer
	code = dispatch(args, &o, &e)
	return code, o.String(), e.String()
}

func TestPeersSendCLI(t *testing.T) {
	w := newPeersWorld(t)
	code, out, errs := runPeersSy("peers", "send", "worker", "please run", "the tests")
	if code != 0 || !strings.HasPrefix(out, "delivered to worker@personal.laptop (msg ") || errs != "" {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
	if !w.b.Wait(1, 2*time.Second) {
		t.Fatal("nothing delivered")
	}
	g := w.b.All()[0]
	if g.Outcome != fakeinbox.Deliver || !strings.HasSuffix(g.Wrapper.Body, "\n\nplease run the tests") {
		t.Errorf("%s %q", g.Outcome, g.Wrapper.Body)
	}

	// Flags after the positionals, and --stdin with a heredoc-style body.
	code, out, _ = runPeersSy("peers", "send", "@worker", "second", "--wait", "5", "--ttl", "30m")
	if code != 0 {
		t.Fatalf("flags after positionals: %d %s", code, out)
	}
}

func TestPeersSendStdin(t *testing.T) {
	w := newPeersWorld(t)
	r, wr, _ := os.Pipe()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	go func() { wr.WriteString("line one\nwith \"quotes\" and $HOME and `ticks`\n"); wr.Close() }()
	code, out, errs := runPeersSy("peers", "send", "worker", "--stdin")
	if code != 0 {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	w.b.Wait(1, 2*time.Second)
	if body := w.b.All()[0].Wrapper.Body; !strings.HasSuffix(body, "line one\nwith \"quotes\" and $HOME and `ticks`\n") {
		t.Errorf("body %q", body)
	}
}

// sendStdin is `sy peers send <to> --stdin` with text on standard input.
func sendStdin(t *testing.T, to, text string) (int, string, string) {
	t.Helper()
	r, wr, _ := os.Pipe()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	go func() { wr.WriteString(text); wr.Close() }()
	return runPeersSy("peers", "send", to, "--stdin")
}

// --stdin takes 64 KiB of multi-byte text whole, refuses more as too_big (exit 6) even when
// the input is far larger than the cap and the cut would fall inside a character, and keeps
// Unicode as it is (CRLF becomes LF).
func TestPeersSendStdinLimits(t *testing.T) {
	w := newPeersWorld(t)
	full := strings.Repeat("é", peerspkg.MaxTextBytes/2)
	if code, out, errs := sendStdin(t, "worker", full); code != 0 {
		t.Fatalf("64 KiB: %d %q %q", code, out, errs)
	}
	for _, text := range []string{full + "é", strings.Repeat("日", 3*peerspkg.MaxTextBytes) + "x"} {
		code, out, _ := sendStdin(t, "worker", text)
		if code != 6 || !strings.HasPrefix(out, "not delivered: ") || !strings.Contains(out, "limit is 65536") {
			t.Errorf("%d bytes: %d %q", len(text), code, out)
		}
	}
	if code, out, _ := sendStdin(t, "worker", "héllo 👋🏽 日本語\r\nzwei\n"); code != 0 {
		t.Fatalf("unicode: %d %q", code, out)
	}
	if !w.b.Wait(2, 2*time.Second) {
		t.Fatal("not delivered")
	}
	got := w.b.All()
	if len(got) != 2 || !strings.HasSuffix(got[0].Wrapper.Body, "\n\n"+full) || !strings.HasSuffix(got[1].Wrapper.Body, "\n\nhéllo 👋🏽 日本語\nzwei\n") {
		t.Errorf("%d frames; last body %q", len(got), got[len(got)-1].Wrapper.Body)
	}
}

func TestPeersSendExitCodes(t *testing.T) {
	w := newPeersWorld(t)
	// Not allowed: exit 5, one line on stdout.
	allow := filepath.Join(w.base, "allow.json")
	peersWriteFile(t, allow, `{"same_account":true,"allow":[["acc-personal","acc-work"]]}`)
	code, out, _ := runPeersSy("peers", "send", "worker", "hi")
	if code != 5 || !strings.HasPrefix(out, "not delivered: the matrix does not allow work → personal. Ask the owner to change it; do not retry.") || strings.Count(out, "\n") != 1 {
		t.Errorf("not_allowed: %d %q", code, out)
	}
	// Limits: exit 6.
	peersWriteFile(t, allow, `{"allow":[["acc-work","acc-personal"]]}`)
	if code, out, _ = runPeersSy("peers", "send", "worker", "ghp_"+strings.Repeat("a", 36)); code != 6 {
		t.Errorf("secret: %d %q", code, out)
	}
	// Unknown target: 7.
	if code, out, _ = runPeersSy("peers", "send", "nobody", "hi"); code != 7 {
		t.Errorf("unknown: %d %q", code, out)
	}
	// Usage: 2.
	for _, a := range [][]string{{"peers", "send"}, {"peers", "send", "worker"}, {"peers", "send", "worker", "x", "--wait", "99"}, {"peers", "send", "worker", "x", "--ttl", "10s"}, {"peers", "nonsense"}} {
		if code, _, _ = runPeersSy(a...); code != 2 {
			t.Errorf("%v: exit %d, want 2", a, code)
		}
	}
	// Not inside a session: 4, on stderr.
	w.startPID = 700
	code, out, errs := runPeersSy("peers", "send", "worker", "hi")
	if code != 4 || out != "" || !strings.Contains(errs, "only inside a Claude Code session") {
		t.Errorf("terminal send: %d %q %q", code, out, errs)
	}
	// Not enabled: 5.
	w.startPID, w.enabled = 501, false
	if code, out, _ = runPeersSy("peers", "send", "worker", "hi"); code != 5 || !strings.Contains(out, "not enabled") || !strings.Contains(out, "sy peers enable.") {
		t.Errorf("disabled: %d %q", code, out)
	}
	if len(w.b.All()) != 0 {
		t.Errorf("refusals reached the inbox: %d", len(w.b.All()))
	}
}

func TestPeersOffOn(t *testing.T) {
	w := newPeersWorld(t)
	w.startPID = 700 // a terminal
	if code, _, _ := runPeersSy("peers", "off"); code != 0 {
		t.Fatal(code)
	}
	w.startPID = 501
	if code, out, _ := runPeersSy("peers", "send", "worker", "hi"); code != 5 || !strings.Contains(out, "off on this machine") {
		t.Errorf("off: %d %q", code, out)
	}
	// Inside a session neither switch works (peers.md 2.3: terminal only); in particular a
	// session cannot turn the machine's messaging back on.
	for _, sub := range []string{"on", "off"} {
		if code, _, errs := runPeersSy("peers", sub); code != 4 || !strings.Contains(errs, "at a terminal") {
			t.Errorf("%s inside a session: %d %q", sub, code, errs)
		}
	}
	if code, _, _ := runPeersSy("peers", "send", "worker", "hi"); code != 5 {
		t.Errorf("still off after a session's on: %d", code)
	}
	w.startPID = 700
	if code, _, _ := runPeersSy("peers", "on"); code != 0 {
		t.Fatal(code)
	}
	w.startPID = 501
	if code, _, _ := runPeersSy("peers", "send", "worker", "hi"); code != 0 {
		t.Errorf("after on: %d", code)
	}
}

func TestPeersListCLI(t *testing.T) {
	w := newPeersWorld(t)
	code, out, errs := runPeersSy("peers", "list", "--json")
	if code != 0 || errs != "" {
		t.Fatalf("%d %q", code, errs)
	}
	var got []peerspkg.ListEntry
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 || got[0].Handle != "worker@personal.laptop" || got[0].Can != "start" {
		t.Fatalf("%v %s", err, out)
	}
	code, out, _ = runPeersSy("peers", "list")
	if code != 0 || !strings.Contains(out, "HANDLE") || !strings.Contains(out, "worker@personal.laptop") || !strings.Contains(out, "start") {
		t.Errorf("table: %d %q", code, out)
	}
	// A terminal needs --all.
	w.startPID = 700
	if code, _, errs = runPeersSy("peers", "list"); code != 4 || !strings.Contains(errs, "--all") {
		t.Errorf("terminal list: %d %q", code, errs)
	}
	code, out, _ = runPeersSy("peers", "list", "--all", "--json")
	var all []peerspkg.ListEntry
	if code != 0 || json.Unmarshal([]byte(out), &all) != nil || len(all) != 2 || all[0].Can != "none" {
		t.Errorf("terminal --all: %d %s", code, out)
	}
	// No matrix: the filtered list is empty and says why.
	t.Setenv(peerspkg.TestAllowEnv, filepath.Join(w.base, "nope.json"))
	w.startPID = 501
	code, out, _ = runPeersSy("peers", "list")
	if code != 0 || !strings.Contains(out, "matrix is not configured") {
		t.Errorf("no matrix: %d %q", code, out)
	}
	code, out, _ = runPeersSy("peers", "list", "--json")
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty json: %q", out)
	}
}
