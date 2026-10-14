package directpush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/config"
)

// Message is ntfy's JSON publish body (push.md 7.2).
type Message struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
	Actions  []Action `json:"actions,omitempty"`
}

// Action is an ntfy "view" action (direct pushes have no http actions: no Snooze or Seen).
type Action struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	Clear  bool   `json:"clear"`
}

const (
	maxTitle   = 80
	maxBody    = 180
	maxMessage = 4096
	// SendTimeout bounds one ntfy POST (machines.md 5.4).
	SendTimeout = 10 * time.Second
)

// cut shortens s to n runes with an ellipsis.
func cut(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// seatLabel is the slot's local label, else the email's local part, else "Unattributed"
// (machines.md 5.4).
func seatLabel(e Episode, labels map[string]string) string {
	if l := strings.TrimSpace(labels[e.Slot]); l != "" {
		return l
	}
	if i := strings.IndexByte(e.Email, '@'); i > 0 {
		return e.Email[:i]
	}
	if e.Email != "" {
		return e.Email
	}
	return "Unattributed"
}

// what is the trigger's title phrase (push.md 5.5).
func what(e Episode) string {
	switch e.Trigger {
	case TriggerNeedsPermission:
		return "needs permission"
	case TriggerAsked:
		switch {
		case e.Detail == "elicitation":
			return "is waiting for input"
		case e.Detail == "tool" && e.ToolName == "ExitPlanMode":
			return "plan ready"
		}
		return "asked a question"
	case TriggerFailed:
		switch e.Detail {
		case "rate_limit":
			return "rate limited"
		case "authentication_failed":
			return "login needed"
		case "billing_error":
			return "billing problem"
		case "":
			return "stopped"
		}
		return "stopped: " + e.Detail
	}
	return e.Trigger
}

func body(e Episode, machine string) string {
	switch e.Trigger {
	case TriggerNeedsPermission:
		if e.Notice != "" {
			return e.Notice + " · " + machine
		}
		if e.ToolName != "" {
			return e.ToolName + " · " + machine
		}
		return "Needs your permission · " + machine
	case TriggerAsked:
		if e.Question != "" {
			return e.Question
		}
		if l := firstLine(e.LastMsg); l != "" {
			return l
		}
		if e.Detail == "elicitation" && e.Notice != "" {
			return e.Notice
		}
		return "Waiting for your answer · " + machine
	}
	return machine
}

// MessageOptions are what Build needs besides the episode.
type MessageOptions struct {
	Topic     string
	Machine   string // machine name
	Labels    map[string]string
	PublicURL string // the policy's public_url, "" when unknown
	Private   bool
}

// Build makes the ntfy message for an episode (machines.md 5.4; push.md 5.5 and 7.2): title
// `<seat> · <project> · <what> · direct`, Open in Claude when the session has a Remote
// Control link, Open with the hub's deep link when a public URL is known, no Snooze/Seen.
// Private mode says only "1 session needs you" and links to the board's root.
func Build(e Episode, o MessageOptions) Message {
	m := Message{Topic: o.Topic, Priority: 4}
	switch e.Trigger {
	case TriggerNeedsPermission:
		m.Tags = []string{"lock"}
	case TriggerAsked:
		m.Tags = []string{"question"}
	case TriggerFailed:
		m.Tags = []string{"rotating_light"}
	}
	deep := ""
	if o.PublicURL != "" {
		deep = o.PublicURL + "/#/sessions/" + url.PathEscape(e.Session)
	}
	if o.Private {
		m.Title, m.Message = "Switchyard", "1 session needs you"
		if o.PublicURL != "" {
			m.Click = o.PublicURL + "/"
			m.Actions = []Action{{Action: "view", Label: "Open", URL: m.Click, Clear: true}}
		}
		return m
	}
	project := e.Project
	if project == "" {
		project = "session"
	}
	m.Title = cut(seatLabel(e, o.Labels)+" · "+project+" · "+what(e), maxTitle-len([]rune(" · direct"))) + " · direct"
	m.Message = cut(body(e, o.Machine), maxBody)
	m.Click = deep
	if e.RemoteURL != "" {
		m.Actions = append(m.Actions, Action{Action: "view", Label: "Open in Claude", URL: e.RemoteURL, Clear: true})
		if m.Click == "" {
			m.Click = e.RemoteURL
		}
	}
	if deep != "" {
		m.Actions = append(m.Actions, Action{Action: "view", Label: "Open", URL: deep, Clear: true})
	}
	return m
}

// TestMessage is `sy direct-push test`'s message (machines.md 5.2).
func TestMessage(topic, machine string) Message {
	return Message{Topic: topic, Title: "Switchyard", Message: "Switchyard direct push test from " + machine, Priority: 3, Tags: []string{"bell"}}
}

// SendResult is one ntfy POST's outcome. Error is short and never the reply body (push.md 7.3).
type SendResult struct {
	OK     bool
	Status int
	Error  string
}

// HTTPClient is the ntfy client: 10 s timeout, no redirects.
func HTTPClient() *http.Client {
	return &http.Client{Timeout: SendTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Send POSTs m to the server root (JSON publishing).
func Send(ctx context.Context, hc *http.Client, server, token string, m Message) SendResult {
	// Every send is checked, whatever the config file says: the topic and the Bearer token
	// must not cross a public network in cleartext.
	if err := CheckServer(server); err != nil {
		return SendResult{Error: "insecure or invalid server"}
	}
	if hc == nil {
		hc = HTTPClient()
	}
	b, err := json.Marshal(m)
	if err != nil {
		return SendResult{Error: "encode"}
	}
	for len(b) > maxMessage && len(m.Message) > 0 {
		r := []rune(m.Message)
		m.Message = cut(m.Message, len(r)/2)
		b, _ = json.Marshal(m)
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/", bytes.NewReader(b))
	if err != nil {
		return SendResult{Error: "bad server URL"}
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return SendResult{Error: transportError(err)}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return SendResult{OK: true}
	}
	return SendResult{Status: resp.StatusCode, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
}

func transportError(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "connection failed"
}

// ServerHost is the host of the ntfy server URL (for records and status; never the topic).
func ServerHost(server string) string {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return "ntfy"
	}
	return u.Host
}

// CheckServer validates a push.direct.server value: https, or http only for loopback,
// private and tailnet addresses (the `sy connect` rule, push.md 7.1). The rule lives in
// config so `sy config set` and a hand-edited file are held to it as well.
func CheckServer(server string) error { return config.CheckDirectServer(server) }
