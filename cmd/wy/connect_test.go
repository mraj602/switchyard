package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/timer"
)

const goodToken = "wy_m_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"

type cmdHub struct {
	mu     sync.Mutex
	got    map[string]bool
	status int // when non-zero, ingest answers this
	srv    *httptest.Server
}

func newCmdHub(t *testing.T) *cmdHub {
	h := &cmdHub{got: map[string]bool{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"ok":true,"version":"0.2.0","schema":1}`)
		case "/v1/ingest":
			if r.Header.Get("Authorization") != "Bearer "+goodToken {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":"no","code":"unauthorized"}`)
				return
			}
			var b struct {
				Records []struct {
					ID string `json:"id"`
				} `json:"records"`
			}
			var body io.Reader = r.Body
			if r.Header.Get("Content-Encoding") == "gzip" {
				zr, err := gzip.NewReader(r.Body)
				if err != nil {
					w.WriteHeader(400)
					return
				}
				body = zr
			}
			_ = json.NewDecoder(body).Decode(&b)
			if len(b.Records) == 0 {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"no records","code":"bad_request"}`)
				return
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.status != 0 {
				w.WriteHeader(h.status)
				return
			}
			acc := []string{}
			for _, rec := range b.Records {
				h.got[rec.ID] = true
				acc = append(acc, rec.ID)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": acc, "rejected": []any{}})
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

type recRunner struct{ calls []string }

func (r *recRunner) Run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return []byte("Linger=yes"), nil
}

// sandbox points every wy folder and the fake home at temp dirs.
func sandbox(t *testing.T) (connectDeps, *recRunner, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("WY_HOME", filepath.Join(root, "wy"))
	t.Setenv("WY_CONFIG_DIR", "")
	t.Setenv("WY_STATE_DIR", "")
	t.Setenv("WY_DATA_DIR", "")
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	r := &recRunner{}
	return connectDeps{
		dirs:   d,
		budget: 5 * time.Second,
		timer: timer.Env{GOOS: "linux", Home: home, UID: 1000, User: "tester", WyPath: "/usr/local/bin/wy",
			StateDir: d.State.Path, Getenv: func(string) string { return "" }, Runner: r},
	}, r, home
}

func seedOutbox(t *testing.T, state string, n int) []string {
	t.Helper()
	dir := filepath.Join(state, "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("01JA2XQ8R7Y5V0Z6M3K1T%05d", i)
		ids = append(ids, id)
		fmt.Fprintf(&sb, `{"v":1,"id":%q,"kind":"hook_event","ts":"%sT10:00:00.000Z","machine":"m_x","payload":{}}`+"\n", id, seedDay())
	}
	if err := os.WriteFile(filepath.Join(dir, seedDay()+".jsonl"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return ids
}

// seedDay is the day file seeded records go in: today (UTC), so the outbox's age-based
// pruning (outbox.Prune) never removes it; a fixed date made the test fail once it aged.
func seedDay() string { return time.Now().UTC().Format("2006-01-02") }

// appendOutbox appends n hook_event records to today's day file through the outbox.
func appendOutbox(t *testing.T, state string, n int) []string {
	t.Helper()
	ob := outbox.New(filepath.Join(state, "outbox"))
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("01JA2XQ8R7Y5V0Z6M3K2T%05d", i)
		ids = append(ids, id)
		line := fmt.Sprintf(`{"v":1,"id":%q,"kind":"hook_event","ts":"2026-10-12T10:00:00.000Z","machine":"m_x","payload":{}}`+"\n", id)
		if _, err := ob.AppendLine([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func TestFlushWithoutHubExitsFast(t *testing.T) {
	_, _, _ = sandbox(t)
	d, _ := paths.Resolve(paths.FromOS())
	var out, errb bytes.Buffer
	start := time.Now()
	if err := runFlush([]string{"--quiet"}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("took %v", time.Since(start))
	}
	if out.Len() != 0 || errb.Len() != 0 {
		t.Errorf("--quiet printed: %q %q", out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(d.State.Path, "outbox")); err == nil {
		t.Error("flush without a hub must not create the outbox folder")
	}
	out.Reset()
	if err := runFlush(nil, &out, &errb); err != nil || !strings.Contains(out.String(), "No hub configured") {
		t.Errorf("by hand: %v %q", err, out.String())
	}
}

func TestConnectHappyPath(t *testing.T) {
	deps, runner, home := sandbox(t)
	ids := seedOutbox(t, deps.dirs.State.Path, 3)
	hub := newCmdHub(t)
	var out, errb bytes.Buffer
	deps.timer.Out = &out
	// Flags after the URL, as in the README.
	if err := connect(context.Background(), deps, []string{hub.srv.URL, "--token", goodToken, "--insecure-lan"}, &out, &errb); err != nil {
		t.Fatalf("%v\n%s\n%s", err, out.String(), errb.String())
	}
	tp := filepath.Join(deps.dirs.State.Path, "token")
	b, err := os.ReadFile(tp)
	if err != nil || strings.TrimSpace(string(b)) != goodToken {
		t.Fatalf("token file: %q %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(tp); st.Mode().Perm() != 0o600 {
			t.Errorf("token mode %v", st.Mode().Perm())
		}
	}
	cfg, _ := os.ReadFile(filepath.Join(deps.dirs.Config.Path, "config.toml"))
	if !strings.Contains(string(cfg), hub.srv.URL) {
		t.Errorf("hub.url not stored:\n%s", cfg)
	}
	for _, id := range ids {
		if !hub.got[id] {
			t.Errorf("record %s not delivered by the first flush", id)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "watchyard-flush.timer")); err != nil {
		t.Errorf("timer not installed in the fake home: %v", err)
	}
	if len(runner.calls) == 0 || !strings.HasPrefix(runner.calls[0], "systemctl --user") {
		t.Errorf("calls %v", runner.calls)
	}

	// disconnect undoes it, and keeps the outbox.
	out.Reset()
	if err := disconnect(deps, nil, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tp); !os.IsNotExist(err) {
		t.Error("token not removed")
	}
	cfg, _ = os.ReadFile(filepath.Join(deps.dirs.Config.Path, "config.toml"))
	if strings.Contains(string(cfg), hub.srv.URL) {
		t.Errorf("hub.url not cleared:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "watchyard-flush.timer")); !os.IsNotExist(err) {
		t.Error("timer file not removed")
	}
	if _, err := os.Stat(filepath.Join(deps.dirs.State.Path, "outbox", seedDay()+".jsonl")); err != nil {
		t.Error("outbox must survive disconnect")
	}
}

func TestConnectTokenFromStdin(t *testing.T) {
	deps, _, _ := sandbox(t)
	hub := newCmdHub(t)
	deps.stdin = strings.NewReader(goodToken + "\n")
	var out, errb bytes.Buffer
	if err := connect(context.Background(), deps, []string{"--token-stdin", "--no-timer", "--insecure-lan", hub.srv.URL}, &out, &errb); err != nil {
		t.Fatalf("%v %s", err, errb.String())
	}
	if b, _ := os.ReadFile(filepath.Join(deps.dirs.State.Path, "token")); strings.TrimSpace(string(b)) != goodToken {
		t.Errorf("token %q", b)
	}
}

func TestConnectRefusals(t *testing.T) {
	deps, runner, _ := sandbox(t)
	hub := newCmdHub(t)
	var out, errb bytes.Buffer
	ctx := context.Background()

	// Wrong token: nothing is stored, no timer.
	err := connect(ctx, deps, []string{hub.srv.URL, "--token", "wy_m_wrong", "--insecure-lan"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("bad token: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deps.dirs.State.Path, "token")); err == nil {
		t.Error("a rejected token must not be stored")
	}
	if _, err := os.Stat(filepath.Join(deps.dirs.Config.Path, "config.toml")); err == nil {
		t.Error("config written for a failed connect")
	}
	// Plain http to a LAN host without the flag.
	if err := connect(ctx, deps, []string{"http://192.168.50.20:8787", "--token", goodToken}, &out, &errb); err == nil {
		t.Error("plain http to a LAN host accepted")
	}
	// Nothing listening.
	if err := connect(ctx, deps, []string{"http://127.0.0.1:1", "--token", goodToken}, &out, &errb); err == nil {
		t.Error("dead hub accepted")
	}
	// Usage errors.
	if err := connect(ctx, deps, []string{hub.srv.URL}, &out, &errb); err == nil {
		t.Error("no token accepted")
	}
	if err := connect(ctx, deps, []string{hub.srv.URL, "--token", goodToken, "--token-stdin"}, &out, &errb); err == nil {
		t.Error("both token flags accepted")
	}
	if len(runner.calls) != 0 {
		t.Errorf("failed connects ran %v", runner.calls)
	}
}

func TestFlushCommandAgainstDownHub(t *testing.T) {
	deps, _, _ := sandbox(t)
	hub := newCmdHub(t)
	var out, errb bytes.Buffer
	if err := connect(context.Background(), deps, []string{hub.srv.URL, "--token", goodToken, "--insecure-lan", "--no-timer"}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	// After connect the cursor is in today's file (connect queued machine_info), so these
	// are appended there rather than written to an older day file.
	ids := appendOutbox(t, deps.dirs.State.Path, 2)
	hub.mu.Lock()
	hub.status = 503
	hub.mu.Unlock()

	out.Reset()
	if err := runFlush([]string{"--quiet"}, &out, &errb); err != nil || out.Len() != 0 {
		t.Fatalf("quiet flush must succeed silently: %v %q", err, out.String())
	}
	if err := runFlush(nil, &out, &errb); err == nil {
		t.Fatal("by hand, a failing hub is an error")
	}
	hub.mu.Lock()
	hub.status = 0
	hub.mu.Unlock()
	out.Reset()
	if err := runFlush(nil, &out, &errb); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if !hub.got[id] {
			t.Errorf("record %s missing", id)
		}
	}
}
