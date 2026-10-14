package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Time is a millisecond timestamp that encodes as an RFC 3339 UTC string with ms, or null
// when zero (the same wire form as store.Time; kept separate so this package stays a leaf).
type Time int64

// TimeOf converts a time.Time; the zero time is Time(0).
func TimeOf(t time.Time) Time {
	if t.IsZero() {
		return 0
	}
	return Time(t.UnixMilli())
}

// Std converts back; Time(0) is the zero time.Time.
func (t Time) Std() time.Time {
	if t == 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(t)).UTC()
}

const timeLayout = "2006-01-02T15:04:05.000Z"

// MarshalJSON implements json.Marshaler.
func (t Time) MarshalJSON() ([]byte, error) {
	if t == 0 {
		return []byte("null"), nil
	}
	return []byte(`"` + time.UnixMilli(int64(t)).UTC().Format(timeLayout) + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler (null and "" give 0).
func (t *Time) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) || bytes.Equal(b, []byte(`""`)) {
		*t = 0
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("model: time: %w", err)
	}
	p, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("model: time: %w", err)
	}
	*t = Time(p.UnixMilli())
	return nil
}

// Trigger is a kind of alert condition (push.md 3.1). Unknown triggers must be ignored.
type Trigger string

// The triggers of push.md 3.1.
const (
	TriggerNeedsPermission Trigger = "needs_permission"
	TriggerAsked           Trigger = "asked"
	TriggerFailed          Trigger = "failed"
	TriggerLimit80         Trigger = "limit_80"
	TriggerLimit95         Trigger = "limit_95"
	TriggerReset           Trigger = "reset"
	TriggerMachineSilent   Trigger = "machine_silent"
)

// Triggers lists every trigger in display order.
var Triggers = []Trigger{TriggerNeedsPermission, TriggerAsked, TriggerFailed, TriggerLimit80, TriggerLimit95, TriggerReset, TriggerMachineSilent}

// Valid reports whether t is a trigger this build knows.
func (t Trigger) Valid() bool {
	for _, k := range Triggers {
		if k == t {
			return true
		}
	}
	return false
}

// Status is an alert's lifecycle state (push.md 4).
type Status string

// The alert statuses.
const (
	StatusPending   Status = "pending"
	StatusFired     Status = "fired"
	StatusSnoozed   Status = "snoozed"
	StatusSeen      Status = "seen"
	StatusCancelled Status = "cancelled"
	StatusClosed    Status = "closed"
	StatusSkipped   Status = "skipped"
)

// Open reports whether the alert may still push (pending, fired, snoozed).
func (s Status) Open() bool {
	return s == StatusPending || s == StatusFired || s == StatusSnoozed
}

// Reasons recorded in alert.reason (push.md 4 and the schema).
const (
	ReasonAnswered      = "answered"
	ReasonEnded         = "ended"
	ReasonSuperseded    = "superseded"
	ReasonStale         = "stale"
	ReasonRuleOff       = "rule_off"
	ReasonRemoteControl = "remote_control"
	ReasonBaseline      = "baseline"
	ReasonRecovered     = "recovered"
	ReasonResetPassed   = "reset_passed"
	ReasonDirect        = "direct"
)

// AlertBrief is the short form: the stream event, the Session.alert field and the top of
// Alert (api.yaml AlertBrief).
type AlertBrief struct {
	ID           string  `json:"id"`
	Trigger      Trigger `json:"trigger"`
	Status       Status  `json:"status"`
	Reason       *string `json:"reason"`
	DueAt        Time    `json:"due_at"`
	FiredAt      Time    `json:"fired_at"`
	FireCount    int     `json:"fire_count"`
	SnoozedUntil Time    `json:"snoozed_until"`
	SeenAt       Time    `json:"seen_at"`
}

// Alert is one episode's row as the API returns it (api.yaml Alert). The json:"-" fields are
// the engine's and the store's, never sent.
type Alert struct {
	AlertBrief
	Rule       *string    `json:"rule"`
	Session    *string    `json:"session"`
	Seat       *string    `json:"seat"`
	Machine    *string    `json:"machine"`
	WindowKey  *string    `json:"window_key"`
	Title      string     `json:"title"`
	Body       *string    `json:"body"`
	URL        string     `json:"url"`
	CreatedAt  Time       `json:"created_at"`
	SinceAt    Time       `json:"since_at"`
	EndedAt    Time       `json:"ended_at"`
	Deliveries []Delivery `json:"deliveries,omitempty"`

	Episode string `json:"-"` // dedupe key (push.md 3.2)
	SeenBy  string `json:"-"` // device id or "ntfy:<target id>"
	Facts   Facts  `json:"-"` // text inputs captured at creation (alert.facts)
}

// Facts is alert.facts: what the text of a push is rendered from, so it can be rendered
// without reading the session again (push.md 5.5, 5.8).
type Facts struct {
	Seat      string  `json:"seat,omitempty"`    // seat label
	Project   string  `json:"project,omitempty"` // session.project
	Machine   string  `json:"machine,omitempty"` // machine name
	State     string  `json:"state,omitempty"`   // session state at creation
	Detail    string  `json:"detail,omitempty"`  // session_state.detail
	Tool      string  `json:"tool,omitempty"`
	Question  string  `json:"question,omitempty"`
	Message   string  `json:"message,omitempty"` // notification or last message text, when kept
	Window    string  `json:"window,omitempty"`  // window key
	Pct       float64 `json:"pct,omitempty"`
	Peak      float64 `json:"peak,omitempty"`
	ResetsAt  Time    `json:"resets_at,omitempty"`
	RemoteURL string  `json:"remote_url,omitempty"`
	Sessions  int     `json:"sessions,omitempty"` // machine_silent: live sessions
	LastSeen  Time    `json:"last_seen,omitempty"`
}

// Delivery is one attempt to bring one alert to one target (api.yaml Delivery).
type Delivery struct {
	Target     string  `json:"target"`
	Kind       string  `json:"kind"`
	TargetName *string `json:"target_name"`
	Fire       int     `json:"fire"`
	Batch      *string `json:"batch"`
	Status     string  `json:"status"` // pending, held, sent, failed, skipped, gone
	Reason     *string `json:"reason"`
	Tries      int     `json:"tries"`
	HTTPStatus *int    `json:"http_status"`
	UpdatedAt  Time    `json:"updated_at"`
}

// RuleScope narrows a rule (api.yaml RuleScope). Empty lists mean "everything".
type RuleScope struct {
	Seats    []string `json:"seats,omitempty"`
	Projects []string `json:"projects,omitempty"`
	Machines []string `json:"machines,omitempty"`
	Windows  []string `json:"windows,omitempty"`
}

// Rule is one row of the rule table (api.yaml Rule).
type Rule struct {
	ID          string    `json:"id"`
	Trigger     Trigger   `json:"trigger"`
	Base        bool      `json:"base"`
	Enabled     bool      `json:"enabled"`
	DelayS      int       `json:"delay_s"`
	BypassQuiet bool      `json:"bypass_quiet"`
	Scope       RuleScope `json:"scope"`
	UpdatedAt   Time      `json:"updated_at"`
}

// RuleInput is the body of POST and PUT /v1/rules (api.yaml RuleInput); nil means "keep".
type RuleInput struct {
	Trigger     *Trigger   `json:"trigger,omitempty"`
	Enabled     *bool      `json:"enabled,omitempty"`
	DelayS      *int       `json:"delay_s,omitempty"`
	BypassQuiet *bool      `json:"bypass_quiet,omitempty"`
	Scope       *RuleScope `json:"scope,omitempty"`
}

// Tap values of Prefs.Tap.
const (
	TapAuto    = "auto"
	TapSession = "session"
	TapClaude  = "claude"
)

// QuietHours is push.md 5.2 (api.yaml QuietHours). Start and End are "HH:MM"; Start > End
// spans midnight; Start == End is off. Critical nil means true.
type QuietHours struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start"`
	End      string `json:"end"`
	TZ       string `json:"tz,omitempty"`
	Critical *bool  `json:"critical,omitempty"`
}

// LetCriticalThrough reports whether rules with bypass_quiet pass quiet hours.
func (q QuietHours) LetCriticalThrough() bool { return q.Critical == nil || *q.Critical }

// Prefs are the per-device choices (push.md 5.4; device.prefs, push_target.prefs).
type Prefs struct {
	Mute       []Trigger   `json:"mute"`
	QuietHours *QuietHours `json:"quiet_hours"` // nil: follow the hub
	Private    *bool       `json:"private"`     // nil: hub default
	Tap        string      `json:"tap"`         // TapAuto when empty
	TZ         *string     `json:"tz"`
	// Widgets asks for silent widget refresh pushes to this device's expo targets (push.md
	// 12.9); nil means false. Omitted when nil, so older prefs encode unchanged.
	Widgets *bool `json:"widgets,omitempty"`
}

// WidgetsOn reports whether the device wants widget refresh pushes.
func (p Prefs) WidgetsOn() bool { return p.Widgets != nil && *p.Widgets }

// Muted reports whether the device mutes t.
func (p Prefs) Muted(t Trigger) bool {
	for _, m := range p.Mute {
		if m == t {
			return true
		}
	}
	return false
}

// TapMode returns Tap with the default applied.
func (p Prefs) TapMode() string {
	switch p.Tap {
	case TapSession, TapClaude:
		return p.Tap
	}
	return TapAuto
}

// Channel kinds.
const (
	KindWebPush = "webpush"
	KindNtfy    = "ntfy"
	KindExpo    = "expo" // the store app through Expo's push service (push.md 12)
)

// Platforms of an expo target (TargetMeta.Platform).
const (
	PlatformIOS     = "ios"
	PlatformAndroid = "android"
)

// TargetMeta is push_target.meta (schema v6): facts about an expo target that are not
// secrets (push.md 12.7). Nil for webpush and ntfy targets.
type TargetMeta struct {
	Platform   string `json:"platform,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
	AppBuild   string `json:"app_build,omitempty"`
}

// Keys are a Web Push subscription's keys, base64url (push_target.keys).
type Keys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// Target is one place a push goes (push_target row), the engine's and senders' view. It is
// not the API shape (api.yaml PushTarget has host, status and token_set; WP-C maps it).
// Secret is the ntfy token and never serialises. For ntfy, Endpoint is "<server>/<topic>";
// for expo it is the Expo push token ("ExponentPushToken[…]").
type Target struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	DeviceID    string      `json:"device_id,omitempty"` // empty: a topic the owner added for the hub
	Name        string      `json:"name,omitempty"`
	Endpoint    string      `json:"endpoint"`
	Keys        *Keys       `json:"keys,omitempty"`
	Secret      string      `json:"-"`
	Prefs       *Prefs      `json:"prefs,omitempty"` // targets without a device only
	Meta        *TargetMeta `json:"meta,omitempty"`  // expo only (schema v6)
	Enabled     bool        `json:"enabled"`
	CreatedAt   Time        `json:"created_at"`
	UpdatedAt   Time        `json:"updated_at"`
	LastOKAt    Time        `json:"last_ok_at"`
	LastError   string      `json:"last_error,omitempty"`
	LastErrorAt Time        `json:"last_error_at"`
	FailCount   int         `json:"fail_count"`
	GoneAt      Time        `json:"gone_at"`
}

// Push types of Payload.Type.
const (
	TypeAlert   = "alert"
	TypeSummary = "summary"
	TypeTest    = "test"
	// TypeRefresh is the silent widget refresh of the expo channel (push.md 12.9): never
	// shown, never an alert, no delivery row. Its data is {"v", "type", "needs_you", "ts"}.
	TypeRefresh = "refresh"
)

// Actions are the action tokens of a push (push.md 8); absent for tests and summaries.
type Actions struct {
	Snooze string `json:"snooze,omitempty"`
	Seen   string `json:"seen,omitempty"`
}

// MaxPayloadAlerts is the most alert ids a push payload lists; a summary of more names the
// first ones and the app refetches the rest.
const MaxPayloadAlerts = 20

// Payload is the plaintext of a Web Push and the contract with the service worker
// (push.md 6.5, pwa.md 5). Field order and tags are exact.
type Payload struct {
	V         int      `json:"v"`
	Type      string   `json:"type"`
	Alert     string   `json:"alert,omitempty"`
	Alerts    []string `json:"alerts,omitempty"`
	Trigger   Trigger  `json:"trigger,omitempty"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Tag       string   `json:"tag"`
	URL       string   `json:"url"`
	RemoteURL string   `json:"remote_url,omitempty"`
	Tap       string   `json:"tap"`
	Session   string   `json:"session,omitempty"`
	NeedsYou  int      `json:"needs_you"`
	Actions   *Actions `json:"actions,omitempty"`
	Private   bool     `json:"private"`
	TS        Time     `json:"ts"`
}

// PayloadVersion is Payload.V.
const PayloadVersion = 1

// Message is what a Sender is asked to deliver to one target: the payload plus the delivery
// parameters the engine decides (push.md 6.4, 7.2). ntfy builds its JSON from the same
// payload; its tags follow Payload.Trigger and Payload.Type.
type Message struct {
	Payload Payload
	// TTL is how long a push service may hold the message (6.4 table).
	TTL time.Duration
	// Urgency is "high", "normal" or "low" (Web Push Urgency header).
	Urgency string
	// Priority is the ntfy priority 1-5 (7.2).
	Priority int
	// HubURL is the hub's public origin; ntfy needs absolute URLs for click and actions.
	HubURL string
}

// Outcome is the result class of one send (push.md 5.6, 10; api.yaml PushTestResult).
type Outcome string

// The outcomes.
const (
	OutcomeSent   Outcome = "sent"
	OutcomeRetry  Outcome = "retry"
	OutcomeGone   Outcome = "gone"
	OutcomeFailed Outcome = "failed"
)

// Result is what a Sender returns (push.md 10).
type Result struct {
	Outcome    Outcome
	HTTPStatus int
	RetryAfter time.Duration // from Retry-After, 0 if none
	Err        string        // first 200 bytes of the reply or the transport error
	// Ref is the push service's id for an accepted message: the Expo push ticket id
	// (push.md 12.3), which the engine stores in delivery.ticket. Empty for other senders.
	Ref string
}
