// seed fills a sandbox hub with realistic data for looking at the phone app by eye (screenshots,
// test/e2e/android/screens.sh): three accounts with limits near 30, 85 and 97 %, sessions in
// every state on two machines, token rows for a week of analytics, and machine info. It posts
// records as the machines (hook_event, limit_sample, token_rows, machine_info) exactly as
// `wy hook` / `wy statusline` would, with no Claude Code involved. It only talks HTTP to the hub
// it is given; never point it at a real hub.
//
//	seed -hub http://127.0.0.1:18787 -m m_A=sy_m_... -m m_B=sy_m_... [-only sessions|limits|tokens|info]
//
// The first -m is "laptop", the second "gpu-box". Session ids are fixed (c41e5eed-...-0000000000NN),
// so a second run moves the same sessions instead of adding new ones.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

type machineFlag []string

func (m *machineFlag) String() string     { return strings.Join(*m, ",") }
func (m *machineFlag) Set(v string) error { *m = append(*m, v); return nil }

type mach struct{ id, token, name string }

var (
	hub   string
	seats = []*record.Seat{
		{Account: "acc-seed-personal", Org: "org-seed-personal", Email: "alex@example.com"},
		{Account: "acc-seed-work", Org: "org-seed-work", Email: "platform-team@acme.example"},
		{Account: "acc-seed-client", Org: "org-seed-client", Email: "contractor@client.example"},
	}
)

func sid(n int) string { return fmt.Sprintf("c41e5eed-0000-4000-8000-%012d", n) }

func post(m mach, recs ...*record.Record) {
	var raws []json.RawMessage
	for _, r := range recs {
		b, err := r.Encode()
		if err != nil {
			fail(err)
		}
		raws = append(raws, b)
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "wy/seed", "records": raws})
	req, _ := http.NewRequest("POST", hub+"/v1/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || bytes.Contains(out, []byte(`"rejected":[{`)) {
		fail(fmt.Errorf("ingest %d: %s", resp.StatusCode, out))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "seed:", err)
	os.Exit(1)
}

func meta(m mach, seat *record.Seat, at time.Time) record.Meta {
	return record.Meta{Machine: m.id, Slot: "~/.claude", Seat: seat, Client: "wy/seed", Now: at}
}

// hook is one hook event of session s at time at.
func hook(m mach, seat *record.Seat, at time.Time, s, cwd string, p map[string]any) *record.Record {
	p["session_id"] = s
	p["cwd"] = cwd
	p["transcript_path"] = "/home/me/.claude/projects/x/" + s + ".jsonl"
	raw, _ := json.Marshal(p)
	r, err := record.NewHookEvent(meta(m, seat, at), raw, record.HookOptions{LastMessages: true})
	if err != nil {
		fail(err)
	}
	return r
}

func ev(name string, kv ...any) map[string]any {
	p := map[string]any{"hook_event_name": name}
	for i := 0; i+1 < len(kv); i += 2 {
		p[kv[i].(string)] = kv[i+1]
	}
	return p
}

type sess struct {
	n       int
	m       int // machine index
	seat    int
	cwd     string
	name    string
	model   string
	ctx     float64
	started time.Duration // ago
	events  []struct {
		ago time.Duration
		p   map[string]any
	}
}

func (s *sess) at(ago time.Duration, p map[string]any) *sess {
	s.events = append(s.events, struct {
		ago time.Duration
		p   map[string]any
	}{ago, p})
	return s
}

func sessions(now time.Time) []*sess {
	min := time.Minute
	var out []*sess
	add := func(s *sess) *sess { out = append(out, s); return s }
	start := func(s *sess) *sess {
		return s.at(s.started, ev("SessionStart", "source", "startup", "model", s.model))
	}

	// working: a tool is running
	start(add(&sess{n: 1, m: 0, seat: 0, cwd: "/home/me/code/watchyard", name: "phone-ui-polish", model: "claude-opus-4-7", ctx: 41, started: 52 * min})).
		at(50*min, ev("UserPromptSubmit", "prompt", "polish the phone app")).
		at(1*min, ev("PreToolUse", "tool_name", "Bash", "tool_input", map[string]any{"command": "npx jest"}))
	// needs permission
	start(add(&sess{n: 2, m: 0, seat: 1, cwd: "/home/me/code/billing-api", name: "migrate-invoices", model: "claude-sonnet-5", ctx: 63, started: 3 * time.Hour})).
		at(170*min, ev("UserPromptSubmit", "prompt", "run the migration")).
		at(4*min, ev("Notification", "notification_type", "permission_prompt", "message", "Claude needs your permission to use Bash"))
	// asked a question (the Stop detector)
	start(add(&sess{n: 3, m: 1, seat: 0, cwd: "/srv/ml/trainer", name: "", model: "claude-opus-4-7", ctx: 78, started: 5 * time.Hour})).
		at(290*min, ev("UserPromptSubmit", "prompt", "tune the lr schedule")).
		at(12*min, ev("Stop", "last_assistant_message", "I added a cosine schedule with warmup to train.py and the loss curve is smoother.\n\nShould I also lower the base learning rate to 3e-4 before the long run tonight?"))
	// asked through AskUserQuestion
	start(add(&sess{n: 4, m: 0, seat: 2, cwd: "/home/me/code/client-portal", name: "auth-rework", model: "claude-sonnet-5", ctx: 22, started: 40 * min})).
		at(38*min, ev("UserPromptSubmit", "prompt", "rework login")).
		at(2*min, ev("PreToolUse", "tool_name", "AskUserQuestion", "tool_input", map[string]any{"questions": []map[string]any{{"question": "Which session store should the portal use: Redis or Postgres?"}}}))
	// failed
	start(add(&sess{n: 5, m: 1, seat: 1, cwd: "/srv/data/etl", name: "nightly-etl", model: "claude-opus-4-7", ctx: 91, started: 7 * time.Hour})).
		at(400*min, ev("UserPromptSubmit", "prompt", "fix the etl")).
		at(35*min, ev("StopFailure", "error", "rate_limit", "last_assistant_message", "Usage limit reached for this account."))
	// done (idle)
	start(add(&sess{n: 6, m: 0, seat: 0, cwd: "/home/me/code/watchyard", name: "release-notes", model: "claude-sonnet-5", ctx: 35, started: 2 * time.Hour})).
		at(110*min, ev("UserPromptSubmit", "prompt", "write release notes")).
		at(25*min, ev("Stop", "last_assistant_message", "Release notes for 0.8.0 are in CHANGELOG.md. The phone and hub sections are split out."))
	// done, a second one on the other machine
	start(add(&sess{n: 7, m: 1, seat: 1, cwd: "/srv/infra/terraform", name: "", model: "claude-opus-4-7", ctx: 12, started: 9 * time.Hour})).
		at(530*min, ev("UserPromptSubmit", "prompt", "plan")).
		at(480*min, ev("Stop", "last_assistant_message", "terraform plan shows 3 to add, 1 to change, 0 to destroy."))
	// ended
	start(add(&sess{n: 8, m: 0, seat: 2, cwd: "/home/me/code/client-portal", name: "fix-typos", model: "claude-haiku-4-5", ctx: 8, started: 26 * time.Hour})).
		at(25*time.Hour, ev("UserPromptSubmit", "prompt", "fix typos")).
		at(24*time.Hour, ev("Stop", "last_assistant_message", "Fixed 14 typos across the docs.")).
		at(23*time.Hour, ev("SessionEnd", "reason", "prompt_input_exit"))
	// working, long project name on the gpu box
	start(add(&sess{n: 9, m: 1, seat: 0, cwd: "/srv/ml/very-long-experiment-name-for-the-retrieval-benchmark", name: "", model: "claude-sonnet-5", ctx: 55, started: 20 * min})).
		at(18*min, ev("UserPromptSubmit", "prompt", "run the benchmark")).
		at(30*time.Second, ev("PostToolUse", "tool_name", "Read"))
	return out
}

func main() {
	var ms machineFlag
	flag.StringVar(&hub, "hub", "", "hub base URL")
	flag.Var(&ms, "m", "machine id=token (repeat; the first is laptop, the second gpu-box)")
	only := flag.String("only", "", "only one part: sessions, limits, tokens or info")
	flag.Parse()
	if hub == "" || len(ms) < 2 {
		fmt.Fprintln(os.Stderr, "usage: seed -hub URL -m ID=TOKEN -m ID=TOKEN [-only part]")
		os.Exit(2)
	}
	names := []string{"laptop", "gpu-box"}
	var machines []mach
	for i, v := range ms {
		id, tok, ok := strings.Cut(v, "=")
		if !ok {
			fail(fmt.Errorf("-m wants ID=TOKEN, got %q", v))
		}
		machines = append(machines, mach{id, tok, names[i%2]})
	}
	now := time.Now()
	on := func(part string) bool { return *only == "" || *only == part }

	if on("info") {
		for i, m := range machines {
			r, err := record.NewMachineInfo(meta(m, nil, now), record.MachineInfoPayload{
				OS: []string{"linux", "linux"}[i%2], Arch: []string{"amd64", "arm64"}[i%2], Sy: "0.8.0", RecordV: 1,
				Reach: record.ReachDirect, Name: m.name, ClaudeVersion: "2.3.1"})
			if err != nil {
				fail(err)
			}
			post(m, r)
		}
	}

	if on("sessions") {
		for _, s := range sessions(now) {
			m := machines[s.m]
			seat := seats[s.seat]
			var recs []*record.Record
			for i := range s.events {
				e := s.events[i]
				p := map[string]any{}
				for k, v := range e.p {
					p[k] = v
				}
				if s.name != "" {
					p["session_name"] = s.name
				}
				recs = append(recs, hook(m, seat, now.Add(-e.ago), sid(s.n), s.cwd, p))
			}
			// the status line: model and context of the session
			raw, _ := json.Marshal(map[string]any{"session_id": sid(s.n), "model": map[string]any{"id": s.model},
				"context_window": map[string]any{"used_percentage": s.ctx}})
			lr, err := record.NewLimitSample(meta(m, seat, now.Add(-s.events[len(s.events)-1].ago).Add(time.Second)), raw)
			if err != nil {
				fail(err)
			}
			recs = append(recs, lr)
			post(m, recs...)
		}
	}

	if on("limits") {
		// five_hour / seven_day used % per seat: 30/22, 85/61, 97/90
		pct := [][2]float64{{30, 22}, {85, 61}, {97, 90}}
		reset := [][2]time.Duration{{3*time.Hour + 20*time.Minute, 4*24*time.Hour + 6*time.Hour}, {75 * time.Minute, 2*24*time.Hour + 3*time.Hour}, {18 * time.Minute, 20 * time.Hour}}
		sess := []int{1, 2, 4}
		orgs := []string{`{"name":"Personal","capabilities":["claude_max"],"rate_limit_tier":"default_claude_max_20x"}`,
			`{"name":"Acme Platform","capabilities":["raven"],"rate_limit_tier":"default_raven"}`,
			`{"name":"Client Co","capabilities":["claude_pro"],"rate_limit_tier":"default_claude_pro"}`}
		people := []string{"Alex", "Platform team", "Contractor"}
		for i, seat := range seats {
			m := machines[0]
			raw, _ := json.Marshal(map[string]any{
				"session_id": sid(sess[i]),
				"rate_limits": map[string]any{
					"five_hour": map[string]any{"used_percentage": pct[i][0], "resets_at": now.Add(reset[i][0]).Unix()},
					"seven_day": map[string]any{"used_percentage": pct[i][1], "resets_at": now.Add(reset[i][1]).Unix()},
				},
			})
			r, err := record.NewLimitSample(meta(m, seat, now), raw)
			if err != nil {
				fail(err)
			}
			// org name, plan and display name, as the claude.ai usage reader reports them
			acct, _ := json.Marshal(map[string]string{"email_address": seat.Email, "display_name": people[i]})
			usage, _ := json.Marshal(map[string]any{
				"five_hour": map[string]any{"utilization": pct[i][0], "resets_at": now.Add(reset[i][0]).UTC().Format(time.RFC3339)},
				"seven_day": map[string]any{"utilization": pct[i][1], "resets_at": now.Add(reset[i][1]).UTC().Format(time.RFC3339)},
			})
			w, err := record.NewWebUsage(meta(m, seat, now), record.WebUsagePayload{TabID: fmt.Sprintf("seed-tab-%d", i),
				Org: json.RawMessage(orgs[i]), Account: acct, Usage: usage})
			if err != nil {
				fail(err)
			}
			post(m, r, w)
		}
	}

	if *only == "alerts" {
		// fresh needs-you events now, so a phone paired after the seed gets one push of each kind
		m := machines[0]
		recs := []*record.Record{
			hook(m, seats[0], now.Add(-2*time.Second), sid(11), "/home/me/code/docs-site", ev("UserPromptSubmit", "prompt", "x")),
			hook(m, seats[0], now, sid(11), "/home/me/code/docs-site", ev("Stop", "last_assistant_message", "The sitemap is regenerated.\n\nDo you want me to deploy the preview now?")),
			hook(m, seats[1], now.Add(-2*time.Second), sid(12), "/home/me/code/billing-api", ev("UserPromptSubmit", "prompt", "x")),
			hook(m, seats[1], now, sid(12), "/home/me/code/billing-api", ev("Notification", "notification_type", "permission_prompt", "message", "Claude needs your permission to use Bash")),
			hook(m, seats[2], now.Add(-2*time.Second), sid(13), "/home/me/code/client-portal", ev("UserPromptSubmit", "prompt", "x")),
			hook(m, seats[2], now, sid(13), "/home/me/code/client-portal", ev("StopFailure", "error", "server_error_500", "last_assistant_message", "API Error: 500")),
		}
		post(m, recs...)
	}

	if on("tokens") {
		models := []string{"claude-opus-4-7", "claude-sonnet-5", "claude-haiku-4-5"}
		cwd := map[string]string{}
		for _, x := range sessions(now) {
			cwd[sid(x.n)] = x.cwd
		}
		for mi, m := range machines {
			var rows []record.TokenRow
			for day := 0; day < 14; day++ {
				for h := 0; h < 24; h += 1 {
					// working hours, more on weekdays; deterministic
					at := time.Date(now.Year(), now.Month(), now.Day(), h, 17, 0, 0, time.UTC).AddDate(0, 0, -day)
					if at.After(now) {
						continue
					}
					w := at.Weekday()
					if h < 8 || h > 22 || ((w == time.Saturday || w == time.Sunday) && h%3 != 0) {
						continue
					}
					k := (day*7 + h*3 + mi) % 9
					if k == 0 {
						continue
					}
					s := 1 + (day+h+mi)%9
					rows = append(rows, record.TokenRow{
						Key: fmt.Sprintf("seed-%d-%d-%d", mi, day, h), TS: at.Format(time.RFC3339Nano), SessionID: sid(s), Cwd: cwd[sid(s)],
						Model: models[(h+mi)%3], Input: int64(20_000 + k*9_000), Output: int64(3_000 + k*1_500),
						CacheRead: int64(k * 120_000), Cache5m: int64(k * 8_000)})
				}
			}
			r, err := record.NewTokenRows(meta(m, seats[mi], now), record.TokenRowsPayload{Rows: rows})
			if err != nil {
				fail(err)
			}
			post(m, r)
		}
	}
	fmt.Println("seed: ok")
}
