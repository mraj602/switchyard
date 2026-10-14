package expo_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/expo"
	"github.com/mraj602/switchyard/internal/hub/push/expotest"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/netguard"
)

var target = model.Target{ID: gTgt, Kind: model.KindExpo, DeviceID: "dev_1", Endpoint: gToken}

func fakeSender(f *expotest.Server, token string, logTo *bytes.Buffer) *expo.Sender {
	o := expo.Options{BaseURL: f.URL(), AccessToken: token, Client: f.Client(),
		Now: func() time.Time { return gNow }}
	if logTo != nil {
		o.Log = log.New(logTo, "", 0)
	}
	return expo.New(o)
}

// TestResponseMapping is push.md 12.3, row by row, against the fake service.
func TestResponseMapping(t *testing.T) {
	f := expotest.New()
	defer f.Close()
	s := fakeSender(f, "", nil)
	msg := goldenCases()["asked_question"]
	for _, c := range []struct {
		name       string
		reply      expotest.Reply
		outcome    model.Outcome
		status     int
		err        string
		retryAfter time.Duration
		ref        bool
	}{
		{"ok", expotest.Reply{}, model.OutcomeSent, 200, "", 0, true},
		{"DeviceNotRegistered", expotest.Reply{TicketError: "DeviceNotRegistered"}, model.OutcomeGone, 200, "DeviceNotRegistered", 0, false},
		{"MessageRateExceeded", expotest.Reply{TicketError: "MessageRateExceeded"}, model.OutcomeRetry, 200, "MessageRateExceeded", time.Minute, false},
		{"MessageTooBig", expotest.Reply{TicketError: "MessageTooBig"}, model.OutcomeFailed, 200, "MessageTooBig", 0, false},
		{"InvalidCredentials", expotest.Reply{TicketError: "InvalidCredentials"}, model.OutcomeFailed, 200, expo.ErrTextCredentials, 0, false},
		{"MismatchSenderId", expotest.Reply{TicketError: "MismatchSenderId"}, model.OutcomeFailed, 200, expo.ErrTextCredentials, 0, false},
		{"other ticket error", expotest.Reply{TicketError: "ProviderError"}, model.OutcomeFailed, 200, "Expo: ProviderError", 0, false},
		{"hostile ticket error", expotest.Reply{TicketError: "Should I also update the migration?"}, model.OutcomeFailed, 200, "Expo: unknown", 0, false},
		{"PUSH_TOO_MANY_EXPERIENCE_IDS", expotest.Reply{Errors: []string{"PUSH_TOO_MANY_EXPERIENCE_IDS"}}, model.OutcomeRetry, 200, "Expo: PUSH_TOO_MANY_EXPERIENCE_IDS", 0, false},
		{"PUSH_TOO_MANY_NOTIFICATIONS", expotest.Reply{Errors: []string{"PUSH_TOO_MANY_NOTIFICATIONS"}}, model.OutcomeRetry, 200, "Expo: PUSH_TOO_MANY_NOTIFICATIONS", 0, false},
		{"TOO_MANY_REQUESTS in 200", expotest.Reply{Errors: []string{"TOO_MANY_REQUESTS"}, RetryAfter: "30"}, model.OutcomeRetry, 200, "Expo: TOO_MANY_REQUESTS", 30 * time.Second, false},
		{"other top-level error", expotest.Reply{Errors: []string{"INTERNAL_SERVER_ERROR"}}, model.OutcomeFailed, 200, "Expo: INTERNAL_SERVER_ERROR", 0, false},
		{"401", expotest.Reply{Status: 401}, model.OutcomeFailed, 401, expo.ErrTextAccessToken, 0, false},
		{"403", expotest.Reply{Status: 403}, model.OutcomeFailed, 403, expo.ErrTextAccessToken, 0, false},
		{"429 Retry-After", expotest.Reply{Status: 429, RetryAfter: "120"}, model.OutcomeRetry, 429, "HTTP 429 TOO_MANY_REQUESTS", 2 * time.Minute, false},
		{"429 HTTP date", expotest.Reply{Status: 429, RetryAfter: gNow.Add(90 * time.Second).Format(http.TimeFormat)}, model.OutcomeRetry, 429, "HTTP 429 TOO_MANY_REQUESTS", 90 * time.Second, false},
		{"429 huge", expotest.Reply{Status: 429, RetryAfter: "999999"}, model.OutcomeRetry, 429, "HTTP 429 TOO_MANY_REQUESTS", time.Hour, false},
		{"500", expotest.Reply{Status: 500}, model.OutcomeRetry, 500, "HTTP 500 INTERNAL_SERVER_ERROR", 0, false},
		{"503 Retry-After", expotest.Reply{Status: 503, RetryAfter: "5"}, model.OutcomeRetry, 503, "HTTP 503 INTERNAL_SERVER_ERROR", 5 * time.Second, false},
		{"400", expotest.Reply{Status: 400}, model.OutcomeFailed, 400, "HTTP 400 VALIDATION_ERROR", 0, false},
		{"413", expotest.Reply{Status: 413}, model.OutcomeFailed, 413, "HTTP 413 PUSH_TOO_MANY_NOTIFICATIONS", 0, false},
		{"404", expotest.Reply{Status: 404, Body: "not found <html>Should I</html>"}, model.OutcomeFailed, 404, "HTTP 404", 0, false},
		{"200 garbage", expotest.Reply{Body: "<html>captive portal</html>"}, model.OutcomeFailed, 200, "unreadable answer from Expo", 0, false},
		{"200 no ticket", expotest.Reply{Body: `{"data":[]}`}, model.OutcomeFailed, 200, "no ticket in Expo's answer", 0, false},
		{"200 array ticket", expotest.Reply{Body: `{"data":[{"status":"ok","id":"abc-123"}]}`}, model.OutcomeSent, 200, "", 0, true},
		{"slow", expotest.Reply{Delay: 2 * time.Second}, model.OutcomeRetry, 0, "timeout", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f.Script(c.reply)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := s.Send(ctx, target, msg)
			if r.Outcome != c.outcome || r.HTTPStatus != c.status || r.Err != c.err || r.RetryAfter != c.retryAfter || (r.Ref != "") != c.ref {
				t.Fatalf("%+v", r)
			}
		})
	}
	for _, g := range f.Requests() {
		if g.Err != nil {
			t.Errorf("the fake refused a request: %v", g.Err)
		}
	}
}

func TestSendRequestShape(t *testing.T) {
	f := expotest.New()
	defer f.Close()
	// No token: no Authorization header (the fake refuses one it does not expect).
	r := fakeSender(f, "", nil).Send(context.Background(), target, goldenCases()["asked_question"])
	if r.Outcome != model.OutcomeSent || !strings.HasPrefix(r.Ref, "0199e3a0-") {
		t.Fatalf("%+v", r)
	}
	got := f.Requests()[0]
	want, _ := expo.Marshal(expo.Build(gToken, goldenCases()["asked_question"]))
	if got.Err != nil || !bytes.Equal(got.Raw, want) || got.Header.Get("Authorization") != "" ||
		got.Header.Get("Accept") != "application/json" || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%v %s %v", got.Err, got.Raw, got.Header)
	}
	// Enhanced security: the bearer token is sent; a sender without it gets the 12.3 error.
	f.ExpectToken("expo-access-token-1")
	if r := fakeSender(f, "expo-access-token-1", nil).Send(context.Background(), target, refreshMessage()); r.Outcome != model.OutcomeSent {
		t.Fatalf("%+v", r)
	}
	if h := f.Requests()[1].Header.Get("Authorization"); h != "Bearer expo-access-token-1" {
		t.Fatal(h)
	}
	if r := fakeSender(f, "", nil).Send(context.Background(), target, refreshMessage()); r.Outcome != model.OutcomeFailed || r.Err != expo.ErrTextAccessToken {
		t.Fatalf("%+v", r)
	}
	// Every golden message is accepted by the fake's own checks.
	f.ExpectToken("")
	n := len(f.Requests())
	for name, m := range goldenCases() {
		if r := fakeSender(f, "", nil).Send(context.Background(), target, m); r.Outcome != model.OutcomeSent {
			t.Errorf("%s: %+v", name, r)
		}
	}
	for _, g := range f.Requests()[n:] {
		if g.Err != nil {
			t.Errorf("fake: %v\n%s", g.Err, g.Raw)
		}
	}
	// A bad token is refused before any request.
	if r := fakeSender(f, "", nil).Send(context.Background(), model.Target{ID: "x", Endpoint: "https://evil.example/"}, testMessage()); r.Outcome != model.OutcomeFailed {
		t.Fatalf("%+v", r)
	}
}

// TestNoTextInErrorsOrLog is the property test: whatever Expo answers, no title, body or
// question text appears in Result.Err or the log.
func TestNoTextInErrorsOrLog(t *testing.T) {
	f := expotest.New()
	defer f.Close()
	var logs bytes.Buffer
	s := fakeSender(f, "", &logs)
	secret := []string{"Ship it to prod", "SECRET-PROJECT-ZEBRA", "sess_01JA3B7Q", gToken, "xxxxxxxxxxxxxxxxxxxxxx"}
	m := goldenCases()["asked_question"]
	m.Payload.Title = "SECRET-PROJECT-ZEBRA · api · asked a question"
	m.Payload.Body = "Ship it to prod now, or wait for the review?"
	replies := []expotest.Reply{{Echo: true}, {Status: 400}, {Status: 401}, {Status: 413}, {Status: 429}, {Status: 500},
		{Body: `{"data":{"status":"error","message":"Ship it to prod now ` + gToken + `","details":{"error":"SECRET-PROJECT-ZEBRA"}}}`},
		{Body: `{"errors":[{"code":"Ship it to prod","message":"SECRET-PROJECT-ZEBRA"}]}`},
		{Status: 400, Body: `{"errors":[{"code":"SECRET_PROJECT_ZEBRA x","message":"Ship it to prod"}]}`},
		{Status: 502, Body: "Ship it to prod now SECRET-PROJECT-ZEBRA"},
		{Body: "Ship it to prod now SECRET-PROJECT-ZEBRA"},
		{Delay: 2 * time.Second}}
	for _, code := range []string{"DeviceNotRegistered", "MessageTooBig", "MessageRateExceeded", "InvalidCredentials", "MismatchSenderId", "Other"} {
		replies = append(replies, expotest.Reply{TicketError: code, Echo: true})
	}
	for i, rp := range replies {
		f.Script(rp)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r := s.Send(ctx, target, m)
		cancel()
		for _, sec := range secret {
			if strings.Contains(r.Err, sec) {
				t.Errorf("reply %d: Err %q holds %q", i, r.Err, sec)
			}
		}
	}
	for _, sec := range secret {
		if strings.Contains(logs.String(), sec) {
			t.Errorf("log holds %q: %s", sec, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "MessageTooBig") {
		t.Errorf("MessageTooBig is not logged as a bug: %q", logs.String())
	}
}

// TestDialGuard is the SSRF test: the default client refuses non-public addresses after
// DNS and does not follow redirects.
func TestDialGuard(t *testing.T) {
	for _, base := range []string{"https://127.0.0.1", "https://10.0.0.1", "https://100.64.0.1", "https://[::1]", "https://localhost"} {
		s := expo.New(expo.Options{BaseURL: base})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		r := s.Send(ctx, target, testMessage())
		cancel()
		if r.Outcome != model.OutcomeRetry || r.Err != "refusing to connect to a non-public address" {
			t.Errorf("%s: %+v", base, r)
		}
	}
	// A 302 is not followed. The guarded client cannot reach a loopback test server, so this
	// uses netguard's open client (same redirect policy) trusting the test certificate.
	var followed atomic.Int32
	dest := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		_, _ = w.Write([]byte(`{"data":{"status":"ok","id":"x"}}`))
	}))
	defer dest.Close()
	redir := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+expo.SendPath, http.StatusFound)
	}))
	defer redir.Close()
	pool := x509.NewCertPool()
	pool.AddCert(redir.Certificate())
	pool.AddCert(dest.Certificate())
	c := netguard.NewOpenClient()
	tr := c.Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	c.Transport = tr
	r := expo.New(expo.Options{BaseURL: redir.URL, Client: c}).Send(context.Background(), target, testMessage())
	if r.Outcome != model.OutcomeFailed || r.HTTPStatus != 302 || followed.Load() != 0 {
		t.Fatalf("%+v followed=%d", r, followed.Load())
	}
}

func TestValidBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://exp.host":           "https://exp.host",
		"https://exp.host/":          "https://exp.host",
		"https://exp.host:443":       "https://exp.host",
		"https://relay.example/expo": "https://relay.example/expo",
		" https://exp.host ":         "https://exp.host",
	} {
		if got, err := expo.ValidBaseURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"http://exp.host", "https://exp.host:8443", "https://u:p@exp.host", "https://exp.host/?a=1",
		"https://exp.host/#x", "https://", "exp.host", "", "ftp://exp.host", "https://exp.host?"} {
		if _, err := expo.ValidBaseURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestReceiptsCall(t *testing.T) {
	f := expotest.New()
	defer f.Close()
	s := fakeSender(f, "", nil)
	r := s.Send(context.Background(), target, testMessage())
	f.SetReceipt(r.Ref, expotest.ReceiptDeviceNotRegistered)
	got, err := s.Receipts(context.Background(), []string{r.Ref, "unknown-id"})
	if err != nil || len(got) != 1 || got[r.Ref].ErrorCode() != "DeviceNotRegistered" {
		t.Fatalf("%v %v", got, err)
	}
	f.ScriptReceipts(expotest.Reply{Status: 429})
	if _, err := s.Receipts(context.Background(), []string{r.Ref}); err == nil || err.Error() != "HTTP 429 TOO_MANY_REQUESTS" {
		t.Fatal(err)
	}
	req := f.ReceiptRequests()
	if len(req) != 2 || req[0].Err != nil || strings.Join(req[0].IDs, ",") != r.Ref+",unknown-id" {
		t.Fatalf("%+v", req)
	}
}
