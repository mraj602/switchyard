// Package expo delivers pushes to the phone app through Expo's push service (push.md 12):
// it builds the Expo message (12.2), posts it and maps the answer (12.3), polls the push
// receipts (12.4) and sends silent widget refresh pushes (12.9). It reaches the rest of the
// hub only through hub/store, hub/events and hub/push/{model,netguard,clock}.
//
// Construction in `sy hub serve` (wave 2, WP-I):
//
//	ex := expo.New(expo.Options{Store: st, Log: logger})            // a model.Sender
//	eng := engine.New(engine.Config{..., Senders: []model.Sender{wp, nf, ex}})
//	rc := expo.NewReceipts(expo.ReceiptsOptions{Store: st, Sender: ex, Clock: clk, Log: logger})
//	rf := expo.NewRefresher(expo.RefresherOptions{Store: st, Sender: ex, Broker: srv.Broker, Clock: clk, Log: logger})
//	rc.Start(); rf.Start()
//	... http.Server.Shutdown(...); rf.Stop(); rc.Stop()
package expo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// Limits and names of push.md 12.2 and phone.md P6.
const (
	// MaxMessageBytes is Expo's limit for one message (MessageTooBig above it).
	MaxMessageBytes = 4096
	// Category is the notification category with Open, Snooze and Seen (phone.md P6.2).
	Category = "sy_alert"
	// RefreshTTL is the ttl of a silent widget refresh (12.2).
	RefreshTTL = 15 * time.Minute
	// DefaultTTL applies when the engine gives none (6.4's longest).
	DefaultTTL = 6 * time.Hour
)

// Android channels of phone.md P6.1.
const (
	ChannelNeedsYou = "sy_needs_you"
	ChannelLimits   = "sy_limits"
	ChannelMachines = "sy_machines"
	ChannelTest     = "sy_test"
)

// tokenRE is push.md 12.7's Expo push token pattern.
var tokenRE = regexp.MustCompile(`^Expo(nent)?PushToken\[[A-Za-z0-9_-]{8,200}\]$`)

// ValidToken reports whether s is an Expo push token (push.md 12.7).
func ValidToken(s string) bool { return tokenRE.MatchString(s) }

// Message is one message to Expo's push service (push.md 12.2; phone/src/contract
// ExpoMessage). Field order is the wire order of the goldens.
type Message struct {
	To               string `json:"to"`
	Title            string `json:"title,omitempty"`
	Body             string `json:"body,omitempty"`
	Data             any    `json:"data"` // *model.Payload, or *RefreshData for a refresh
	CategoryID       string `json:"categoryId,omitempty"`
	ChannelID        string `json:"channelId,omitempty"`
	Sound            string `json:"sound,omitempty"`
	Badge            *int   `json:"badge,omitempty"`
	ContentAvailable bool   `json:"contentAvailable,omitempty"`
	Priority         string `json:"priority"`
	TTL              int64  `json:"ttl"`
}

// RefreshData is the data of a silent widget refresh (push.md 12.9): never shown.
type RefreshData struct {
	V        int        `json:"v"`
	Type     string     `json:"type"`
	NeedsYou int        `json:"needs_you"`
	TS       model.Time `json:"ts"`
}

// Build makes the Expo message for one target (push.md 12.2). A payload of type refresh
// gives a silent data message; anything else is shown, with the payload as data. When the
// payload is private the data has no remote_url (Expo, Apple and Google can read data).
func Build(to string, m model.Message) Message {
	p := m.Payload
	if p.Type == model.TypeRefresh {
		ts := p.TS
		if ts == 0 {
			ts = model.TimeOf(time.Now())
		}
		return Message{To: to, Data: &RefreshData{V: model.PayloadVersion, Type: model.TypeRefresh, NeedsYou: p.NeedsYou, TS: ts},
			ContentAvailable: true, Priority: "normal", TTL: int64(RefreshTTL / time.Second)}
	}
	if p.Private {
		p.RemoteURL = ""
	}
	if p.Alerts != nil {
		p.Alerts = append([]string(nil), p.Alerts...)
	}
	title := p.Title
	if title == "" {
		title = "Switchyard"
	}
	out := Message{To: to, Title: title, Body: p.Body, Data: &p, ChannelID: ChannelFor(p), Sound: "default",
		Priority: priorityFor(m.Urgency), TTL: ttlSeconds(m.TTL)}
	if a := p.Actions; a != nil && (a.Snooze != "" || a.Seen != "") {
		out.CategoryID = Category
	}
	if p.Type != model.TypeTest { // a test push does not know the needs-you count
		n := p.NeedsYou
		out.Badge = &n
	}
	return out
}

// ChannelFor is the Android channel of a payload (phone.md P6.1).
func ChannelFor(p model.Payload) string {
	switch p.Type {
	case model.TypeTest:
		return ChannelTest
	case model.TypeSummary:
		return ChannelNeedsYou
	}
	switch p.Trigger {
	case model.TriggerLimit80, model.TriggerLimit95, model.TriggerReset:
		return ChannelLimits
	case model.TriggerMachineSilent:
		return ChannelMachines
	}
	return ChannelNeedsYou
}

// priorityFor maps the Web Push urgency (6.4): high stays high, normal and low are normal.
func priorityFor(urgency string) string {
	if urgency == "high" {
		return "high"
	}
	return "normal"
}

func ttlSeconds(d time.Duration) int64 {
	if d <= 0 {
		d = DefaultTTL
	}
	return int64(d / time.Second)
}

// Marshal encodes the message. When it is over MaxMessageBytes it cuts the body (in both
// places), then drops alert ids from the end of data.alerts, then cuts the title, never the
// actions (push.md 12.2). It fails only when the message is too big without any text.
func Marshal(msg Message) ([]byte, error) {
	b, err := encode(msg)
	if err != nil {
		return nil, err
	}
	p, ok := msg.Data.(*model.Payload)
	if len(b) <= MaxMessageBytes || !ok {
		if len(b) > MaxMessageBytes {
			return nil, fmt.Errorf("expo: message is %d bytes (limit %d)", len(b), MaxMessageBytes)
		}
		return b, nil
	}
	cp := *p
	msg.Data = &cp
	cut := func(get func() string, set func(string)) error {
		for len(b) > MaxMessageBytes && get() != "" {
			s := get()
			// The text is in the message twice (top level and data); cut at least half the
			// excess from each copy, plus room for the ellipsis.
			n := len(s) - ((len(b)-MaxMessageBytes)+1)/2 - len("…")
			if n <= 0 {
				set("")
			} else {
				set(cutRunes(s, n) + "…")
			}
			if b, err = encode(msg); err != nil {
				return err
			}
		}
		return nil
	}
	if err := cut(func() string { return cp.Body }, func(s string) { cp.Body, msg.Body = s, s }); err != nil {
		return nil, err
	}
	for len(b) > MaxMessageBytes && len(cp.Alerts) > 0 {
		cp.Alerts = cp.Alerts[:len(cp.Alerts)-1]
		if len(cp.Alerts) == 0 {
			cp.Alerts = nil
		}
		if b, err = encode(msg); err != nil {
			return nil, err
		}
	}
	if err := cut(func() string { return cp.Title }, func(s string) {
		cp.Title, msg.Title = s, s
		if s == "" {
			msg.Title = "Switchyard"
		}
	}); err != nil {
		return nil, err
	}
	if len(b) > MaxMessageBytes {
		return nil, fmt.Errorf("expo: message is %d bytes without its text (limit %d)", len(b), MaxMessageBytes)
	}
	return b, nil
}

// encode is json.Marshal without HTML escaping (so "<" costs one byte, as Expo counts it)
// and without the trailing newline.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func cutRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
