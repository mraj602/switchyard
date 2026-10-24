package ntfy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/ntfy"
	"github.com/mraj602/watchyard/internal/hub/push/ntfytest"
)

const hub = "https://hub.example.ts.net"

func askedMsg() model.Message {
	return model.Message{
		Payload: model.Payload{
			V: 1, Type: model.TypeAlert, Alert: "al_01JA3B", Alerts: []string{"al_01JA3B"}, Trigger: model.TriggerAsked,
			Title: "work · api · asked a question", Body: "Should I also update the migration?",
			Tag: "sy-s-c41e", URL: "/#/sessions/c41e?alert=al_01JA3B", RemoteURL: "https://claude.ai/code/session_x",
			Tap: "auto", Session: "c41e", NeedsYou: 3, Actions: &model.Actions{Snooze: "a1.snz", Seen: "a1.seen"},
		},
		Priority: 4, HubURL: hub + "/",
	}
}

func TestBuildMatchesSpec72(t *testing.T) {
	got := ntfy.Build(askedMsg(), ntfy.BuildOptions{Topic: "sy-4fQx0Lr2", SnoozeMin: 15})
	b, _ := json.Marshal(got)
	want := `{"topic":"sy-4fQx0Lr2","title":"work · api · asked a question","message":"Should I also update the migration?",` +
		`"priority":4,"tags":["question"],"click":"https://hub.example.ts.net/#/sessions/c41e?alert=al_01JA3B",` +
		`"actions":[` +
		`{"action":"view","label":"Open in Claude","url":"https://claude.ai/code/session_x","clear":true},` +
		`{"action":"http","label":"Snooze 15 min","url":"https://hub.example.ts.net/v1/actions/a1.snz","method":"POST","headers":{"Content-Type":"application/json"},"body":"{}","clear":true},` +
		`{"action":"http","label":"Seen","url":"https://hub.example.ts.net/v1/actions/a1.seen","method":"POST","headers":{"Content-Type":"application/json"},"body":"{}","clear":true}]}`
	if string(b) != want {
		t.Fatalf("\n got %s\nwant %s", b, want)
	}
}

func TestBuildVariants(t *testing.T) {
	t.Run("no remote link: Open with the deep link", func(t *testing.T) {
		m := askedMsg()
		m.Payload.RemoteURL = ""
		a := ntfy.Build(m, ntfy.BuildOptions{Topic: "t"}).Actions
		if len(a) != 3 || a[0].Label != "Open" || a[0].URL != hub+"/#/sessions/c41e?alert=al_01JA3B" || a[1].Label != "Snooze" {
			t.Fatalf("%+v", a)
		}
	})
	t.Run("tap claude clicks the remote link", func(t *testing.T) {
		m := askedMsg()
		m.Payload.Tap = model.TapClaude
		if c := ntfy.Build(m, ntfy.BuildOptions{Topic: "t"}).Click; c != "https://claude.ai/code/session_x" {
			t.Fatal(c)
		}
	})
	t.Run("private: alert page, no remote link anywhere, snooze and seen kept", func(t *testing.T) {
		m := askedMsg()
		m.Payload.Private = true
		m.Payload.Tap = model.TapClaude
		m.Payload.Title, m.Payload.Body = "1 session needs you", ""
		m.Payload.URL = "/#/alerts/al_01JA3B"
		got := ntfy.Build(m, ntfy.BuildOptions{Topic: "t"})
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), "claude.ai") {
			t.Fatalf("remote link leaked: %s", b)
		}
		if got.Click != hub+"/#/alerts/al_01JA3B" || len(got.Actions) != 3 || got.Actions[0].Label != "Open" ||
			got.Actions[1].Label != "Snooze" || got.Actions[2].Label != "Seen" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("test and summary have no snooze or seen", func(t *testing.T) {
		m := askedMsg()
		m.Payload.Type, m.Payload.Trigger, m.Payload.Actions, m.Payload.RemoteURL, m.Payload.URL = model.TypeTest, "", nil, "", "/#/"
		got := ntfy.Build(m, ntfy.BuildOptions{Topic: "t"})
		if len(got.Actions) != 1 || got.Actions[0].Label != "Open" || got.Tags[0] != "white_check_mark" {
			t.Fatalf("%+v", got)
		}
		m.Payload.Type = model.TypeSummary
		if tg := ntfy.Build(m, ntfy.BuildOptions{}).Tags; len(tg) != 1 || tg[0] != "bell" {
			t.Fatal(tg)
		}
	})
	t.Run("priorities and tags", func(t *testing.T) {
		for trig, tag := range map[model.Trigger]string{
			model.TriggerNeedsPermission: "lock", model.TriggerAsked: "question", model.TriggerFailed: "rotating_light",
			model.TriggerLimit80: "warning", model.TriggerLimit95: "warning", model.TriggerMachineSilent: "zzz",
		} {
			m := askedMsg()
			m.Payload.Trigger = trig
			if tg := ntfy.Build(m, ntfy.BuildOptions{}).Tags; len(tg) != 1 || tg[0] != tag {
				t.Errorf("%s: %v", trig, tg)
			}
		}
		for in, want := range map[int]int{0: 3, -1: 1, 2: 2, 5: 5, 9: 5} {
			m := askedMsg()
			m.Priority = in
			if p := ntfy.Build(m, ntfy.BuildOptions{}).Priority; p != want {
				t.Errorf("priority %d -> %d, want %d", in, p, want)
			}
		}
	})
	t.Run("no hub url: no http actions, no relative click", func(t *testing.T) {
		m := askedMsg()
		m.HubURL = ""
		got := ntfy.Build(m, ntfy.BuildOptions{})
		if got.Click != "" || len(got.Actions) != 1 || got.Actions[0].Label != "Open in Claude" {
			t.Fatalf("%+v", got)
		}
	})
}

func TestMarshalCutsBodyFirstAndAtRuneBoundaries(t *testing.T) {
	m := askedMsg()
	m.Payload.Body = strings.Repeat("é", 6000)
	b, err := ntfy.Marshal(ntfy.Build(m, ntfy.BuildOptions{Topic: "sy-x"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > ntfy.MaxMessageBytes || len(b) < ntfy.MaxMessageBytes-8 {
		t.Fatalf("%d bytes", len(b))
	}
	var back ntfy.Message
	if err := json.Unmarshal(b, &back); err != nil || !strings.HasSuffix(back.Message, "...") || back.Title != m.Payload.Title ||
		len(back.Actions) != 3 {
		t.Fatalf("bad cut: %v title=%q actions=%d", err, back.Title, len(back.Actions))
	}
	// Then the title, when the body is gone and it is still too long.
	m.Payload.Body = "short"
	m.Payload.Title = strings.Repeat("T", 5000)
	b, err = ntfy.Marshal(ntfy.Build(m, ntfy.BuildOptions{Topic: "sy-x"}))
	if err != nil || len(b) > ntfy.MaxMessageBytes {
		t.Fatalf("%d %v", len(b), err)
	}
	// Small messages are untouched.
	m = askedMsg()
	b, _ = ntfy.Marshal(ntfy.Build(m, ntfy.BuildOptions{Topic: "sy-x"}))
	if !strings.Contains(string(b), `"message":"Should I also update the migration?"`) {
		t.Fatal(string(b))
	}
}

func TestTopics(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tp, err := ntfy.NewTopic()
		if err != nil {
			t.Fatal(err)
		}
		if len(tp) != 27 || !strings.HasPrefix(tp, "sy-") || !ntfy.ValidTopic(tp) || strings.ContainsAny(tp[3:], "-_") {
			t.Fatalf("bad topic %q", tp)
		}
		if seen[tp] {
			t.Fatal("duplicate topic")
		}
		seen[tp] = true
	}
	for _, ok := range []string{"a", "sy-x_Y", strings.Repeat("a", 64)} {
		if !ntfy.ValidTopic(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "a b", "a/b", "ü", strings.Repeat("a", 65)} {
		if ntfy.ValidTopic(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckServer(t *testing.T) {
	ok := map[string]string{
		"": "https://ntfy.sh", "https://ntfy.sh/": "https://ntfy.sh", "https://ntfy.example.com/sub/": "https://ntfy.example.com/sub",
		"http://127.0.0.1:8080": "http://127.0.0.1:8080", "http://localhost": "http://localhost", "http://ntfy.hub.example.ts.net": "http://ntfy.hub.example.ts.net",
		"http://100.101.102.103": "http://100.101.102.103", "http://192.168.1.5": "http://192.168.1.5", "https://10.0.0.7": "https://10.0.0.7",
	}
	for in, want := range ok {
		if got, err := ntfy.CheckServer(in, true); err != nil || got != want {
			t.Errorf("CheckServer(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"http://ntfy.example.com", "http://hub-box:8080", "ftp://x", "https://", "https://u:p@ntfy.sh", "https://ntfy.sh?x=1", "nonsense"} {
		if _, err := ntfy.CheckServer(bad, true); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	s, tp := ntfy.SplitEndpoint(ntfy.JoinEndpoint("https://ntfy.example.com/sub/", "sy-abc"))
	if s != "https://ntfy.example.com/sub" || tp != "sy-abc" {
		t.Fatal(s, tp)
	}
	if !ntfy.IsPublicServer("https://ntfy.sh") || !ntfy.IsPublicServer("https://NTFY.sh/") || ntfy.IsPublicServer("https://ntfy.example.com") {
		t.Fatal("IsPublicServer")
	}
}

func target(srv *ntfytest.Server, token string) model.Target {
	return model.Target{ID: "pt_1", Kind: model.KindNtfy, Endpoint: ntfy.JoinEndpoint(srv.URL(), "sy-topic123"), Secret: token, Enabled: true}
}

func TestSendPublishesToStub(t *testing.T) {
	srv := ntfytest.New()
	defer srv.Close()
	srv.RequireToken("tk_secret")
	s := ntfy.New(ntfy.Options{SnoozeMin: func() int { return 15 }})
	res := s.Send(context.Background(), target(srv, "tk_secret"), askedMsg())
	if res.Outcome != model.OutcomeSent || res.HTTPStatus != 200 || res.Err != "" {
		t.Fatalf("%+v", res)
	}
	got := srv.Requests()
	if len(got) != 1 || got[0].Err != nil {
		t.Fatalf("%+v", got)
	}
	g := got[0]
	if g.Header.Get("Authorization") != "Bearer tk_secret" || g.Msg.Topic != "sy-topic123" || g.Msg.Priority != 4 ||
		g.Msg.Title != "work · api · asked a question" || g.Msg.Tags[0] != "question" ||
		g.Msg.Click != hub+"/#/sessions/c41e?alert=al_01JA3B" || len(g.Msg.Actions) != 3 ||
		g.Msg.Actions[1].Label != "Snooze 15 min" || g.Msg.Actions[1].Method != "POST" || g.Msg.Actions[2].URL != hub+"/v1/actions/a1.seen" {
		t.Fatalf("%+v", g)
	}
}

func TestSendWithoutTokenSendsNoAuthHeader(t *testing.T) {
	srv := ntfytest.New()
	defer srv.Close()
	res := ntfy.New(ntfy.Options{}).Send(context.Background(), target(srv, ""), askedMsg())
	if res.Outcome != model.OutcomeSent || srv.Requests()[0].Header.Get("Authorization") != "" {
		t.Fatalf("%+v", res)
	}
}

func TestSendStatusMapping(t *testing.T) {
	date := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	cases := []struct {
		name    string
		reply   ntfytest.Reply
		outcome model.Outcome
		after   time.Duration
		afterLo time.Duration // for dates
	}{
		{"200", ntfytest.Reply{}, model.OutcomeSent, 0, 0},
		{"429", ntfytest.Reply{Status: 429, Body: `{"code":42908}`}, model.OutcomeRetry, 0, 0},
		{"429 retry-after", ntfytest.Reply{Status: 429, RetryAfter: "45"}, model.OutcomeRetry, 45 * time.Second, 0},
		{"429 capped", ntfytest.Reply{Status: 429, RetryAfter: "86400"}, model.OutcomeRetry, time.Hour, 0},
		{"429 date", ntfytest.Reply{Status: 429, RetryAfter: date}, model.OutcomeRetry, -1, 100 * time.Second},
		{"500", ntfytest.Reply{Status: 500}, model.OutcomeRetry, 0, 0},
		{"502", ntfytest.Reply{Status: 502}, model.OutcomeRetry, 0, 0},
		{"401", ntfytest.Reply{Status: 401, Body: "unauthorized"}, model.OutcomeFailed, 0, 0},
		{"403", ntfytest.Reply{Status: 403}, model.OutcomeFailed, 0, 0},
		{"413", ntfytest.Reply{Status: 413}, model.OutcomeFailed, 0, 0},
		{"400", ntfytest.Reply{Status: 400}, model.OutcomeFailed, 0, 0},
		{"404 is not gone", ntfytest.Reply{Status: 404}, model.OutcomeFailed, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := ntfytest.New()
			defer srv.Close()
			srv.Script(c.reply)
			res := ntfy.New(ntfy.Options{}).Send(context.Background(), target(srv, "tk_secret"), askedMsg())
			if res.Outcome != c.outcome {
				t.Fatalf("%+v", res)
			}
			switch {
			case c.after >= 0 && res.RetryAfter != c.after:
				t.Fatalf("RetryAfter %v, want %v", res.RetryAfter, c.after)
			case c.after < 0 && (res.RetryAfter < c.afterLo || res.RetryAfter > 121*time.Second):
				t.Fatalf("RetryAfter %v", res.RetryAfter)
			}
			if c.outcome != model.OutcomeSent && res.HTTPStatus == 0 {
				t.Fatal("no status")
			}
		})
	}
}

// A push-scoped caller gets https to public hosts only.
func TestCheckServerDeviceCallerPublicHTTPSOnly(t *testing.T) {
	for _, in := range []string{"", "https://ntfy.example.com/sub/", "https://8.8.8.8"} {
		if _, err := ntfy.CheckServer(in, false); err != nil {
			t.Errorf("%q rejected: %v", in, err)
		}
	}
	for _, bad := range []string{"http://ntfy.example.com", "https://localhost", "https://127.0.0.1", "https://10.0.0.5:8443",
		"https://192.168.1.5", "https://100.101.102.103", "https://hub-box", "https://ntfy.hub.example.ts.net", "https://foo.internal",
		"https://169.254.169.254", "https://[::1]", "https://0.0.0.0", "https://[64:ff9b::7f00:1]", "http://127.0.0.1:8080"} {
		if _, err := ntfy.CheckServer(bad, false); err == nil {
			t.Errorf("%q accepted for a device", bad)
		}
	}
}

// The owner's own targets reach loopback; a phone's do not, and neither follows redirects.
func TestSenderGuardsDeviceTargetsOnly(t *testing.T) {
	srv := ntfytest.New()
	defer srv.Close()
	tg := target(srv, "")
	s := ntfy.New(ntfy.Options{})
	if res := s.Send(context.Background(), tg, askedMsg()); res.Outcome != model.OutcomeSent {
		t.Fatalf("owner target: %+v", res)
	}
	tg.DeviceID = "dv_phone"
	res := s.Send(context.Background(), tg, askedMsg())
	if res.Outcome == model.OutcomeSent || !strings.Contains(res.Err, "non-public") || res.HTTPStatus != 0 {
		t.Fatalf("device target reached loopback: %+v", res)
	}
}

func TestErrorTextNeverHasTokenOrMessage(t *testing.T) {
	srv := ntfytest.New()
	defer srv.Close()
	srv.Script(ntfytest.Reply{Status: 403, Body: "token tk_secret is not allowed on topic " + strings.Repeat("z", 400)})
	res := ntfy.New(ntfy.Options{}).Send(context.Background(), target(srv, "tk_secret"), askedMsg())
	if res.Outcome != model.OutcomeFailed || strings.Contains(res.Err, "tk_secret") || res.Err != "HTTP 403" ||
		strings.Contains(res.Err, "Should I also") {
		t.Fatalf("%+v", res)
	}
}

func TestTransportErrorsRetryWithoutLeakingTopic(t *testing.T) {
	srv := ntfytest.New()
	tg := target(srv, "")
	srv.Script(ntfytest.Reply{Delay: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := ntfy.New(ntfy.Options{}).Send(ctx, tg, askedMsg())
	if res.Outcome != model.OutcomeRetry || res.Err != "timeout" {
		t.Fatalf("%+v", res)
	}
	srv.Close()
	res = ntfy.New(ntfy.Options{}).Send(context.Background(), tg, askedMsg())
	if res.Outcome != model.OutcomeRetry || res.Err != "connection failed" {
		t.Fatalf("%+v", res)
	}
}

func TestBadEndpointFails(t *testing.T) {
	for _, e := range []string{"", "nothing", "https://ntfy.sh/bad topic", "https://ntfy.sh/"} {
		res := ntfy.New(ntfy.Options{}).Send(context.Background(), model.Target{Endpoint: e}, askedMsg())
		if res.Outcome != model.OutcomeFailed {
			t.Errorf("%q: %+v", e, res)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	srv := ntfytest.New()
	defer srv.Close()
	srv.Script(ntfytest.Reply{Status: 307})
	res := ntfy.New(ntfy.Options{}).Send(context.Background(), target(srv, ""), askedMsg())
	if res.Outcome != model.OutcomeFailed || res.HTTPStatus != 307 {
		t.Fatalf("%+v", res)
	}
}

func TestStubPollReturnsWhatWasPublished(t *testing.T) {
	srv := ntfytest.New()
	defer srv.Close()
	ntfy.New(ntfy.Options{}).Send(context.Background(), target(srv, ""), askedMsg())
	resp, err := http.Get(srv.URL() + "/sy-topic123/json?poll=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ev struct {
		Event, Topic, Title string
		Actions             []ntfytest.Action
	}
	if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil || ev.Event != "message" || len(ev.Actions) != 3 {
		t.Fatalf("%v %+v", err, ev)
	}
}
