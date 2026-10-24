package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/version"
)

// Limits of one batch (outbox.md section 5; the hub allows 1000 records and 8 MiB).
const (
	MaxBatchRecords = 500
	MaxBatchBytes   = 4 << 20
	// GzipOver is the body size above which the request is gzip-compressed.
	GzipOver = 1 << 10
	// TokenFile is the name of the device token inside the state folder (tokens.md).
	TokenFile = "token"
)

// Client talks to one hub with one device token.
type Client struct {
	BaseURL string // e.g. http://127.0.0.1:8787, no trailing slash needed
	Token   string
	HTTP    *http.Client // nil means a client with a 20 s timeout
}

// New returns a client for baseURL.
func New(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, Token: token}
}

// HTTPClient is the client every request of c goes through, with the redirect guard.
func (c *Client) HTTPClient() *http.Client { return c.http() }

func (c *Client) http() *http.Client {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	// The Authorization header must never follow a redirect from https to plain http (Go
	// keeps it for a same-host redirect). A copy leaves the caller's client alone.
	out := *hc
	prev := out.CheckRedirect
	out.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("refusing a redirect from https to plain http")
		}
		if prev != nil {
			return prev(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &out
}

func (c *Client) endpoint(p string) string { return strings.TrimRight(c.BaseURL, "/") + p }

// StatusError is a reply with a status the client does not treat as success.
type StatusError struct {
	Status     int
	Code       string // the hub's error code, when the body had one
	Message    string
	RetryAfter time.Duration // from the Retry-After header, 0 if absent
}

func (e *StatusError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("hub answered %d: %s", e.Status, msg)
}

// IngestReply is the 200 body of POST /v1/ingest.
type IngestReply struct {
	Accepted   []string   `json:"accepted"`
	Duplicates []string   `json:"duplicates"`
	Rejected   []Rejected `json:"rejected"`
	// Hub is what the hub reads (machines.md 7.2); nil from a hub that predates it and
	// reads record v 1.
	Hub *HubInfo `json:"hub,omitempty"`
}

// HubInfo is `hub` in an ingest reply (api.yaml IngestReply.hub) and what the drain `start`
// frame carries: the hub's version and the highest record v it types.
type HubInfo struct {
	Version    string `json:"version"`
	RecordVMax int    `json:"record_v_max"`
}

// DefaultRecordVMax is what a hub that does not say reads (an older hub): record v 1.
const DefaultRecordVMax = 1

// VMax is the highest record v the hub reads, DefaultRecordVMax when unknown (nil receiver
// or a zero field).
func (h *HubInfo) VMax() int {
	if h == nil || h.RecordVMax < 1 {
		return DefaultRecordVMax
	}
	return h.RecordVMax
}

// Rejected is one record the hub refused for good.
type Rejected struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Health is the body of GET /health.
type Health struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Schema  int    `json:"schema"`
	Time    string `json:"time"`

	// machines.md 7.2; zero/nil from older hubs.
	RecordVMax int           `json:"record_v_max,omitempty"`
	BatchV     []int         `json:"batch_v,omitempty"`
	DrainProto int           `json:"drain_proto,omitempty"`
	Frozen     *HealthFrozen `json:"frozen,omitempty"`
}

// HealthFrozen is /health's `frozen` object: the hub is frozen for a move. Informational:
// the client never follows MovedTo by itself (machines.md 8.3).
type HealthFrozen struct {
	Since   string  `json:"since"`
	MovedTo *string `json:"moved_to"`
}

// VMax is the highest record v the hub reads, DefaultRecordVMax when /health does not say.
func (h Health) VMax() int { return (&HubInfo{RecordVMax: h.RecordVMax}).VMax() }

// Health calls GET /health (no token needed).
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/health"), nil)
	if err != nil {
		return h, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return h, statusError(resp, body)
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return h, fmt.Errorf("not a Watchyard hub (bad /health body): %w", err)
	}
	if !h.OK {
		return h, errors.New("hub reports it is not ok")
	}
	return h, nil
}

// Ingest uploads records (each one a complete JSON object, sent as stored) in one batch.
// backlog is the number of records still queued locally, sent as X-Sy-Backlog (negative
// means unknown). A non-200 answer is returned as *StatusError.
func (c *Client) Ingest(ctx context.Context, records [][]byte, backlog int) (*IngestReply, error) {
	var buf bytes.Buffer
	buf.WriteString(`{"v":1,"client":`)
	cl, _ := json.Marshal("wy/" + version.Version)
	buf.Write(cl)
	buf.WriteString(`,"records":[`)
	for i, r := range records {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(r)
	}
	buf.WriteString(`]}`)
	body := buf.Bytes()
	encoding := ""
	if len(body) > GzipOver {
		var z bytes.Buffer
		zw := gzip.NewWriter(&z)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body, encoding = z.Bytes(), "gzip"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/v1/ingest"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wy/"+version.Version)
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	if backlog >= 0 {
		req.Header.Set("X-Sy-Backlog", strconv.Itoa(backlog))
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp, data)
	}
	var out IngestReply
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unreadable ingest reply: %w", err)
	}
	return &out, nil
}

func statusError(resp *http.Response, body []byte) *StatusError {
	e := &StatusError{Status: resp.StatusCode}
	var eb struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(body, &eb) == nil {
		e.Message, e.Code = eb.Error, eb.Code
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		}
	}
	return e
}

// ReadToken reads <stateDir>/token: one line, whitespace trimmed. A missing file returns
// os.ErrNotExist.
func ReadToken(stateDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, TokenFile))
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New("token file is empty")
	}
	return t, nil
}

// CheckURL validates a hub URL for `wy connect` and returns it normalised (scheme and
// host only). Plain http is accepted for loopback and Tailscale (*.ts.net names, 100.64.0.0/10
// addresses), which WireGuard already encrypts; a single-label name is not (it could be any LAN
// name), so use the full MagicDNS name; any
// other http host needs insecureLAN. The warning, when not empty, is for the user.
func CheckURL(raw string, insecureLAN bool) (norm, warning string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("%q is not a hub URL (want http(s)://host[:port])", raw)
	}
	if p := strings.Trim(u.Path, "/"); p != "" || u.RawQuery != "" || u.User != nil {
		return "", "", fmt.Errorf("%q: the hub URL has no path, query or credentials", raw)
	}
	norm = u.Scheme + "://" + u.Host
	if u.Scheme == "http" {
		host := u.Hostname()
		switch {
		case isLoopback(host):
		case isTailscale(host):
			warning = "plain http over Tailscale is encrypted by WireGuard; use `tailscale serve` for HTTPS if you want browsers to trust it"
		case insecureLAN:
			warning = "plain http: records and the token cross the network unencrypted"
		default:
			return "", "", fmt.Errorf("%s is plain http to a non-loopback host; use https, a Tailscale address, or --insecure-lan", norm)
		}
	}
	return norm, warning, nil
}
