package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mraj602/switchyard/internal/board"
)

const anReadToken = "sy_d_" + "rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"

var anReplies = map[string]string{
	"daily": `{"report":"daily","from":"2026-10-14","to":"2026-10-15","tz":"Asia/Kolkata","seat":null,
	  "totals":{"input":1000,"output":2000,"cache_5m":3000,"cache_1h":0,"cache_read":4000000,"replies":42,"api_usd":12.345,"unpriced_replies":2},
	  "days":[{"day":"2026-10-14","source":"history","total_tokens":5000,"replies":7,"input":0,"output":0,"cache_5m":0,"cache_1h":0,"cache_read":0,"api_usd":0,"unpriced_replies":0},
	          {"day":"2026-10-15","source":"transcripts","total_tokens":4006000,"replies":35,"input":1000,"output":2000,"cache_5m":3000,"cache_1h":0,"cache_read":4000000,"api_usd":12.345,"unpriced_replies":2}],
	  "seats":[{"seat":"s1","name":"work","input":1000,"output":2000,"cache_5m":3000,"cache_1h":0,"cache_read":4000000,"replies":35,"api_usd":12.345,"unpriced_replies":2}]}`,
	"models": `{"report":"models","from":"2026-10-14","to":"2026-10-15","tz":"UTC","seat":null,
	  "models":[{"model":"claude-opus-4-6","label":"Opus 4.6","priced":true,"input":1,"output":2,"cache_5m":0,"cache_1h":0,"cache_read":0,"replies":3,"api_usd":1.5,"unpriced_replies":0},
	            {"model":"mystery","label":"mystery","priced":false,"input":1,"output":2,"cache_5m":0,"cache_1h":0,"cache_read":0,"replies":1,"api_usd":0,"unpriced_replies":1}]}`,
	"projects": `{"report":"projects","from":"2026-10-14","to":"2026-10-15","tz":"UTC","seat":null,
	  "projects":[{"project":"/home/me/switchyard","name":"switchyard","sessions":4,"last_ts":"2026-10-15T10:00:00Z","input":1,"output":2,"cache_5m":0,"cache_1h":0,"cache_read":0,"replies":9,"api_usd":3.2,"unpriced_replies":0}]}`,
	"sessions": `{"report":"sessions","from":"2026-10-14","to":"2026-10-15","tz":"UTC","seat":"s1",
	  "sessions":[{"session_id":"abcdef0123456789","seat":"s1","seat_name":"work","project":"/home/me/switchyard","models":["claude-opus-4-6"],"first_ts":"2026-10-15T09:00:00Z","last_ts":"2026-10-15T10:00:00Z","input":1,"output":2,"cache_5m":0,"cache_1h":0,"cache_read":0,"replies":9,"api_usd":3.2,"unpriced_replies":0}]}`,
	"limits": `{"report":"limits","from":"2026-10-14","to":"2026-10-15","tz":"UTC","seat":null,
	  "windows":[{"seat":"s1","seat_name":"work","key":"five_hour","label":"5 hour","started_at":null,"resets_at":"2026-10-15T12:00:00Z","peak_pct":91.4,"final_pct":null,"unused_pct":8.6,"samples":5}]}`,
	"hits": `{"report":"hits","from":"2026-10-14","to":"2026-10-15","tz":"Asia/Kolkata","seat":null,
	  "hits":[{"ts":"2026-10-15T11:00:00Z","seat":"s1","seat_name":"work","session_id":"x","type":"five_hour","status":"rejected","resets_at":"2026-10-15T12:00:00Z","wait_s":3600},
	          {"ts":"2026-10-15T11:30:00Z","seat":null,"seat_name":"Unattributed","session_id":"y","type":null,"status":null,"resets_at":null,"wait_s":null}]}`,
	"heatmap": `{"report":"heatmap","from":"2026-10-14","to":"2026-10-15","tz":"UTC","seat":null,"api_usd":[` +
		anHeatRows() + `],"replies":[` + anHeatRows() + `]}`,
}

func anHeatRows() string {
	rows := make([]string, 7)
	for d := range rows {
		cells := make([]string, 24)
		for h := range cells {
			cells[h] = "0"
		}
		if d == 1 {
			cells[9], cells[10] = "10", "2.5"
		}
		rows[d] = "[" + strings.Join(cells, ",") + "]"
	}
	return strings.Join(rows, ",")
}

type anHub struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string // path?query of each request
	auth  []string
}

func (h *anHub) last() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.paths[len(h.paths)-1]
}

// newAnHub answers the analytics reports with anReplies; status overrides it when not 0.
func newAnHub(t *testing.T, status int, body string) *anHub {
	t.Helper()
	h := &anHub{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.paths = append(h.paths, r.URL.RequestURI())
		h.auth = append(h.auth, r.Header.Get("Authorization"))
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		rep, ok := anReplies[strings.TrimPrefix(r.URL.Path, "/v1/analytics/")]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such report","code":"not_found"}`))
			return
		}
		_, _ = w.Write([]byte(rep))
	}))
	t.Cleanup(h.Close)
	return h
}

// anSandbox is a temp HOME connected to url, with a board token when withToken.
func anSandbox(t *testing.T, url string, withToken bool) {
	t.Helper()
	d := deskSandbox(t)
	connectSandbox(t, d, url)
	if withToken {
		if err := board.SaveToken(d.State.Path, board.Token{URL: url, Device: "dev_1", Token: anReadToken}); err != nil {
			t.Fatal(err)
		}
	}
}

func runAn(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := dispatch(append([]string{"analytics"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestAnalyticsDefaultIsDaily(t *testing.T) {
	hub := newAnHub(t, 0, "")
	anSandbox(t, hub.URL, true)
	code, out, errb := runAn()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if hub.last() != "/v1/analytics/daily" || hub.auth[0] != "Bearer "+anReadToken {
		t.Errorf("request %q auth %q", hub.last(), hub.auth[0])
	}
	for _, want := range []string{"daily  2026-10-14 to 2026-10-15", "Asia/Kolkata", "all seats", "42 replies", "4.0M tokens",
		"$12.35 API-equivalent", "2 replies of unpriced models", "DAY", "2026-10-15", "work", "SEAT"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// A history day shows only the total and the count.
	if !strings.Contains(out, "2026-10-14  7") || !strings.Contains(out, "5000") {
		t.Errorf("history day row missing:\n%s", out)
	}
	if strings.Contains(out+errb, anReadToken) {
		t.Error("token printed")
	}
}

func TestAnalyticsEachReport(t *testing.T) {
	hub := newAnHub(t, 0, "")
	anSandbox(t, hub.URL, true)
	cases := map[string][]string{
		"models":   {"Opus 4.6", "unpriced", "$1.50"},
		"projects": {"switchyard", "SESSIONS", "$3.20", "2026-10-15 10:00 UTC"},
		"sessions": {"abcdef01", "work", "switchyard", "seat s1"},
		"limits":   {"5 hour", "91%", "9%", "-"},
		"hits":     {"five_hour", "1h00m", "Unattributed", "2026-10-15 16:30 IST", "2026-10-15 17:30 IST"},
		"heatmap":  {"Mon", "Sun", "█", "darkest cell $10.00"},
	}
	for report, wants := range cases {
		code, out, errb := runAn(report)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", report, code, errb)
		}
		if hub.last() != "/v1/analytics/"+report {
			t.Errorf("%s: requested %q", report, hub.last())
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", report, want, out)
			}
		}
	}
}

func TestAnalyticsQueryFlags(t *testing.T) {
	hub := newAnHub(t, 0, "")
	anSandbox(t, hub.URL, true)
	// Flags may come after the report name.
	if code, _, errb := runAn("models", "--days", "7", "--seat", "s1", "--from", "2026-10-01", "--to", "2026-10-07"); code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	got := hub.last()
	for _, want := range []string{"/v1/analytics/models?", "days=7", "seat=s1", "from=2026-10-01", "to=2026-10-07"} {
		if !strings.Contains(got, want) {
			t.Errorf("request %q lacks %q", got, want)
		}
	}
}

func TestAnalyticsJSONIsTheHubsReply(t *testing.T) {
	hub := newAnHub(t, 0, "")
	anSandbox(t, hub.URL, true)
	code, out, errb := runAn("daily", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	var got, want any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	_ = json.Unmarshal([]byte(anReplies["daily"]), &want)
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Errorf("json differs:\n%s\n%s", gb, wb)
	}
	if strings.Contains(out, "DAY") {
		t.Error("--json printed the table too")
	}
}

func TestAnalyticsUsageErrors(t *testing.T) {
	hub := newAnHub(t, 0, "")
	anSandbox(t, hub.URL, true)
	for _, args := range [][]string{{"weekly"}, {"daily", "models"}, {"--days", "0x"}, {"--days", "400"}, {"--nope"}} {
		if code, _, _ := runAn(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if len(hub.paths) != 0 {
		t.Errorf("a usage error reached the hub: %v", hub.paths)
	}
}

func TestAnalyticsNoHubConfigured(t *testing.T) {
	deskSandbox(t)
	code, _, errb := runAn()
	if code != 4 || !strings.Contains(errb, "no hub is set") {
		t.Errorf("exit %d: %q", code, errb)
	}
}

// This machine's own token only writes (tokens.md 3), so without `sy board connect` the
// command explains that and does not send the machine token to the read endpoint.
func TestAnalyticsNoReadToken(t *testing.T) {
	hub := newAnHub(t, 0, "")
	anSandbox(t, hub.URL, false)
	code, _, errb := runAn()
	if code != 4 || !strings.Contains(errb, "may only write") || !strings.Contains(errb, "sy board connect") {
		t.Errorf("exit %d: %q", code, errb)
	}
	if len(hub.paths) != 0 {
		t.Errorf("the hub was called: %v", hub.paths)
	}
}

func TestAnalyticsHubUnreachable(t *testing.T) {
	hub := newAnHub(t, 0, "")
	url := hub.URL
	anSandbox(t, url, true)
	hub.Close()
	code, _, errb := runAn()
	if code != 5 || !strings.Contains(errb, "did not answer") {
		t.Errorf("exit %d: %q", code, errb)
	}
}

func TestAnalyticsHubRefusals(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   int
		want   string
	}{
		{401, `{"error":"unauthorized","code":"unauthorized"}`, 3, "refused the board token"},
		{403, `{"error":"forbidden","code":"forbidden"}`, 3, "no read scope"},
		{404, `{"error":"seat not found","code":"not_found"}`, 1, "seat not found"},
		{400, `{"error":"days must be 1-366","code":"bad_request"}`, 1, "days must be 1-366"},
		{500, `oops`, 1, "500"},
	}
	for _, c := range cases {
		hub := newAnHub(t, c.status, c.body)
		anSandbox(t, hub.URL, true)
		code, out, errb := runAn("--seat", "nope")
		if code != c.code || !strings.Contains(errb, c.want) || out != "" {
			t.Errorf("%d: exit %d out %q err %q", c.status, code, out, errb)
		}
	}
}

func TestAnalyticsInHelp(t *testing.T) {
	var out, errb bytes.Buffer
	if dispatch([]string{"help"}, &out, &errb) != 0 || !strings.Contains(out.String(), "analytics") {
		t.Errorf("sy help lacks analytics:\n%s", out.String())
	}
}
