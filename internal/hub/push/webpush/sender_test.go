package webpush_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/webpush"
	"github.com/mraj602/switchyard/internal/hub/push/webpushtest"
)

type rig struct {
	fake   *webpushtest.Server
	sub    *webpushtest.Subscription
	sender *webpush.Sender
	keys   *webpush.VAPID
	target model.Target
}

func newRig(t *testing.T) *rig {
	t.Helper()
	fake := webpushtest.New()
	t.Cleanup(fake.Close)
	keys, _, err := webpush.GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	fake.ExpectVapidKey(keys.PublicKey())
	sub := fake.NewSubscription()
	s := webpush.New(webpush.Options{
		Keys: keys, Subject: func() string { return "https://hub.example.ts.net" },
		Client: fake.Client(), AllowAnyEndpoint: true,
	})
	return &rig{fake: fake, sub: sub, sender: s, keys: keys, target: model.Target{
		ID: "pt_1", Kind: model.KindWebPush, Endpoint: sub.Endpoint, Enabled: true,
		Keys: &model.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}}
}

func msg() model.Message {
	return model.Message{
		Payload: model.Payload{
			V: 1, Type: model.TypeAlert, Alert: "al_1", Alerts: []string{"al_1"}, Trigger: model.TriggerAsked,
			Title: "work · api · asked a question", Body: "Should I also update the migration?",
			Tag: "sy-s-c41e", URL: "/#/sessions/c41e?alert=al_1", Tap: "auto", NeedsYou: 3,
			Actions: &model.Actions{Snooze: "a1.x", Seen: "a1.y"},
		},
		TTL: 6 * time.Hour, Urgency: "high",
	}
}

func TestSendRoundTrip(t *testing.T) {
	r := newRig(t)
	res := r.sender.Send(context.Background(), r.target, msg())
	if res.Outcome != model.OutcomeSent || res.HTTPStatus != 201 {
		t.Fatalf("result %+v", res)
	}
	got := r.fake.Requests()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	g := got[0]
	if g.VapidErr != nil || g.DecodeErr != nil {
		t.Fatalf("vapid %v decode %v", g.VapidErr, g.DecodeErr)
	}
	if g.Claims["sub"] != "https://hub.example.ts.net" {
		t.Fatalf("claims %v", g.Claims)
	}
	var p model.Payload
	if err := json.Unmarshal(g.Plain, &p); err != nil || p.Alert != "al_1" || p.Body != "Should I also update the migration?" ||
		p.Actions == nil || p.Actions.Seen != "a1.y" || p.NeedsYou != 3 {
		t.Fatalf("payload %s (%v)", g.Plain, err)
	}
	if g.Padding == 0 {
		t.Error("record not padded")
	}
	h := g.Header
	for k, want := range map[string]string{
		"Content-Encoding": "aes128gcm", "Content-Type": "application/octet-stream",
		"Ttl": "21600", "Urgency": "high", "Topic": webpush.Topic("sy-s-c41e"),
	} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	if len(h.Get("Topic")) != 22 || !strings.HasPrefix(h.Get("Authorization"), "vapid t=") {
		t.Errorf("topic %q auth %q", h.Get("Topic"), h.Get("Authorization"))
	}
}

func TestSendDefaultsAndNoTopic(t *testing.T) {
	r := newRig(t)
	m := msg()
	m.TTL, m.Urgency, m.Payload.Tag = 0, "bogus", ""
	if res := r.sender.Send(context.Background(), r.target, m); res.Outcome != model.OutcomeSent {
		t.Fatalf("%+v", res)
	}
	h := r.fake.Requests()[0].Header
	if h.Get("Ttl") != "21600" || h.Get("Urgency") != "normal" || h.Get("Topic") != "" {
		t.Fatalf("headers %v", h)
	}
}

// Every status of push.md 5.6, through the fake service.
func TestSendStatusMapping(t *testing.T) {
	cases := []struct {
		name       string
		reply      webpushtest.Reply
		outcome    model.Outcome
		status     int
		retryAfter time.Duration
	}{
		{"201", webpushtest.Reply{}, model.OutcomeSent, 201, 0},
		{"200", webpushtest.Reply{Status: 200}, model.OutcomeSent, 200, 0},
		{"202", webpushtest.Reply{Status: 202}, model.OutcomeSent, 202, 0},
		{"404", webpushtest.Reply{Status: 404, Body: "no such subscription"}, model.OutcomeGone, 404, 0},
		{"410", webpushtest.Reply{Status: 410, Body: "unsubscribed or expired"}, model.OutcomeGone, 410, 0},
		{"400", webpushtest.Reply{Status: 400, Body: "bad request"}, model.OutcomeFailed, 400, 0},
		{"401", webpushtest.Reply{Status: 401}, model.OutcomeFailed, 401, 0},
		{"403 apple", webpushtest.Reply{Status: 403, Body: `{"reason":"BadJwtToken"}`}, model.OutcomeFailed, 403, 0},
		{"413", webpushtest.Reply{Status: 413}, model.OutcomeFailed, 413, 0},
		{"429", webpushtest.Reply{Status: 429}, model.OutcomeRetry, 429, 0},
		{"429 seconds", webpushtest.Reply{Status: 429, RetryAfter: "120"}, model.OutcomeRetry, 429, 120 * time.Second},
		{"429 capped", webpushtest.Reply{Status: 429, RetryAfter: "999999"}, model.OutcomeRetry, 429, time.Hour},
		{"429 junk", webpushtest.Reply{Status: 429, RetryAfter: "soon"}, model.OutcomeRetry, 429, 0},
		{"429 negative", webpushtest.Reply{Status: 429, RetryAfter: "-5"}, model.OutcomeRetry, 429, 0},
		{"500", webpushtest.Reply{Status: 500}, model.OutcomeRetry, 500, 0},
		{"503 retry-after", webpushtest.Reply{Status: 503, RetryAfter: "30"}, model.OutcomeRetry, 503, 30 * time.Second},
		{"302 not followed", webpushtest.Reply{Status: 302}, model.OutcomeFailed, 302, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.fake.Script(c.reply)
			res := r.sender.Send(context.Background(), r.target, msg())
			if res.Outcome != c.outcome || res.HTTPStatus != c.status || res.RetryAfter != c.retryAfter {
				t.Fatalf("got %+v", res)
			}
			if c.outcome != model.OutcomeSent && !strings.HasPrefix(res.Err, itoa(c.status)) {
				t.Errorf("Err %q lacks the status", res.Err)
			}
			if c.outcome == model.OutcomeSent && res.Err != "" {
				t.Errorf("Err on success: %q", res.Err)
			}
			if g := r.fake.Requests()[0]; g.VapidErr != nil || g.DecodeErr != nil {
				t.Fatalf("service could not read the push: vapid %v decode %v", g.VapidErr, g.DecodeErr)
			}
		})
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRetryAfterHTTPDate(t *testing.T) {
	r := newRig(t)
	at := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	r.fake.Script(webpushtest.Reply{Status: 429, RetryAfter: at})
	res := r.sender.Send(context.Background(), r.target, msg())
	if res.Outcome != model.OutcomeRetry || res.RetryAfter < 80*time.Second || res.RetryAfter > 91*time.Second {
		t.Fatalf("%+v", res)
	}
}

func TestErrorKeepsFirst200BytesOfReply(t *testing.T) {
	r := newRig(t)
	r.fake.Script(webpushtest.Reply{Status: 403, Body: strings.Repeat("e", 1000)})
	res := r.sender.Send(context.Background(), r.target, msg())
	if len(res.Err) != len("403 ")+200 {
		t.Fatalf("Err is %d bytes", len(res.Err))
	}
}

func TestTimeoutAndConnectionErrorsRetryWithoutLeakingTheEndpoint(t *testing.T) {
	r := newRig(t)
	r.fake.Script(webpushtest.Reply{Delay: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := r.sender.Send(ctx, r.target, msg())
	if res.Outcome != model.OutcomeRetry || res.Err != "timeout" {
		t.Fatalf("%+v", res)
	}
	// A listener that drops every connection before TLS. Holding it open for the whole
	// test keeps the port from being reused by another test's server, which a closed
	// port does not.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	dead := "https://" + ln.Addr().String() + "/push/x"
	tg := r.target
	tg.Endpoint = dead
	res = r.sender.Send(context.Background(), tg, msg())
	if res.Outcome != model.OutcomeRetry || res.Err == "" || strings.Contains(res.Err, dead) {
		t.Fatalf("%+v", res)
	}
}

func TestBadKeysFailWithoutNetwork(t *testing.T) {
	r := newRig(t)
	for name, k := range map[string]*model.Keys{
		"nil":      nil,
		"bad auth": {P256dh: r.sub.P256dh, Auth: "AAAA"},
		"bad key":  {P256dh: "AAAA", Auth: r.sub.Auth},
	} {
		tg := r.target
		tg.Keys = k
		if res := r.sender.Send(context.Background(), tg, msg()); res.Outcome != model.OutcomeFailed {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if n := len(r.fake.Requests()); n != 0 {
		t.Fatalf("%d requests reached the service", n)
	}
}

func TestOversizePayloadIsCutToFit(t *testing.T) {
	r := newRig(t)
	m := msg()
	m.Payload.Body = strings.Repeat("ä", 5000)
	if res := r.sender.Send(context.Background(), r.target, m); res.Outcome != model.OutcomeSent {
		t.Fatalf("%+v", res)
	}
	g := r.fake.Requests()[0]
	if len(g.Plain) > webpush.MaxPayloadJSON {
		t.Fatalf("plaintext %d bytes", len(g.Plain))
	}
	var p model.Payload
	if err := json.Unmarshal(g.Plain, &p); err != nil || !strings.HasSuffix(p.Body, "...") || p.Title != m.Payload.Title {
		t.Fatalf("payload not cut cleanly: %v %q", err, p.Body[len(p.Body)-10:])
	}
}

func TestEndpointAllowList(t *testing.T) {
	ok := []string{
		"https://fcm.googleapis.com/fcm/send/abc", "https://web.push.apple.com/Qx", "https://updates.push.services.mozilla.com/wpush/v2/x",
		"https://wns2-par02p.notify.windows.com/w/?token=x",
	}
	for _, e := range ok {
		if err := webpush.CheckEndpoint(e, nil); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
	bad := []string{
		"http://fcm.googleapis.com/x", "https://fcm.googleapis.com:8443/x", "https://evil.example/x", "https://googleapis.com.evil.example/x",
		"https://notfcm.googleapis.com.evil.io/x", "https://user:pw@fcm.googleapis.com/x", "https://127.0.0.1/x", "https://googleapis.com/x",
		"https://jmt17.google.com/x", "ftp://fcm.googleapis.com/x", "/relative", "",
	}
	for _, e := range bad {
		if err := webpush.CheckEndpoint(e, nil); err == nil {
			t.Errorf("%q accepted", e)
		}
	}
	if err := webpush.CheckEndpoint("https://push.example.org/x", []string{"push.example.org"}); err != nil {
		t.Errorf("custom host: %v", err)
	}
	if err := webpush.CheckEndpoint("https://FCM.GoogleAPIs.com/x", nil); err != nil {
		t.Errorf("case: %v", err)
	}
}

// The sender re-checks the endpoint even though registration did.
func TestSenderRefusesDisallowedEndpoint(t *testing.T) {
	r := newRig(t)
	s := webpush.New(webpush.Options{Keys: r.keys, Client: r.fake.Client()}) // no AllowAnyEndpoint
	res := s.Send(context.Background(), r.target, msg())                     // endpoint is https://127.0.0.1:port
	if res.Outcome != model.OutcomeFailed || res.Err == "" {
		t.Fatalf("%+v", res)
	}
	if len(r.fake.Requests()) != 0 {
		t.Fatal("request was sent")
	}
}

// The guarded client does not connect to loopback or private addresses, and follows no redirect.
func TestGuardedClientRefusesPrivateAddresses(t *testing.T) {
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- struct{}{} }))
	defer srv.Close()
	keys, _, _ := webpush.GenerateVAPID()
	s := webpush.New(webpush.Options{Keys: keys, AllowAnyEndpoint: true}) // default guarded client
	fake := webpushtest.New()
	defer fake.Close()
	sub := fake.NewSubscription()
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]", "10.1.2.3", "192.168.1.1", "169.254.169.254", "100.64.0.1", "[::ffff:127.0.0.1]", "0.0.0.0"} {
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
		tg := model.Target{Kind: model.KindWebPush, Endpoint: "https://" + host + ":" + port + "/x",
			Keys: &model.Keys{P256dh: sub.P256dh, Auth: sub.Auth}}
		res := s.Send(context.Background(), tg, msg())
		if res.Outcome == model.OutcomeSent || !strings.Contains(res.Err, "non-public") {
			t.Errorf("%s: %+v", host, res)
		}
	}
	select {
	case <-hit:
		t.Fatal("the guard let a connection through")
	default:
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	r := newRig(t)
	r.fake.Script(webpushtest.Reply{Status: 307})
	if res := r.sender.Send(context.Background(), r.target, msg()); res.Outcome != model.OutcomeFailed || res.HTTPStatus != 307 {
		t.Fatalf("%+v", res)
	}
	if n := len(r.fake.Requests()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestSenderKindAndPublicKey(t *testing.T) {
	r := newRig(t)
	if r.sender.Kind() != model.KindWebPush || r.sender.PublicKey() != r.keys.PublicKey() {
		t.Fatal("kind or key")
	}
}
