package api_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// anaNow is the hub clock of these tests: 2026-10-15 17:30 in Asia/Kolkata (UTC+5:30, a zone
// that is not a whole number of hours from UTC), so the default range is 2026-09-16..2026-10-15.
var anaNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// anaHub is a hub with a fixed clock and the hub time zone set; seed fills it.
func anaHub(t *testing.T, tz string, seed bool) (h *hub, token string) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(api.Config{Store: st, Now: func() time.Time { return anaNow },
		WebUI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>board")}}, PingEvery: time.Hour})
	h = &hub{t: t, st: st, srv: srv, http: httptest.NewServer(srv.Handler()), tokens: map[string]string{}}
	t.Cleanup(h.stop)
	if tz != "" {
		h.exec(`INSERT INTO setting (key, value, updated_at) VALUES ('hub.tz', ?, 1)`, `"`+tz+`"`)
	}
	if seed {
		anaSeed(h)
	}
	return h, h.token("phone")
}

func (h *hub) exec(q string, args ...any) {
	h.t.Helper()
	err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.ExecContext(context.Background(), q, args...)
		return err
	})
	if err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
}

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

// anaSeed: two seats, four token rows in range (one with no seat, one with an unknown
// model), one after the range, history days, a limit window and limit hits. Times are UTC;
// in Asia/Kolkata they land on:
//
//	k1 seat_a sonnet-5   2026-10-15 01:30 Thu  /p/alpha  1M in, 100k out         = $3.00
//	k2 seat_a sonnet-5   2026-10-14 15:30 Wed  /p/alpha  500k in, 1M cache read  = $1.20
//	k3 seat_b opus-4-6   2026-10-10 11:30 Sat  /p/beta   200k out, 100k 5m write = $5.625
//	k4 none   mystery-9  2026-10-12 05:30 Mon  (none)    1000 in                 = unpriced
//	k5 seat_a sonnet-5   2026-10-16 01:30 (after the range)
func anaSeed(h *hub) {
	h.exec(`INSERT INTO account (uuid, email, first_seen, last_seen) VALUES ('acc_a', 'a@x.io', 1, 1), ('acc_b', 'b@x.io', 1, 1)`)
	h.exec(`INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org', 1, 1)`)
	h.exec(`INSERT INTO seat (id, account_uuid, org_uuid, label, created_at) VALUES ('seat_a', 'acc_a', 'org', 'work', 1)`)
	h.exec(`INSERT INTO seat (id, account_uuid, org_uuid, created_at) VALUES ('seat_b', 'acc_b', 'org', 2)`)
	row := `INSERT INTO token_row (key, session_id, seat_id, ts, project, model, input, output, cache_5m, cache_1h, cache_read, record_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'r')`
	h.exec(row, "k1", "s1", "seat_a", ms("2026-10-14T20:00:00Z"), "/p/alpha", "claude-sonnet-5", 1_000_000, 100_000, 0, 0)
	h.exec(row, "k2", "s1", "seat_a", ms("2026-10-14T10:00:00Z"), "/p/alpha", "claude-sonnet-5", 500_000, 0, 0, 1_000_000)
	h.exec(row, "k3", "s2", "seat_b", ms("2026-10-10T06:00:00Z"), "/p/beta", "claude-opus-4-6", 0, 200_000, 100_000, 0)
	h.exec(row, "k4", "s3", nil, ms("2026-10-12T00:00:00Z"), nil, "mystery-9", 1000, 0, 0, 0)
	h.exec(row, "k5", "s1", "seat_a", ms("2026-10-15T20:00:00Z"), "/p/alpha", "claude-sonnet-5", 9, 9, 0, 0)

	h.exec(`INSERT INTO machine (id, name, first_seen) VALUES ('m_x', 'x', 1)`)
	h.exec(`INSERT INTO slot (id, machine_id, folder, first_seen, last_seen) VALUES (1, 'm_x', '~/.claude', 1, 1)`)
	hist := `INSERT INTO history_day (slot_id, day, model, seat_id, tokens, messages, record_id) VALUES (1, ?, ?, ?, ?, ?, 'r')`
	h.exec(hist, "2026-10-05", "claude-sonnet-5", "seat_a", 700, 5, "r")
	h.exec(hist, "2026-10-05", "", "seat_a", nil, 5, "r")
	h.exec(hist, "2026-10-06", "claude-opus-4-6", "seat_b", 100, 1, "r")
	h.exec(hist, "2026-10-10", "claude-opus-4-6", "seat_b", 5555, 9, "r") // a day with token rows: ignored

	h.exec(`INSERT INTO usage_window (seat_id, window_key, resets_at, started_at, peak_pct, final_pct, unused_pct, samples, updated_at)
		VALUES ('seat_a', 'five_hour', ?, ?, 80, 70, 30, 4, 1), ('seat_a', 'five_hour', ?, NULL, 10, 10, 90, 1, 1)`,
		ms("2026-10-14T12:00:00Z"), ms("2026-10-14T07:00:00Z"), ms("2026-08-01T12:00:00Z"))
	hit := `INSERT INTO limit_hit (session_id, ts, type, status, resets_at, seat_id) VALUES (?, ?, ?, 'rejected', ?, ?)`
	h.exec(hit, "s1", ms("2026-10-14T10:00:00Z"), "five_hour", ms("2026-10-14T12:00:00Z"), "seat_a")
	h.exec(hit, "s9", ms("2026-10-14T10:30:00Z"), "five_hour", ms("2026-10-14T12:00:00Z"), "seat_a") // same window: shown once
	h.exec(hit, "s3", ms("2026-10-13T10:00:00Z"), "seven_day", nil, nil)
}

func (h *hub) report(path, token string) (int, map[string]any) {
	h.t.Helper()
	code, body := h.do("GET", path, token, nil)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if m == nil {
		h.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	return code, m
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func list(t *testing.T, m map[string]any, key string) []map[string]any {
	t.Helper()
	arr, ok := m[key].([]any)
	if !ok {
		t.Fatalf("%s is not an array: %v", key, m[key])
	}
	out := make([]map[string]any, len(arr))
	for i, v := range arr {
		out[i], _ = v.(map[string]any)
	}
	return out
}

func TestAnalyticsErrors(t *testing.T) {
	h, tok := anaHub(t, "Asia/Kolkata", true)
	longAgo := "from=2024-01-01&to=2026-10-15"
	for _, c := range []struct {
		name, path string
		token      string
		code       int
		msg        string
	}{
		{"no token", "/v1/analytics/daily", "", 401, ""},
		{"write-only token", "/v1/analytics/daily", h.token("machine"), 403, ""},
		{"unknown report", "/v1/analytics/nope", tok, 404, "unknown report"},
		{"unknown seat", "/v1/analytics/daily?seat=seat_zzz", tok, 404, "no such seat"},
		{"bad from", "/v1/analytics/daily?from=2026-13-01", tok, 400, "from must be a date"},
		{"bad to", "/v1/analytics/daily?to=yesterday", tok, 400, "to must be a date"},
		{"time of day", "/v1/analytics/daily?from=2026-10-01T00:00:00Z", tok, 400, "from must be a date"},
		{"bad days", "/v1/analytics/daily?days=0", tok, 400, "days must be"},
		{"days too many", "/v1/analytics/daily?days=367", tok, 400, "days must be"},
		{"days not a number", "/v1/analytics/daily?days=x", tok, 400, "days must be"},
		{"from after to", "/v1/analytics/models?from=2026-10-10&to=2026-10-09", tok, 400, "from is after to"},
		{"too long", "/v1/analytics/heatmap?" + longAgo, tok, 400, "at most 366"},
		{"366 days is fine", "/v1/analytics/models?from=2025-10-15&to=2026-10-15", tok, 200, ""},
		{"367 days is not", "/v1/analytics/models?from=2025-10-14&to=2026-10-15", tok, 400, "at most 366"},
		{"one day", "/v1/analytics/models?from=2026-10-15&to=2026-10-15", tok, 200, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := h.do("GET", c.path, c.token, nil)
			if code != c.code || !strings.Contains(string(body), c.msg) {
				t.Fatalf("%d %s, want %d %q", code, body, c.code, c.msg)
			}
			if c.code == 400 && !strings.Contains(string(body), "bad_request") || c.code == 404 && !strings.Contains(string(body), "not_found") {
				t.Fatalf("error code: %s", body)
			}
		})
	}
}

func TestAnalyticsEmpty(t *testing.T) {
	h, tok := anaHub(t, "Asia/Kolkata", false)
	_, m := h.report("/v1/analytics/daily", tok)
	if m["from"] != "2026-09-16" || m["to"] != "2026-10-15" || m["tz"] != "Asia/Kolkata" || m["seat"] != nil {
		t.Fatalf("head: %v", m)
	}
	days := list(t, m, "days")
	if len(days) != 30 || days[0]["day"] != "2026-09-16" || days[29]["day"] != "2026-10-15" || days[29]["source"] != "transcripts" {
		t.Fatalf("days: %d %v", len(days), days)
	}
	if m["totals"].(map[string]any)["api_usd"].(float64) != 0 || len(list(t, m, "seats")) != 0 {
		t.Fatalf("totals/seats: %v", m)
	}
	for _, k := range []string{"models", "projects", "sessions", "limits", "hits"} {
		_, m := h.report("/v1/analytics/"+k, tok)
		key := map[string]string{"limits": "windows"}[k]
		if key == "" {
			key = k
		}
		if arr, ok := m[key].([]any); !ok || len(arr) != 0 { // [], never null
			t.Errorf("%s: %v", k, m[key])
		}
	}
	_, m = h.report("/v1/analytics/heatmap", tok)
	usd := m["api_usd"].([]any)
	if len(usd) != 7 || len(usd[0].([]any)) != 24 {
		t.Fatalf("heatmap shape: %v", m)
	}
}

func TestAnalyticsDaily(t *testing.T) {
	h, tok := anaHub(t, "Asia/Kolkata", true)
	_, m := h.report("/v1/analytics/daily", tok)
	days := map[string]map[string]any{}
	for _, d := range list(t, m, "days") {
		days[d["day"].(string)] = d
	}
	// The Kolkata day, not the UTC day: k1 is 2026-10-14T20:00Z = 2026-10-15 01:30.
	for day, want := range map[string]float64{"2026-10-15": 3.0, "2026-10-14": 1.2, "2026-10-10": 5.625, "2026-10-12": 0, "2026-10-11": 0} {
		if d := days[day]; d == nil || !near(d["api_usd"].(float64), want) {
			t.Errorf("%s: %v, want %v", day, d, want)
		}
	}
	if d := days["2026-10-15"]; d["input"].(float64) != 1_000_000 || d["output"].(float64) != 100_000 || d["replies"].(float64) != 1 ||
		d["total_tokens"].(float64) != 1_100_000 || d["source"] != "transcripts" {
		t.Errorf("counters: %v", d)
	}
	if d := days["2026-10-12"]; d["unpriced_replies"].(float64) != 1 || d["total_tokens"].(float64) != 1000 {
		t.Errorf("unpriced day: %v", d)
	}
	// Days before the first token row (2026-10-10) come from history; later ones ignore it.
	if d := days["2026-10-05"]; d["source"] != "history" || d["total_tokens"].(float64) != 700 || d["replies"].(float64) != 10 || d["api_usd"].(float64) != 0 {
		t.Errorf("history day: %v", d)
	}
	if d := days["2026-10-06"]; d["source"] != "history" || d["total_tokens"].(float64) != 100 {
		t.Errorf("history day: %v", d)
	}
	if d := days["2026-10-10"]; d["source"] != "transcripts" || d["total_tokens"].(float64) != 300_000 {
		t.Errorf("a day with token rows ignores history: %v", d)
	}
	if d := days["2026-09-20"]; d["source"] != "transcripts" || d["total_tokens"].(float64) != 0 {
		t.Errorf("empty day: %v", d)
	}
	tot := m["totals"].(map[string]any) // k5 is after the range
	if !near(tot["api_usd"].(float64), 3.0+1.2+5.625) || tot["replies"].(float64) != 4 || tot["unpriced_replies"].(float64) != 1 ||
		tot["cache_read"].(float64) != 1_000_000 || tot["cache_5m"].(float64) != 100_000 {
		t.Errorf("totals: %v", tot)
	}
	seats := list(t, m, "seats") // dearest first, Unattributed last
	if len(seats) != 3 || seats[0]["name"] != "b@x.io" || seats[1]["name"] != "work" || seats[2]["seat"] != nil || seats[2]["name"] != "Unattributed" {
		t.Fatalf("seats: %v", seats)
	}
	if !near(seats[1]["api_usd"].(float64), 4.2) {
		t.Errorf("seat_a: %v", seats[1])
	}
}

func TestAnalyticsRangeAndZone(t *testing.T) {
	// Same data in UTC: k1 (20:00Z) belongs to 10-14, and k5 (after the Kolkata range) to 10-15.
	h, tok := anaHub(t, "", true)
	t.Setenv("TZ", "")
	_, m := h.report("/v1/analytics/daily?from=2026-10-14&to=2026-10-15", tok)
	if m["tz"] != "UTC" || m["from"] != "2026-10-14" || m["to"] != "2026-10-15" {
		t.Fatalf("head: %v", m)
	}
	days := list(t, m, "days")
	if len(days) != 2 || !near(days[0]["api_usd"].(float64), 4.2) || !near(days[1]["api_usd"].(float64), 0.000108) {
		t.Fatalf("UTC days: %v", days)
	}
	// A narrower range excludes the rest from the totals.
	_, m = h.report("/v1/analytics/daily?from=2026-10-10&to=2026-10-10", tok)
	if tot := m["totals"].(map[string]any); !near(tot["api_usd"].(float64), 5.625) {
		t.Fatalf("totals: %v", tot)
	}
	// days sets the length ending at `to`; a given `from` wins.
	_, m = h.report("/v1/analytics/models?days=7", tok)
	if m["from"] != "2026-10-09" || m["to"] != "2026-10-15" {
		t.Fatalf("days=7: %v", m)
	}
	_, m = h.report("/v1/analytics/models?days=7&from=2026-10-01&to=2026-10-03", tok)
	if m["from"] != "2026-10-01" || m["to"] != "2026-10-03" {
		t.Fatalf("from wins over days: %v", m)
	}
	// To defaults to today, from to 29 days earlier.
	_, m = h.report("/v1/analytics/models?from=2026-10-12", tok)
	if m["from"] != "2026-10-12" || m["to"] != "2026-10-15" {
		t.Fatalf("default to: %v", m)
	}
}

func TestAnalyticsSeatFilter(t *testing.T) {
	h, tok := anaHub(t, "Asia/Kolkata", true)
	for _, c := range []struct {
		seat    string
		usd     float64
		replies float64
		seats   int
	}{
		{"seat_a", 4.2, 2, 1},
		{"seat_b", 5.625, 1, 1},
		{"unattributed", 0, 1, 1},
		{"", 3.0 + 1.2 + 5.625, 4, 3},
	} {
		t.Run("seat="+c.seat, func(t *testing.T) {
			_, m := h.report("/v1/analytics/daily?seat="+c.seat, tok)
			tot := m["totals"].(map[string]any)
			if !near(tot["api_usd"].(float64), c.usd) || tot["replies"].(float64) != c.replies || len(list(t, m, "seats")) != c.seats {
				t.Fatalf("%v", m)
			}
			if c.seat == "" && m["seat"] != nil || c.seat != "" && m["seat"] != c.seat {
				t.Fatalf("seat echo: %v", m["seat"])
			}
		})
	}
	// History honours the seat filter too: 2026-10-05 is seat_a's.
	_, m := h.report("/v1/analytics/daily?seat=seat_b", tok)
	for _, d := range list(t, m, "days") {
		if d["day"] == "2026-10-05" && d["total_tokens"].(float64) != 0 {
			t.Errorf("seat_b sees seat_a history: %v", d)
		}
		if d["day"] == "2026-10-06" && d["total_tokens"].(float64) != 100 {
			t.Errorf("seat_b history: %v", d)
		}
	}
	_, m = h.report("/v1/analytics/hits?seat=unattributed", tok)
	if hits := list(t, m, "hits"); len(hits) != 1 || hits[0]["seat"] != nil || hits[0]["seat_name"] != "Unattributed" {
		t.Errorf("unattributed hits: %v", hits)
	}
	_, m = h.report("/v1/analytics/limits?seat=seat_b", tok)
	if len(list(t, m, "windows")) != 0 {
		t.Errorf("seat_b windows: %v", m)
	}
}

func TestAnalyticsModelsProjectsSessions(t *testing.T) {
	h, tok := anaHub(t, "Asia/Kolkata", true)

	_, m := h.report("/v1/analytics/models", tok)
	models := list(t, m, "models")
	if len(models) != 3 {
		t.Fatalf("models: %v", models)
	}
	for i, w := range []struct {
		model, label string
		usd          float64
		priced       bool
	}{{"claude-opus-4-6", "Opus 4.6", 5.625, true}, {"claude-sonnet-5", "Sonnet 5", 4.2, true}, {"mystery-9", "mystery-9", 0, false}} {
		if models[i]["model"] != w.model || models[i]["label"] != w.label || !near(models[i]["api_usd"].(float64), w.usd) || models[i]["priced"] != w.priced {
			t.Errorf("model %d: %v, want %+v", i, models[i], w)
		}
	}
	if models[2]["unpriced_replies"].(float64) != 1 {
		t.Errorf("unpriced: %v", models[2])
	}

	_, m = h.report("/v1/analytics/projects", tok)
	projects := list(t, m, "projects")
	if len(projects) != 3 || projects[0]["name"] != "beta" || projects[1]["name"] != "alpha" || projects[2]["name"] != "(unknown)" ||
		projects[1]["sessions"].(float64) != 1 || projects[1]["project"] != "/p/alpha" || projects[2]["project"] != "" ||
		projects[1]["last_ts"] != "2026-10-14T20:00:00.000Z" || !near(projects[1]["api_usd"].(float64), 4.2) {
		t.Fatalf("projects: %v", projects)
	}

	_, m = h.report("/v1/analytics/sessions", tok)
	sessions := list(t, m, "sessions")
	if len(sessions) != 3 || sessions[0]["session_id"] != "s2" || sessions[1]["session_id"] != "s1" || sessions[2]["session_id"] != "s3" {
		t.Fatalf("sessions: %v", sessions)
	}
	s1 := sessions[1]
	if s1["seat"] != "seat_a" || s1["seat_name"] != "work" || s1["first_ts"] != "2026-10-14T10:00:00.000Z" ||
		s1["last_ts"] != "2026-10-14T20:00:00.000Z" || s1["replies"].(float64) != 2 || !near(s1["api_usd"].(float64), 4.2) {
		t.Errorf("s1: %v", s1)
	}
	if sessions[2]["seat"] != nil || sessions[2]["seat_name"] != "Unattributed" {
		t.Errorf("s3: %v", sessions[2])
	}
}

func TestAnalyticsHeatmap(t *testing.T) {
	h, tok := anaHub(t, "Asia/Kolkata", true)
	_, m := h.report("/v1/analytics/heatmap", tok)
	usd, replies := m["api_usd"].([]any), m["replies"].([]any)
	cell := func(a []any, wd, hr int) float64 { return a[wd].([]any)[hr].(float64) }
	for _, c := range []struct {
		name   string
		wd, hr int
		usd    float64
	}{
		{"k1 Thu 01:30", 4, 1, 3.0},
		{"k2 Wed 15:30", 3, 15, 1.2},
		{"k3 Sat 11:30", 6, 11, 5.625},
		{"k4 Mon 05:30", 1, 5, 0},
	} {
		if !near(cell(usd, c.wd, c.hr), c.usd) || cell(replies, c.wd, c.hr) != 1 {
			t.Errorf("%s: usd %v replies %v", c.name, cell(usd, c.wd, c.hr), cell(replies, c.wd, c.hr))
		}
	}
	var n float64
	for _, row := range replies {
		for _, v := range row.([]any) {
			n += v.(float64)
		}
	}
	if n != 4 {
		t.Errorf("replies in all cells: %v", n)
	}
}

func TestAnalyticsLimitsAndHits(t *testing.T) {
	h, tok := anaHub(t, "Asia/Kolkata", true)
	_, m := h.report("/v1/analytics/limits", tok)
	ws := list(t, m, "windows") // the August window is out of range
	if len(ws) != 1 || ws[0]["seat"] != "seat_a" || ws[0]["seat_name"] != "work" || ws[0]["key"] != "five_hour" || ws[0]["label"] == "" ||
		ws[0]["peak_pct"].(float64) != 80 || ws[0]["final_pct"].(float64) != 70 || ws[0]["unused_pct"].(float64) != 30 ||
		ws[0]["samples"].(float64) != 4 || ws[0]["resets_at"] != "2026-10-14T12:00:00.000Z" || ws[0]["started_at"] != "2026-10-14T07:00:00.000Z" {
		t.Fatalf("windows: %v", ws)
	}
	_, m = h.report("/v1/analytics/hits", tok)
	hits := list(t, m, "hits") // oldest first; the two s1/s9 hits of one window are one
	if len(hits) != 2 {
		t.Fatalf("hits: %v", hits)
	}
	if hits[0]["type"] != "seven_day" || hits[0]["resets_at"] != nil || hits[0]["wait_s"] != nil || hits[0]["seat"] != nil {
		t.Errorf("hit 0: %v", hits[0])
	}
	if hits[1]["session_id"] != "s1" || hits[1]["seat"] != "seat_a" || hits[1]["wait_s"].(float64) != 7200 || hits[1]["status"] != "rejected" {
		t.Errorf("hit 1: %v", hits[1])
	}
}

// A model with an id the price table does not know costs nothing but is still counted.
func TestAnalyticsCostMatchesPriceTable(t *testing.T) {
	h, tok := anaHub(t, "UTC", false)
	h.exec(`INSERT INTO token_row (key, session_id, ts, model, input, output, cache_5m, cache_1h, cache_read, record_id)
		VALUES ('x', 's', ?, 'claude-opus-4-6', 1000000, 1000000, 1000000, 1000000, 1000000, 'r')`, ms("2026-10-14T10:00:00Z"))
	_, m := h.report("/v1/analytics/models", tok)
	// opus 4.6: input 5, output 25, read 0.5, 5m write 6.25, 1h write 10 (USD per million).
	if got := list(t, m, "models")[0]["api_usd"].(float64); !near(got, 5+25+0.5+6.25+10) {
		t.Fatalf("cost %v", got)
	}
}
