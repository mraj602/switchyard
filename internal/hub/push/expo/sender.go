package expo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/netguard"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// Expo's push API (push.md 12.1).
const (
	DefaultBaseURL = "https://exp.host"
	SendPath       = "/--/api/v2/push/send"
	ReceiptsPath   = "/--/api/v2/push/getReceipts"
)

// Error texts of push.md 12.3 (Result.Err; never message text).
const (
	ErrTextCredentials = "Expo has no push credentials for this app (see phone/README)"
	ErrTextAccessToken = "Expo access token missing or wrong"
)

const (
	maxRetryAfter  = time.Hour
	rateRetry      = 60 * time.Second
	maxAnswerBytes = 1 << 20
)

// Options configure a Sender. All fields are optional.
type Options struct {
	// Store holds the channel's base_url and the access token (push.md 12.5). They are read
	// once and cached until Reload. Nil uses BaseURL and AccessToken below.
	Store *store.Store
	// BaseURL and AccessToken are used when Store is nil (tests). "" BaseURL is the default.
	BaseURL     string
	AccessToken string
	// Client is the HTTP client; nil is netguard.NewClient() (public addresses only, no
	// redirects, 10 s timeout). The fakeclock build's SY_TEST_EXPO_DIAL passes its own.
	Client *http.Client
	// Now is the clock for Retry-After dates; nil is time.Now.
	Now func() time.Time
	// Log gets a line when Expo reports a bug of ours (MessageTooBig); nil discards. The
	// line names the target, never the message.
	Log *log.Logger
}

// Sender delivers pushes through Expo's push service. It implements model.Sender.
type Sender struct {
	o      Options
	client *http.Client

	mu  sync.Mutex
	cfg *config // nil: read on next use
}

type config struct {
	base  string // without a trailing slash
	token string
}

var _ model.Sender = (*Sender)(nil)

// New builds a Sender.
func New(o Options) *Sender {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	c := o.Client
	if c == nil {
		c = netguard.NewClient()
	}
	return &Sender{o: o, client: c}
}

// Kind implements model.Sender.
func (s *Sender) Kind() string { return model.KindExpo }

// Reload drops the cached base URL and access token, so the next send reads them again.
// PUT /v1/push/channels/expo calls it.
func (s *Sender) Reload() {
	s.mu.Lock()
	s.cfg = nil
	s.mu.Unlock()
}

func (s *Sender) config(ctx context.Context) (config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg != nil {
		return *s.cfg, nil
	}
	c := config{base: s.o.BaseURL, token: s.o.AccessToken}
	if s.o.Store != nil {
		ec, err := s.o.Store.ExpoConfig(ctx)
		if err != nil {
			return config{}, err
		}
		c = config{base: ec.BaseURL, token: ec.AccessToken}
	}
	if c.base == "" {
		c.base = DefaultBaseURL
	}
	c.base = strings.TrimRight(c.base, "/")
	s.cfg = &c
	return c, nil
}

// Send implements model.Sender: one POST of one message (push.md 12.2), mapped as 12.3.
func (s *Sender) Send(ctx context.Context, t model.Target, m model.Message) model.Result {
	if !ValidToken(t.Endpoint) {
		return failed(0, "bad Expo push token")
	}
	body, err := Marshal(Build(t.Endpoint, m))
	if err != nil {
		return failed(0, "message does not fit 4096 bytes")
	}
	status, hdr, answer, res, ok := s.post(ctx, SendPath, body)
	if !ok {
		return res
	}
	r := mapSend(status, hdr.Get("Retry-After"), answer, s.o.Now())
	if r.Err == "MessageTooBig" {
		s.o.Log.Printf("expo: MessageTooBig for target %s: a bug, the builder keeps messages under %d bytes", t.ID, MaxMessageBytes)
	}
	return r
}

// post sends one JSON request to path. ok is false when there is no HTTP answer; res then
// says why (a retry).
func (s *Sender) post(ctx context.Context, path string, body []byte) (status int, hdr http.Header, answer []byte, res model.Result, ok bool) {
	cfg, err := s.config(ctx)
	if err != nil {
		return 0, nil, nil, model.Result{Outcome: model.OutcomeRetry, Err: "cannot read the Expo channel settings"}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, failed(0, "bad Expo base URL"), false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if cfg.token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, nil, model.Result{Outcome: model.OutcomeRetry, Err: transportError(err)}, false
	}
	defer resp.Body.Close()
	answer, err = io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil || ctx.Err() != nil {
		// A deadline or a dropped connection mid-answer is a retry, not an unreadable answer.
		if err == nil {
			err = ctx.Err()
		}
		return 0, nil, nil, model.Result{Outcome: model.OutcomeRetry, Err: transportError(err)}, false
	}
	return resp.StatusCode, resp.Header, answer, model.Result{}, true
}

// Ticket is one entry of Expo's send answer (and the shape of a receipt).
type Ticket struct {
	Status  string `json:"status"` // ok, error
	ID      string `json:"id,omitempty"`
	Message string `json:"message,omitempty"` // may hold the push token or text: never kept
	Details *struct {
		Error string `json:"error,omitempty"`
	} `json:"details,omitempty"`
}

// ErrorCode is the ticket's details.error, as a safe code ("" when none).
func (t Ticket) ErrorCode() string {
	if t.Details == nil {
		return ""
	}
	return SafeCode(t.Details.Error)
}

// apiError is one entry of a top-level errors array.
type apiError struct {
	Code string `json:"code"`
}

type sendAnswer struct {
	Data   json.RawMessage `json:"data"`
	Errors []apiError      `json:"errors"`
}

var codeRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// SafeCode returns code when it looks like an Expo error code, else "unknown": a code is
// echoed into last_error, so nothing else from the answer may pass.
func SafeCode(code string) string {
	if code == "" {
		return ""
	}
	if codeRE.MatchString(code) {
		return code
	}
	return "unknown"
}

var ticketIDRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// mapSend is push.md 12.3. The answer body is parsed for codes and the ticket id only.
func mapSend(status int, retryAfter string, answer []byte, now time.Time) model.Result {
	r := model.Result{HTTPStatus: status}
	var ans sendAnswer
	parsed := json.Unmarshal(answer, &ans) == nil
	code := ""
	if parsed && len(ans.Errors) > 0 {
		code = SafeCode(ans.Errors[0].Code)
	}
	switch {
	case status == http.StatusOK:
		if !parsed {
			r.Outcome, r.Err = model.OutcomeFailed, "unreadable answer from Expo"
			return r
		}
		if code != "" {
			if strings.HasPrefix(code, "PUSH_TOO_MANY_") || code == "TOO_MANY_REQUESTS" {
				r.Outcome, r.Err = model.OutcomeRetry, "Expo: "+code
				r.RetryAfter = parseRetryAfter(retryAfter, now)
				return r
			}
			r.Outcome, r.Err = model.OutcomeFailed, "Expo: "+code
			return r
		}
		t, ok := firstTicket(ans.Data)
		if !ok {
			r.Outcome, r.Err = model.OutcomeFailed, "no ticket in Expo's answer"
			return r
		}
		return mapTicket(r, t)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		r.Outcome, r.Err = model.OutcomeFailed, ErrTextAccessToken
	case status == http.StatusTooManyRequests || status >= 500:
		r.Outcome = model.OutcomeRetry
		r.RetryAfter = parseRetryAfter(retryAfter, now)
		r.Err = withCode("HTTP "+strconv.Itoa(status), code)
	default: // 400, 413, redirects and anything else
		r.Outcome = model.OutcomeFailed
		r.Err = withCode("HTTP "+strconv.Itoa(status), code)
	}
	return r
}

func withCode(s, code string) string {
	if code != "" {
		return s + " " + code
	}
	return s
}

// mapTicket maps one ticket of a 200 answer (push.md 12.3).
func mapTicket(r model.Result, t Ticket) model.Result {
	if t.Status == "ok" {
		r.Outcome = model.OutcomeSent
		if ticketIDRE.MatchString(t.ID) {
			r.Ref = t.ID
		}
		return r
	}
	switch code := t.ErrorCode(); code {
	case store.ReceiptDeviceNotRegistered:
		r.Outcome, r.Err = model.OutcomeGone, code
	case "MessageRateExceeded":
		r.Outcome, r.Err, r.RetryAfter = model.OutcomeRetry, code, rateRetry
	case "":
		r.Outcome, r.Err = model.OutcomeFailed, "Expo: error"
	default:
		r.Outcome, r.Err = model.OutcomeFailed, ErrorText(code)
	}
	return r
}

// ErrorText is the stored error text of an Expo error code (push.md 12.3), shared by
// ticket and receipt errors so the doctor's fix hints match both: credential errors get
// ErrTextCredentials, MessageTooBig stays a bare code, anything else is "Expo: <code>".
func ErrorText(code string) string {
	switch code {
	case "InvalidCredentials", "MismatchSenderId":
		return ErrTextCredentials
	case "MessageTooBig":
		return code
	}
	return "Expo: " + code
}

// firstTicket reads data as one ticket (a single-message request) or a list of them.
func firstTicket(data json.RawMessage) (Ticket, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return Ticket{}, false
	}
	if data[0] == '[' {
		var ts []Ticket
		if json.Unmarshal(data, &ts) != nil || len(ts) == 0 {
			return Ticket{}, false
		}
		return ts[0], ts[0].Status != ""
	}
	var t Ticket
	if json.Unmarshal(data, &t) != nil {
		return Ticket{}, false
	}
	return t, t.Status != ""
}

// Receipts asks Expo for the receipts of up to 1000 ticket ids (push.md 12.4). Ids Expo does
// not know are missing from the map.
func (s *Sender) Receipts(ctx context.Context, ids []string) (map[string]Ticket, error) {
	body, err := json.Marshal(map[string][]string{"ids": ids})
	if err != nil {
		return nil, err
	}
	status, _, answer, res, ok := s.post(ctx, ReceiptsPath, body)
	if !ok {
		return nil, errors.New(res.Err)
	}
	var ans struct {
		Data   map[string]Ticket `json:"data"`
		Errors []apiError        `json:"errors"`
	}
	if json.Unmarshal(answer, &ans) != nil {
		return nil, fmt.Errorf("HTTP %d: unreadable answer", status)
	}
	if status != http.StatusOK || len(ans.Errors) > 0 {
		code := ""
		if len(ans.Errors) > 0 {
			code = SafeCode(ans.Errors[0].Code)
		}
		return nil, errors.New(withCode("HTTP "+strconv.Itoa(status), code))
	}
	return ans.Data, nil
}

// ValidBaseURL checks a channel base_url (push.md 12.5): https on port 443, a host, no user
// info, query or fragment. It returns the URL without a trailing slash.
func ValidBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil:
		return "", errors.New("base_url is not a URL")
	case u.Scheme != "https":
		return "", errors.New("base_url must be https://")
	case u.Host == "" || u.Hostname() == "":
		return "", errors.New("base_url needs a host")
	case u.User != nil:
		return "", errors.New("base_url must not carry user info")
	case u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return "", errors.New("base_url must not have a query or fragment")
	case u.Port() != "" && u.Port() != "443":
		return "", errors.New("base_url must be on port 443")
	}
	out := "https://" + u.Hostname()
	if strings.Contains(u.Hostname(), ":") {
		out = "https://[" + u.Hostname() + "]"
	}
	return strings.TrimRight(out+u.EscapedPath(), "/"), nil
}

func failed(status int, msg string) model.Result {
	return model.Result{Outcome: model.OutcomeFailed, HTTPStatus: status, Err: msg}
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
	return min(d, maxRetryAfter)
}

// transportError describes a connection failure without the URL or resolver details.
func transportError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, netguard.ErrBlocked):
		return "refusing to connect to a non-public address"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	}
	return "connection failed"
}
