package ntfy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/netguard"
)

const (
	maxRetryAfter = time.Hour
	maxErrBytes   = 200
)

// Options configure a Sender. All fields are optional.
type Options struct {
	// Client is the HTTP client for every target (tests); nil uses two clients that follow no
	// redirect: a dial-guarded one (public addresses only) for targets a phone registered
	// (DeviceID set), and an unguarded one for the owner's own targets (DeviceID empty), so a
	// self-hosted ntfy on the tailnet or loopback still works.
	Client *http.Client
	// SnoozeMin labels the Snooze button, read per message (hub setting snooze_min); nil
	// labels it "Snooze".
	SnoozeMin func() int
	// Now is the clock for Retry-After dates; nil is time.Now.
	Now func() time.Time
}

// Sender publishes pushes to ntfy servers. It implements model.Sender.
type Sender struct {
	o      Options
	client *http.Client // nil: pick per target
	guard  *http.Client
	open   *http.Client
}

var _ model.Sender = (*Sender)(nil)

// New builds a Sender.
func New(o Options) *Sender {
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Sender{o: o, client: o.Client}
	if s.client == nil {
		s.guard, s.open = netguard.NewClient(), netguard.NewOpenClient()
	}
	return s
}

// Kind implements model.Sender.
func (s *Sender) Kind() string { return model.KindNtfy }

// Send implements model.Sender: one POST of the JSON message to the server root, with
// `Authorization: Bearer <token>` when the target has one. The result follows push.md 7.3:
// 200 sent; 429 and 5xx retry (Retry-After honoured); 401, 403, 413 and anything else fail;
// transport errors retry. The token never appears in Result.Err.
func (s *Sender) Send(ctx context.Context, t model.Target, m model.Message) model.Result {
	server, topic := SplitEndpoint(t.Endpoint)
	if server == "" || !ValidTopic(topic) {
		return failed("bad ntfy target endpoint")
	}
	o := BuildOptions{Topic: topic}
	if s.o.SnoozeMin != nil {
		o.SnoozeMin = s.o.SnoozeMin()
	}
	body, err := Marshal(Build(m, o))
	if err != nil {
		return failed(err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/", bytes.NewReader(body))
	if err != nil {
		return failed("bad ntfy server URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if t.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+t.Secret)
	}
	client := s.client
	if client == nil {
		client = s.guard
		if t.DeviceID == "" {
			client = s.open
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return model.Result{Outcome: model.OutcomeRetry, Err: transportError(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrBytes))
	return mapResponse(resp.StatusCode, resp.Header.Get("Retry-After"), s.o.Now())
}

// mapResponse maps the status. The reply body is never kept: the server may be an internal
// service a phone pointed us at, and last_error is shown to the caller.
func mapResponse(status int, retryAfter string, now time.Time) model.Result {
	r := model.Result{HTTPStatus: status}
	switch {
	case status == http.StatusOK:
		r.Outcome = model.OutcomeSent
		return r
	case status == http.StatusTooManyRequests || status >= 500:
		r.Outcome = model.OutcomeRetry
		r.RetryAfter = parseRetryAfter(retryAfter, now)
	default: // 401, 403, 413 and anything else (400 bad request, 404, redirects)
		r.Outcome = model.OutcomeFailed
	}
	r.Err = "HTTP " + strconv.Itoa(status)
	return r
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0
		}
		if n > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		if d = t.Sub(now); d < 0 {
			d = 0
		}
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

func failed(msg string) model.Result { return model.Result{Outcome: model.OutcomeFailed, Err: msg} }

// transportError describes a connection failure without the URL (the topic is a secret on
// ntfy.sh).
func transportError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	if errors.Is(err, netguard.ErrBlocked) {
		return "refusing to connect to a non-public address"
	}
	// No resolver or socket detail: it would let a caller probe the hub's network.
	return "connection failed"
}
