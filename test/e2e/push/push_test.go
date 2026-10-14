// Package push is the phase 4 end-to-end test (push.md 11.4, docs/phases/4.md wave 2): the
// real `sy` binary, built with `-tags fakeclock`, runs `sy hub serve` in a temporary home
// with a fake clock (SY_FAKE_CLOCK control socket), a fake Web Push service (webpushtest)
// and an ntfy stub (ntfytest). The test pairs a phone through the API, registers a Web Push
// and an ntfy target, and ingests records as a machine:
//
//  1. a Stop that asks a question → no push at 59 s, one push at 60 s whose decrypted
//     payload matches push.md 6.5;
//
//  2. a question answered at 40 s → no push, the alert is cancelled;
//
//  3. three limit samples (50, 70, 85 %) → exactly one push (limit_80).
//
// expo_test.go adds phase 8's scenarios X1 to X8 (docs/phases/8.md): the same hub with a
// fake Expo push service (expotest) behind SY_TEST_EXPO_DIAL.
//
//	go test ./test/e2e/push            (skipped with -short and on Windows)
package push

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/expotest"
	"github.com/mraj602/switchyard/internal/hub/push/ntfytest"
	"github.com/mraj602/switchyard/internal/hub/push/webpushtest"
	"github.com/mraj602/switchyard/internal/record"
)

const (
	publicURL = "https://hub.e2e.test" // what the phone would open; never dialled
	pushHost  = "push.e2e.test"        // allow-listed fake push service host
)

// rig is one running hub.
type rig struct {
	t       *testing.T
	sy      string
	env     []string
	base    string // http://127.0.0.1:port
	clock   *http.Client
	hubLog  *syncBuf
	machine string
	mtoken  string
	wp      *webpushtest.Server
	nf      *ntfytest.Server
	ex      *expotest.Server
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

var (
	buildOnce sync.Once
	builtSy   string
	buildErr  error
	buildDir  string
)

// TestMain removes the shared sy build after the package's tests.
func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// buildSy builds sy with the fakeclock tag once per test binary.
func buildSy(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		out, err := exec.Command("go", "env", "GOMOD").Output()
		if err != nil {
			buildErr = err
			return
		}
		root := filepath.Dir(strings.TrimSpace(string(out)))
		if buildDir, buildErr = os.MkdirTemp("", "sye2e-bin"); buildErr != nil {
			return
		}
		bin := filepath.Join(buildDir, "sy")
		cmd := exec.Command("go", "build", "-tags", "fakeclock", "-o", bin, "./cmd/sy")
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("%v\n%s", err, b)
			return
		}
		builtSy = bin
	})
	if buildErr != nil {
		t.Fatalf("build sy: %v", buildErr)
	}
	return builtSy
}

// cleanEnv is the parent's environment without anything that could point sy or Claude
// Code at a real home, plus a temp HOME.
func cleanEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "SY_") || strings.HasPrefix(k, "XDG_") ||
			k == "HOME" || k == "TZ" || k == "NO_COLOR" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "TZ=UTC", "NO_COLOR=1")
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end: builds and runs sy")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake clock control socket is a unix socket")
	}
	tmp := t.TempDir()
	r := &rig{t: t, sy: buildSy(t), hubLog: &syncBuf{}}
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// unix socket paths are limited to ~104 bytes: keep it short.
	sockDir, err := os.MkdirTemp("", "sye2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "clock")

	r.wp = webpushtest.New()
	t.Cleanup(r.wp.Close)
	r.nf = ntfytest.New()
	t.Cleanup(r.nf.Close)
	r.ex = expotest.New() // the expo channel's default https://exp.host is dialled here instead
	t.Cleanup(r.ex.Close)
	wpURL, _ := url.Parse(r.wp.URL())

	r.env = append(cleanEnv(home), "SY_FAKE_CLOCK="+sock, "SY_TEST_WEBPUSH_DIAL="+wpURL.Host,
		"SY_TEST_EXPO_DIAL="+r.ex.Addr())
	r.clock = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}

	cmd := exec.Command(r.sy, "hub", "serve", "--listen", "127.0.0.1:0", "--public-url", publicURL)
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
		_ = cmd.Process.Signal(os.Interrupt) // graceful shutdown: drain, close, stop the engine
		var err error
		select {
		case err = <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			err = fmt.Errorf("no exit 20 s after SIGINT: %w", <-done)
		}
		if err != nil {
			t.Errorf("hub exit: %v\n%s", err, r.hubLog)
		}
		if !strings.Contains(r.hubLog.String(), "hub stopped") {
			t.Errorf("hub did not stop cleanly:\n%s", r.hubLog)
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
			line := sc.Text()
			fmt.Fprintln(r.hubLog, line)
			if m := listening.FindStringSubmatch(line); m != nil {
				addr <- m[1]
			}
		}
	}()
	select {
	case a := <-addr:
		r.base = "http://" + a
	case err := <-done:
		t.Fatalf("hub exited: %v\n%s", err, r.hubLog)
	case <-time.After(30 * time.Second):
		t.Fatalf("hub did not start:\n%s", r.hubLog)
	}
	return r
}

// run runs a sy command in the rig's home and returns stdout.
func (r *rig) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.sy, args...)
	cmd.Env = r.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("sy %s: %v\n%s%s", strings.Join(args, " "), err, out.String(), errb.String())
	}
	return out.String()
}

// call makes an API request; auth is "Bearer <token>" or "Cookie <cookie>".
func (r *rig) call(method, path, auth string, body any) (int, http.Header, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.base+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok, ok := strings.CutPrefix(auth, "Bearer "); ok {
		req.Header.Set("Authorization", "Bearer "+tok)
	} else if c, ok := strings.CutPrefix(auth, "Cookie "); ok {
		req.Header.Set("Cookie", c)
		req.Header.Set("Origin", publicURL) // cookie requests need the hub's own origin
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func (r *rig) must(want int, method, path, auth string, body any) []byte {
	r.t.Helper()
	code, _, b := r.call(method, path, auth, body)
	if code != want {
		r.t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, b)
	}
	return b
}

func (r *rig) clockCall(method, path string) time.Time {
	r.t.Helper()
	req, _ := http.NewRequest(method, "http://clock"+path, nil)
	resp, err := r.clock.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("clock %s: %d %s", path, resp.StatusCode, b)
	}
	at, err := time.Parse(time.RFC3339Nano, string(b))
	if err != nil {
		r.t.Fatal(err)
	}
	return at
}

func (r *rig) now() time.Time { return r.clockCall("GET", "/now") }
func (r *rig) sync()          { r.clockCall("POST", "/sync") }
func (r *rig) advance(d time.Duration) time.Time {
	return r.clockCall("POST", "/advance?d="+d.String())
}

// ingest sends one record as the machine, then lets the engine reconcile.
func (r *rig) ingest(rec *record.Record) {
	r.t.Helper()
	b, err := rec.Encode()
	if err != nil {
		r.t.Fatal(err)
	}
	out := r.must(200, "POST", "/v1/ingest", "Bearer "+r.mtoken,
		map[string]any{"v": 1, "client": "sy/e2e", "records": []json.RawMessage{b}})
	if bytes.Contains(out, []byte(`"rejected":[{`)) {
		r.t.Fatalf("rejected: %s", out)
	}
	r.sync()
}

var seat = &record.Seat{Account: "acc-e2e", Org: "org-e2e", Email: "e2e@example.com"}

func (r *rig) hook(sid, event string, fields map[string]any) {
	r.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + sid + ".jsonl"}
	for k, v := range fields {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	rec, err := record.NewHookEvent(record.Meta{Machine: r.machine, Slot: "~/.claude", Seat: seat, Client: "sy/e2e", Now: r.now()},
		raw, record.HookOptions{LastMessages: true})
	if err != nil {
		r.t.Fatal(err)
	}
	r.ingest(rec)
}

func (r *rig) ask(sid string) {
	r.hook(sid, "Stop", map[string]any{"last_assistant_message": "Done with the schema.\n\nShould I also update the migration?"})
}

func (r *rig) limit(pct float64, resets time.Time) {
	r.t.Helper()
	raw, _ := json.Marshal(map[string]any{"session_id": "", "rate_limits": map[string]any{
		"five_hour": map[string]any{"used_percentage": pct, "resets_at": resets.Unix()}}})
	rec, err := record.NewLimitSample(record.Meta{Machine: r.machine, Slot: "~/.claude", Seat: seat, Client: "sy/e2e", Now: r.now()}, raw)
	if err != nil {
		r.t.Fatal(err)
	}
	r.ingest(rec)
}

// pushes waits a little and returns what the fake services received so far.
func (r *rig) settle() (wp []webpushtest.Received, nf []ntfytest.Received) {
	time.Sleep(500 * time.Millisecond)
	return r.wp.Requests(), r.nf.Requests()
}

// expectPush waits for the n-th web push and ntfy message and returns the decrypted payload.
func (r *rig) expectPush(n int) map[string]any {
	r.t.Helper()
	if !r.wp.Wait(n, 15*time.Second) {
		r.t.Fatalf("web push %d did not arrive (got %d)", n, len(r.wp.Requests()))
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(r.nf.Requests()) < n && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := len(r.nf.Requests()); got < n {
		r.t.Fatalf("ntfy message %d did not arrive (got %d)", n, got)
	}
	got := r.wp.Requests()[n-1]
	if got.VapidErr != nil || got.DecodeErr != nil {
		r.t.Fatalf("web push %d: vapid %v, decode %v", n, got.VapidErr, got.DecodeErr)
	}
	var p map[string]any
	if err := json.Unmarshal(got.Plain, &p); err != nil {
		r.t.Fatalf("payload: %v: %s", err, got.Plain)
	}
	if m := r.nf.Requests()[n-1]; m.Err != nil {
		r.t.Fatalf("ntfy %d: %v", n, m.Err)
	}
	return p
}

func TestEndToEndPush(t *testing.T) { phase4(t, newRig(t), false) }

// phase4 is the phase 4 scenario. With withExpo the phone also registers an app (expo)
// target, and every push must reach it too, without changing a byte of what Web Push and
// ntfy get (X8).
func phase4(t *testing.T, r *rig, withExpo bool) {

	if code, _, b := r.call("GET", "/health", "", nil); code != 200 || !bytes.Contains(b, []byte(`"ui":"`)) {
		t.Fatalf("/health: %d %s", code, b)
	}

	// ---- owner: a one-time link from the hub's shell, swapped for an owner session.
	var link struct{ Secret string }
	if err := json.Unmarshal([]byte(r.run("hub", "owner-link", "--json")), &link); err != nil || link.Secret == "" {
		t.Fatalf("owner-link: %v %+v", err, link)
	}
	_, hdr, b := r.call("POST", "/v1/session/owner", "", map[string]any{"secret": link.Secret})
	var owner string
	for _, c := range (&http.Response{Header: hdr}).Cookies() {
		if c.Name == "sy_session" {
			owner = "Cookie sy_session=" + c.Value
		}
	}
	if owner == "" {
		t.Fatalf("no owner cookie: %v %s", hdr, b)
	}
	// Allow the fake push service's host (push.md 6.6).
	r.must(200, "PUT", "/v1/push/settings", owner, map[string]any{"webpush_hosts": []string{pushHost}})

	// ---- pair a phone (pairing.md 3): the owner makes a code, the phone claims it.
	var code struct{ Code, Secret, URL string }
	if err := json.Unmarshal(r.must(201, "POST", "/v1/pair/codes", owner, map[string]any{"kind": "phone", "name": "Pixel"}), &code); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code.URL, publicURL+"/#/pair?") {
		t.Errorf("pair URL %q is not on the public URL", code.URL)
	}
	var claim struct{ Device, Token, Kind string }
	if err := json.Unmarshal(r.must(200, "POST", "/v1/pair/claim", "", map[string]any{"code": code.Code, "name": "Pixel"}), &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Kind != "phone" || claim.Token == "" {
		t.Fatalf("claim: %+v", claim)
	}
	phone := "Bearer " + claim.Token

	// ---- register a Web Push target as the phone and an ntfy target as the owner.
	var cfg struct {
		Key string `json:"vapid_public_key"`
	}
	_ = json.Unmarshal(r.must(200, "GET", "/v1/push/config", phone, nil), &cfg)
	if cfg.Key == "" {
		t.Fatal("no VAPID key")
	}
	r.wp.ExpectVapidKey(cfg.Key)
	sub := r.wp.NewSubscription()
	endpoint := "https://" + pushHost + strings.TrimPrefix(sub.Endpoint, r.wp.URL())
	r.must(201, "POST", "/v1/push/targets", phone, map[string]any{"kind": "webpush", "endpoint": endpoint,
		"keys": map[string]string{"p256dh": sub.P256dh, "auth": sub.Auth}, "name": "Pixel"})
	// The ntfy stub is on loopback, which only the owner may register (push.md 7.1).
	r.must(201, "POST", "/v1/push/targets", owner, map[string]any{"kind": "ntfy", "server": r.nf.URL(), "topic": "sy-e2e-topic"})
	expoTrigger := func(int, string) {}
	if withExpo {
		r.must(201, "POST", "/v1/push/targets", phone, map[string]any{"kind": "expo", "expo_token": expoToken1,
			"platform": "ios", "app": map[string]string{"version": "0.8.0", "build": "1"}})
		expoTrigger = func(n int, trigger string) {
			t.Helper()
			msg := r.expectExpo(n)
			if msg.Data["trigger"] != trigger {
				t.Errorf("expo message %d trigger %v, want %s", n, msg.Data["trigger"], trigger)
			}
		}
	}

	// ---- a machine token from the hub's shell.
	r.machine = "m_" + record.NewID(time.Now())
	var tok struct{ Token string }
	if err := json.Unmarshal([]byte(r.run("hub", "token", "create", "--kind", "machine", "--name", "laptop", "--machine", r.machine, "--json")), &tok); err != nil {
		t.Fatal(err)
	}
	r.mtoken = tok.Token

	// ---- 1. a question → a push at 60 s, not before.
	const s1 = "c41e0000-0000-4000-8000-000000000001"
	asked := r.now()
	r.ask(s1)
	r.advance(59 * time.Second)
	if wp, nf := r.settle(); len(wp)+len(nf) != 0 {
		t.Fatalf("pushed before 60 s: %d web, %d ntfy", len(wp), len(nf))
	}
	at := r.advance(time.Second)
	p := r.expectPush(1)
	expoTrigger(1, "asked")
	if d := at.Sub(asked); d != 60*time.Second {
		t.Errorf("pushed after %s", d)
	}
	for k, want := range map[string]any{"v": 1.0, "type": "alert", "trigger": "asked", "session": s1,
		"tag": "sy-s-" + s1, "body": "Should I also update the migration?", "private": false} {
		if p[k] != want {
			t.Errorf("payload %s = %v, want %v", k, p[k], want)
		}
	}
	if title, _ := p["title"].(string); !strings.Contains(title, "asked a question") {
		t.Errorf("title %q", title)
	}
	alertID, _ := p["alert"].(string)
	if !strings.HasPrefix(alertID, "al_") || !strings.HasPrefix(fmt.Sprint(p["url"]), "/#/sessions/"+s1) {
		t.Errorf("alert %q url %v", alertID, p["url"])
	}
	if acts, _ := p["actions"].(map[string]any); acts["snooze"] == nil || acts["seen"] == nil {
		t.Errorf("actions %v", p["actions"])
	}
	for _, k := range []string{"alerts", "needs_you", "tap", "ts"} {
		if _, ok := p[k]; !ok {
			t.Errorf("payload has no %s", k)
		}
	}
	if m := r.nf.Requests()[0].Msg; m.Topic != "sy-e2e-topic" || !strings.Contains(m.Message, "update the migration") {
		t.Errorf("ntfy message %+v", m)
	}
	nWP, nNF := 1, 1

	// ---- 2. a question answered at 40 s → nothing, the alert is cancelled.
	const s2 = "c41e0000-0000-4000-8000-000000000002"
	r.ask(s2)
	r.advance(40 * time.Second)
	r.hook(s2, "UserPromptSubmit", map[string]any{"prompt": "yes"})
	r.advance(2 * time.Minute)
	if wp, nf := r.settle(); len(wp) != nWP || len(nf) != nNF {
		t.Fatalf("answered at 40 s but pushed: %d web, %d ntfy", len(wp)-nWP, len(nf)-nNF)
	}
	var list struct {
		Alerts []struct {
			ID, Trigger, Status, Session string
		} `json:"alerts"`
	}
	if err := json.Unmarshal(r.must(200, "GET", "/v1/alerts", phone, nil), &list); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, a := range list.Alerts {
		statuses[a.Session+"/"+a.Trigger] = a.Status
	}
	if statuses[s2+"/asked"] != "cancelled" {
		t.Errorf("alert of the answered question: %q (all %v)", statuses[s2+"/asked"], statuses)
	}
	if statuses[s1+"/asked"] != "fired" {
		t.Errorf("alert of the first question: %q", statuses[s1+"/asked"])
	}

	// ---- 3. crossing 80 % with three samples → exactly one push.
	resets := r.now().Add(3 * time.Hour)
	for _, pct := range []float64{50, 70, 85} {
		r.limit(pct, resets)
		r.advance(time.Minute)
	}
	p = r.expectPush(nWP + 1)
	expoTrigger(2, "limit_80")
	if p["trigger"] != "limit_80" {
		t.Errorf("limit push trigger %v", p["trigger"])
	}
	r.limit(88, resets) // still over 80 %, under 95 %: the same episode
	r.advance(time.Minute)
	if wp, nf := r.settle(); len(wp) != nWP+1 || len(nf) != nNF+1 {
		t.Errorf("crossing 80%% sent %d web pushes and %d ntfy messages, want exactly 1 each", len(wp)-nWP, len(nf)-nNF)
	}
}
