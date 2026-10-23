package directpush

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/record"
)

const sid1 = "c41e0000-0000-4000-8000-000000000001"

type fakeNtfy struct {
	mu     sync.Mutex
	got    []Message
	auth   []string
	status int
	srv    *httptest.Server
}

func newNtfy(t *testing.T) *fakeNtfy {
	f := &fakeNtfy{status: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m Message
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.got = append(f.got, m)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		st := f.status
		f.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNtfy) msgs() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.got...)
}

type rig struct {
	t      *testing.T
	o      Options
	ob     *outbox.Outbox
	now    time.Time
	ntfy   *fakeNtfy
	slot   string
	email  string
	remote bool   // hook records carry derived.remote_url
	peer   string // hook records carry derived.<peer> = true (peer_prompt, peer_wait)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	state := t.TempDir()
	r := &rig{t: t, now: t0, ntfy: newNtfy(t), slot: "~/.claude-2", email: "me@example.com"}
	cfg := config.Defaults()
	cfg.Push.Direct.Enabled = true
	cfg.Push.Direct.Server = r.ntfy.srv.URL
	cfg.Push.Direct.Private = false
	cfg.Slots.Labels = map[string]string{"~/.claude-2": "work"}
	r.o = Options{StateDir: state, OutboxDir: filepath.Join(state, "outbox"), MachineID: "m_" + record.NewID(t0), Machine: "gpu-box",
		Config: cfg, Client: "sy/0.5.0-test", Now: func() time.Time { return r.now }}
	r.ob = outbox.New(r.o.OutboxDir)
	r.ob.Now, r.ob.NoFsync = r.o.Now, true
	if err := SaveSecret(state, Secret{Topic: "sy-test-topic", Token: "tk_abc"}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) hook(sid, event string, at time.Duration, fields map[string]any) *record.Record {
	r.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + sid + ".jsonl"}
	for k, v := range fields {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	rec, err := record.NewHookEvent(record.Meta{Machine: r.o.MachineID, Slot: r.slot, Client: "sy/0.5.0-test",
		Seat: &record.Seat{Account: "acc", Org: "org", Email: r.email}, Now: t0.Add(at)}, raw, record.HookOptions{LastMessages: true})
	if err != nil {
		r.t.Fatal(err)
	}
	if r.remote {
		rec.Derived.Set("remote_url", "https://claude.ai/code/session_abc")
	}
	if r.peer != "" {
		rec.Derived.Set(r.peer, true)
	}
	if _, err := r.ob.Append(rec); err != nil {
		r.t.Fatal(err)
	}
	return rec
}

func (r *rig) ask(sid string, at time.Duration) *record.Record {
	return r.hook(sid, "Stop", at, map[string]any{"last_assistant_message": "Done.\n\nShould I also update the migration?"})
}

func (r *rig) check() []Result {
	r.t.Helper()
	res, err := Check(context.Background(), r.o)
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

func (r *rig) contact(at time.Duration) {
	r.t.Helper()
	b, _ := json.Marshal(map[string]any{"time": t0.Add(at).Format(time.RFC3339), "ok": true, "last_contact": t0.Add(at).Format(time.RFC3339Nano)})
	if err := os.MkdirAll(r.o.OutboxDir, 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.o.OutboxDir, "flush.status.json"), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// directRecords returns the direct_push records in the outbox.
func (r *rig) directRecords() []record.DirectPushPayload {
	r.t.Helper()
	recs, _, err := readOutbox(r.o.OutboxDir, "", t0.Add(-FoldBack))
	if err != nil {
		r.t.Fatal(err)
	}
	var out []record.DirectPushPayload
	for _, x := range recs {
		if x.Kind == record.KindDirectPush {
			var p record.DirectPushPayload
			if err := x.DecodePayload(&p); err != nil || p.Validate() != nil {
				r.t.Fatalf("bad direct_push: %v", err)
			}
			if x.Machine != r.o.MachineID || x.Slot != r.slot || x.Seat != nil {
				r.t.Fatalf("envelope: %+v", x)
			}
			out = append(out, p)
		}
	}
	return out
}

func TestCheckSendsOnceWhenHubUnreachable(t *testing.T) {
	r := newRig(t)
	rec := r.ask(sid1, 0)
	r.now = t0.Add(74 * time.Second) // 60 s delay + 15 s grace not reached
	if res := r.check(); len(res) != 0 {
		t.Fatalf("early: %+v", res)
	}
	if at, ok, err := NextDue(r.o); err != nil || !ok || !at.Equal(t0.Add(75*time.Second)) {
		t.Fatalf("NextDue %v %v %v", at, ok, err)
	}
	r.now = t0.Add(75 * time.Second)
	res := r.check()
	if len(res) != 1 || res[0].Outcome != OutcomeSent || res[0].Episode != EpisodeKey(sid1, "asked", t0) {
		t.Fatalf("%+v", res)
	}
	m := r.ntfy.msgs()
	if len(m) != 1 || m[0].Topic != "sy-test-topic" || m[0].Title != "work · api · asked a question · direct" ||
		m[0].Message != "Should I also update the migration?" || m[0].Priority != 4 || m[0].Tags[0] != "question" {
		t.Fatalf("message %+v", m)
	}
	if r.ntfy.auth[0] != "Bearer tk_abc" {
		t.Fatalf("auth %q", r.ntfy.auth[0])
	}
	d := r.directRecords()
	if len(d) != 1 || !d[0].OK || d[0].Record != rec.ID || d[0].Episode != res[0].Episode || d[0].ServerHost != ServerHost(r.ntfy.srv.URL) {
		t.Fatalf("direct_push %+v", d)
	}
	// Marker: never twice.
	r.now = t0.Add(3 * time.Minute)
	if res := r.check(); len(res) != 0 || len(r.ntfy.msgs()) != 1 {
		t.Fatalf("second check sent again: %+v", res)
	}
	if _, ok, _ := NextDue(r.o); ok {
		t.Fatal("NextDue after the push")
	}
}

func TestCheckConditionsBlock(t *testing.T) {
	quiet := QuietHours{Enabled: true, Start: "08:00", End: "10:00", TZ: "UTC"}
	cases := []struct {
		name  string
		setup func(r *rig)
		at    time.Duration
		want  int
	}{
		{"hub reachable: recent contact after the record", func(r *rig) { r.contact(50 * time.Second) }, 90 * time.Second, 0},
		{"contact before the record: unreachable", func(r *rig) { r.contact(-time.Second) }, 90 * time.Second, 1},
		{"contact older than after_s", func(r *rig) { r.contact(5 * time.Second) }, 130 * time.Second, 1},
		{"pull mode: 180 s", func(r *rig) { r.o.Config.Hub.Reach = config.ReachPull; r.contact(5 * time.Second) }, 130 * time.Second, 0},
		{"answered by the hub (cursor past the record)", func(r *rig) {
			r.ask(sid1, 0)
			b := r.directDayFile()
			fi, _ := os.Stat(filepath.Join(r.o.OutboxDir, b))
			if err := r.ob.WriteCursor(outbox.Cursor{File: b, Offset: fi.Size()}); err != nil {
				r.t.Fatal(err)
			}
		}, 90 * time.Second, 0},
		{"stale: past delay + max_age", nil, 60*time.Second + 901*time.Second, 0},
		{"just before stale", nil, 60*time.Second + 900*time.Second, 1},
		{"quiet hours hold", func(r *rig) { r.policy(func(p *Policy) { p.Settings.QuietHours = quiet }) }, 90 * time.Second, 0},
		{"rule off", func(r *rig) { r.policy(func(p *Policy) { p.Rules[1].Enabled = false }) }, 90 * time.Second, 0},
		{"no matching rule (base deleted)", func(r *rig) { r.policy(func(p *Policy) { p.Rules = p.Rules[:1] }) }, 90 * time.Second, 0},
		{"skip_remote with a remote link", func(r *rig) {
			r.policy(func(p *Policy) { p.Settings.SkipRemote = true })
			r.remote = true
		}, 90 * time.Second, 0},
		{"skip_peers (default): a question waiting on a peer's reply", func(r *rig) { r.peer = "peer_wait" }, 90 * time.Second, 0},
		{"skip_peers (default): a question in a turn a peer message started", func(r *rig) {
			r.peer = "peer_prompt"
			r.hook(sid1, "UserPromptSubmit", -time.Second, map[string]any{"prompt": "<cross-session-message>"})
			r.peer = ""
			r.ask(sid1, 0)
		}, 90 * time.Second, 0},
		{"a person's turn after it pushes", func(r *rig) {
			r.peer = "peer_prompt"
			r.hook(sid1, "UserPromptSubmit", -2*time.Second, map[string]any{"prompt": "<cross-session-message>"})
			r.peer = ""
			r.hook(sid1, "UserPromptSubmit", -time.Second, map[string]any{"prompt": "go on"})
			r.ask(sid1, 0)
		}, 90 * time.Second, 1},
		{"skip_peers off", func(r *rig) {
			off := false
			r.policy(func(p *Policy) { p.Settings.SkipPeers = &off })
			r.peer = "peer_wait"
		}, 90 * time.Second, 1},
		{"direct push off", func(r *rig) { r.o.Config.Push.Direct.Enabled = false }, 90 * time.Second, 0},
		{"no topic", func(r *rig) { os.Remove(SecretPath(r.o.StateDir)) }, 90 * time.Second, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if c.setup != nil {
				c.setup(r)
			}
			if len(r.directDayFileMaybe()) == 0 {
				r.ask(sid1, 0)
			}
			r.now = t0.Add(c.at)
			if got := len(r.check()); got != c.want {
				t.Fatalf("pushed %d, want %d", got, c.want)
			}
		})
	}
}

func (r *rig) directDayFileMaybe() string {
	ents, _ := os.ReadDir(r.o.OutboxDir)
	for _, e := range ents {
		if dayFileRe.MatchString(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

func (r *rig) directDayFile() string {
	ents, _ := os.ReadDir(r.o.OutboxDir)
	for _, e := range ents {
		if dayFileRe.MatchString(e.Name()) {
			return e.Name()
		}
	}
	r.t.Fatal("no day file")
	return ""
}

func (r *rig) policy(edit func(p *Policy)) {
	r.t.Helper()
	p := DefaultPolicy()
	edit(&p)
	if err := SavePolicy(r.o.StateDir, p, r.now); err != nil {
		r.t.Fatal(err)
	}
}

func TestCheckFloodCap(t *testing.T) {
	r := newRig(t)
	for i := 1; i <= 8; i++ {
		r.ask(sidN(i), time.Duration(i)*time.Millisecond)
	}
	r.now = t0.Add(2 * time.Minute)
	res := r.check()
	var sent, flood int
	for _, x := range res {
		switch x.Outcome {
		case OutcomeSent:
			sent++
		case OutcomeFlood:
			flood++
		}
	}
	if sent != FloodMax || flood != 2 || len(r.ntfy.msgs()) != FloodMax || len(r.directRecords()) != FloodMax {
		t.Fatalf("sent %d flood %d: %+v", sent, flood, res)
	}
	r.now = t0.Add(13 * time.Minute)
	if res := r.check(); len(res) != 0 {
		t.Fatalf("flood-skipped episodes are recorded as skipped, not sent later: %+v", res)
	}
}

func sidN(n int) string { return "c41e0000-0000-4000-8000-00000000000" + string(rune('0'+n)) }

func TestCheckFailedPushIsRetried(t *testing.T) {
	r := newRig(t)
	r.ask(sid1, 0)
	r.ntfy.status = 503
	r.now = t0.Add(80 * time.Second)
	res := r.check()
	if len(res) != 1 || res[0].Outcome != OutcomeFailed || res[0].Status != 503 {
		t.Fatalf("%+v", res)
	}
	d := r.directRecords()
	if len(d) != 1 || d[0].OK || d[0].Status != 503 {
		t.Fatalf("%+v", d)
	}
	r.now = t0.Add(100 * time.Second)
	if res := r.check(); len(res) != 0 {
		t.Fatalf("retried before RetryAfter: %+v", res)
	}
	r.ntfy.status = 200
	r.now = t0.Add(141 * time.Second)
	if res := r.check(); len(res) != 1 || res[0].Outcome != OutcomeSent {
		t.Fatalf("retry: %+v", res)
	}
	r.now = t0.Add(5 * time.Minute)
	if res := r.check(); len(res) != 0 {
		t.Fatalf("after success: %+v", res)
	}
}

// Two checks racing for the same episode send it once (O_EXCL marker).
func TestCheckConcurrentOnce(t *testing.T) {
	r := newRig(t)
	r.ask(sid1, 0)
	r.now = t0.Add(80 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Check(context.Background(), r.o)
		}()
	}
	wg.Wait()
	if n := len(r.ntfy.msgs()); n != 1 {
		t.Fatalf("sent %d times", n)
	}
}

func TestBuildMessages(t *testing.T) {
	e := Episode{Key: "k", Trigger: TriggerNeedsPermission, Session: sid1, Slot: "~/.claude", Email: "client@example.com",
		Project: "data-pipeline", Notice: "Claude needs your permission to use Bash", RemoteURL: "https://claude.ai/code/session_x"}
	m := Build(e, MessageOptions{Topic: "t", Machine: "gpu-box", PublicURL: "https://hub.example.ts.net"})
	if m.Title != "client · data-pipeline · needs permission · direct" || m.Message != "Claude needs your permission to use Bash · gpu-box" ||
		m.Tags[0] != "lock" || m.Click != "https://hub.example.ts.net/#/sessions/"+sid1 || len(m.Actions) != 2 ||
		m.Actions[0].Label != "Open in Claude" || m.Actions[1].Label != "Open" {
		t.Fatalf("%+v", m)
	}
	for _, a := range m.Actions {
		if a.Action != "view" || strings.Contains(a.Label, "Snooze") || strings.Contains(a.Label, "Seen") {
			t.Fatalf("%+v", a)
		}
	}
	p := Build(e, MessageOptions{Topic: "t", Machine: "gpu-box", PublicURL: "https://hub.example.ts.net", Private: true})
	if p.Title != "Switchyard" || p.Message != "1 session needs you" || strings.Contains(p.Click, sid1) || len(p.Actions) != 1 {
		t.Fatalf("private %+v", p)
	}
	f := Build(Episode{Trigger: TriggerFailed, Detail: "rate_limit", Project: "api"}, MessageOptions{Machine: "gpu-box"})
	if f.Title != "Unattributed · api · rate limited · direct" || f.Tags[0] != "rotating_light" || f.Click != "" || len(f.Actions) != 0 {
		t.Fatalf("%+v", f)
	}
	long := Build(Episode{Trigger: TriggerAsked, Project: strings.Repeat("p", 200), Question: strings.Repeat("q", 400)}, MessageOptions{})
	if n := len([]rune(long.Title)); n > maxTitle || !strings.HasSuffix(long.Title, " · direct") || len([]rune(long.Message)) > maxBody {
		t.Fatalf("not cut: %d %q", n, long.Title)
	}
}

func TestPolicyCacheAndDefaults(t *testing.T) {
	dir := t.TempDir()
	p, ok := LoadPolicy(dir)
	if ok || len(p.Rules) != 3 || p.Settings.MaxAgeS != 900 {
		t.Fatalf("defaults: %+v", p)
	}
	r, _ := p.First("failed", Seat{}, "x")
	if r.DelayS != 0 || !r.BypassQuiet {
		t.Fatalf("%+v", r)
	}
	u := "https://hub.example"
	p.PublicURL = &u
	if err := SavePolicy(dir, p, t0); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadPolicy(dir)
	if !ok || got.FetchedAt == "" || got.publicURL() != u {
		t.Fatalf("%+v", got)
	}
	if fi, err := os.Stat(PolicyPath(dir)); err != nil || (fi.Mode().Perm() != 0o600 && !isWindows()) {
		t.Fatalf("%v %v", fi.Mode(), err)
	}
	if err := SavePolicy(dir, Policy{V: 2}, t0); err == nil {
		t.Fatal("v2 accepted")
	}
	// The default rules equal the hub's base rules (push.md 3.1).
	base := []model.Rule{
		{ID: "rule_needs_permission", Trigger: model.TriggerNeedsPermission, Base: true, Enabled: true, DelayS: 30},
		{ID: "rule_asked", Trigger: model.TriggerAsked, Base: true, Enabled: true, DelayS: 60},
		{ID: "rule_failed", Trigger: model.TriggerFailed, Base: true, Enabled: true, BypassQuiet: true},
	}
	for i, b := range base {
		d := DefaultPolicy().Rules[i]
		if d.Trigger != string(b.Trigger) || d.DelayS != b.DelayS || d.BypassQuiet != b.BypassQuiet {
			t.Fatalf("default %d: %+v vs %+v", i, d, b)
		}
	}
}

func TestSecret(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadSecret(dir); err != ErrNoSecret {
		t.Fatal(err)
	}
	for _, bad := range []Secret{{}, {Topic: "has space"}, {Topic: strings.Repeat("a", 65)}, {Topic: "ok", Token: "a b"}} {
		if SaveSecret(dir, bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := SaveSecret(dir, Secret{Topic: "sy-abc_DEF-123"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(SecretPath(dir)); fi.Mode().Perm() != 0o600 && !isWindows() {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestCheckServer(t *testing.T) {
	for s, ok := range map[string]bool{"https://ntfy.sh": true, "https://ntfy.example.com/": true, "http://127.0.0.1:8080": true,
		"http://10.0.0.5": true, "http://100.101.102.103": true, "http://ntfy.sh": false, "ftp://x": false, "https://u:p@x": false,
		"https://ntfy.sh/topic": false, "": false} {
		if (CheckServer(s) == nil) != ok {
			t.Errorf("%q: %v", s, CheckServer(s))
		}
	}
}

// The records Check writes, uploaded to a hub, mark the episode's alert fired/direct
// (machines.md 5.5): the client's key is the hub's key.
func TestDirectPushRecordMarksHubAlert(t *testing.T) {
	r := newRig(t)
	r.ask(sid1, 0)
	r.now = t0.Add(80 * time.Second)
	if res := r.check(); len(res) != 1 {
		t.Fatalf("%+v", res)
	}
	recs, _, err := readOutbox(r.o.OutboxDir, "", t0.Add(-FoldBack))
	if err != nil || len(recs) != 2 {
		t.Fatalf("%d %v", len(recs), err)
	}
	h := newHubSide(t, r.o.MachineID, func() time.Time { return t0.Add(2 * time.Minute) })
	h.ingest(t, recs...)
	var status, reason, episode string
	if err := h.st.R().QueryRow(`SELECT status, coalesce(reason, ''), episode FROM alert`).Scan(&status, &reason, &episode); err != nil {
		t.Fatal(err)
	}
	if status != "fired" || reason != "direct" || episode != EpisodeKey(sid1, "asked", t0) {
		t.Fatalf("%s/%s %s", status, reason, episode)
	}
}
