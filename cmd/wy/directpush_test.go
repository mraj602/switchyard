package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func directPushHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WY_HOME", home)
	t.Setenv("HOME", home) // never a real home or CLAUDE_CONFIG_DIR
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, v := range []string{"WY_CONFIG_DIR", "WY_STATE_DIR", "WY_DATA_DIR"} {
		t.Setenv(v, "")
	}
	return home
}

func runDP(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	old := directPushStdin
	directPushStdin = strings.NewReader(stdin)
	defer func() { directPushStdin = old }()
	var out, errb bytes.Buffer
	code := dispatch(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestDirectPushOnStatusTestOff(t *testing.T) {
	home := directPushHome(t)
	var mu sync.Mutex
	var got []map[string]any
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}))
	defer ntfy.Close()

	// No topic and no terminal: refused, nothing written.
	if code, _, errs := runDP(t, "", "direct-push", "on"); code == 0 || !strings.Contains(errs, "--secret-stdin") {
		t.Fatalf("on without a topic: %d %s", code, errs)
	}
	// A topic as an argument is refused (it is the secret).
	if code, _, _ := runDP(t, "", "direct-push", "on", "wy-topic"); code != 2 {
		t.Fatalf("topic argument: %d", code)
	}
	if code, _, errs := runDP(t, `{"topic":"bad topic"}`, "direct-push", "on", "--secret-stdin"); code == 0 {
		t.Fatalf("bad topic accepted: %s", errs)
	}
	code, out, errs := runDP(t, `{"topic":"wy-SecretTopic123","token":"tk_secret"}`, "direct-push", "on", "--secret-stdin",
		"--server", ntfy.URL, "--private=false", "--after", "3m")
	if code != 0 {
		t.Fatalf("on: %d %s %s", code, out, errs)
	}
	cfg, err := os.ReadFile(filepath.Join(home, "config", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "SecretTopic") || strings.Contains(string(cfg), "tk_secret") || !strings.Contains(string(cfg), "after_s = 180") {
		t.Fatalf("config.toml: %s", cfg)
	}
	sec := filepath.Join(home, "state", "push", "direct.json")
	fi, err := os.Stat(sec)
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("direct.json: %v %v", fi, err)
	}
	if strings.Contains(out, "SecretTopic") {
		t.Fatalf("topic printed: %s", out)
	}

	code, out, errs = runDP(t, "", "direct-push", "status", "--json")
	if code != 0 {
		t.Fatalf("status: %d %s", code, errs)
	}
	var st struct {
		Enabled, TopicSet, TokenSet, Private bool
		AfterS                               int `json:"after_s"`
	}
	if strings.Contains(out, "SecretTopic") || strings.Contains(out, "tk_secret") {
		t.Fatalf("status leaks the secret: %s", out)
	}
	if err := json.Unmarshal([]byte(strings.ReplaceAll(strings.ReplaceAll(out, "topic_set", "topicset"), "token_set", "tokenset")), &st); err != nil ||
		!st.Enabled || !st.TopicSet || !st.TokenSet || st.Private || st.AfterS != 180 {
		t.Fatalf("status: %s", out)
	}
	if code, out, _ = runDP(t, "", "direct-push", "status"); code != 0 || !strings.Contains(out, "Direct push: on") {
		t.Fatalf("status text: %s", out)
	}

	if code, out, errs = runDP(t, "", "direct-push", "test"); code != 0 {
		t.Fatalf("test: %d %s %s", code, out, errs)
	}
	mu.Lock()
	if len(got) != 1 || got[0]["topic"] != "wy-SecretTopic123" || !strings.Contains(got[0]["message"].(string), "direct push test from") {
		t.Fatalf("test push: %+v", got)
	}
	mu.Unlock()

	if code, out, _ = runDP(t, "", "direct-push", "policy"); code != 0 || !strings.Contains(out, `"needs_permission"`) {
		t.Fatalf("policy defaults: %s", out)
	}
	pf := filepath.Join(home, "policy.json")
	os.WriteFile(pf, []byte(`{"v":1,"hub":"0.5.0","public_url":null,"machine":"m_x","settings":{"quiet_hours":{"enabled":false,"start":"23:00","end":"08:00"},"skip_remote":false,"max_age_s":900},"rules":[{"trigger":"asked","enabled":true,"delay_s":5,"bypass_quiet":false,"seats":[],"projects":[]}]}`), 0o600)
	if code, _, errs = runDP(t, "", "direct-push", "policy", "--file", pf); code != 0 {
		t.Fatalf("policy --file: %s", errs)
	}
	if code, out, _ = runDP(t, "", "direct-push", "policy"); code != 0 || !strings.Contains(out, `"delay_s": 5`) {
		t.Fatalf("installed policy: %s", out)
	}

	if code, _, _ = runDP(t, "", "direct-push", "off"); code != 0 {
		t.Fatal("off")
	}
	cfg, _ = os.ReadFile(filepath.Join(home, "config", "config.toml"))
	if !strings.Contains(string(cfg), "enabled = false") {
		t.Fatalf("off: %s", cfg)
	}
	if code, _, _ = runDP(t, "", "direct-push", "bogus"); code != 2 {
		t.Fatalf("unknown subcommand: %d", code)
	}
}
