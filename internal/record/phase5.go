package record

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Phase 5 record kinds (record.md 4.8, 4.9; machines.md 5.5, 6.3).
const (
	KindMachineInfo = "machine_info"
	KindDirectPush  = "direct_push"
)

// MaxVersion is the highest record `v` this package writes and the hub's parsers read
// (machines.md 7.1, 7.2). All kinds are v1 in phase 5. A client never sends a record above
// the hub's own record_v_max; a hub reads every older v forever.
const MaxVersion = 1

// Reach values of machine_info.reach (machines.md 3). ReachNone means no hub is configured.
const (
	ReachDirect  = "direct"
	ReachForward = "forward"
	ReachPull    = "pull"
	ReachNone    = "none"
)

// Triggers a direct push can cover (machines.md 5.1).
const (
	DirectTriggerNeedsPermission = "needs_permission"
	DirectTriggerAsked           = "asked"
	DirectTriggerFailed          = "failed"
)

// DirectChannelNtfy is the only direct push channel (Web Push needs the hub's VAPID keys).
const DirectChannelNtfy = "ntfy"

var (
	directSessionID = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,63}$`)
	episodeTail     = regexp.MustCompile(`^[0-9]{1,16}$`)
)

// OutboxHealth is machine_info.outbox (record.md 4.8). OldestUnflushed is an RFC 3339 time
// or nil (written as null) when nothing is waiting.
type OutboxHealth struct {
	Backlog         int64   `json:"backlog"`
	UnflushedBytes  int64   `json:"unflushed_bytes"`
	OldestUnflushed *string `json:"oldest_unflushed"`
	Held            int64   `json:"held"`
	RejectedTotal   int64   `json:"rejected_total"`
	CorruptTotal    int64   `json:"corrupt_total"`
	CapDroppedTotal int64   `json:"cap_dropped_total"`
	Extra           Extra   `json:"-"`
}

type outboxHealthAlias OutboxHealth

func (p OutboxHealth) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(outboxHealthAlias(p), p.Extra)
}
func (p *OutboxHealth) UnmarshalJSON(b []byte) error {
	var a outboxHealthAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = OutboxHealth(a)
	return nil
}

// DirectPushState is machine_info.direct_push. Never the topic or token.
type DirectPushState struct {
	Enabled    bool    `json:"enabled"`
	ServerHost string  `json:"server_host,omitempty"`
	Private    bool    `json:"private"`
	PolicyAt   *string `json:"policy_at"` // RFC 3339 of the cached policy, or null
	Extra      Extra   `json:"-"`
}

type directPushStateAlias DirectPushState

func (p DirectPushState) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(directPushStateAlias(p), p.Extra)
}
func (p *DirectPushState) UnmarshalJSON(b []byte) error {
	var a directPushStateAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = DirectPushState(a)
	return nil
}

// TimerState is machine_info.timer, as sy doctor sees it. Linger is Linux only (nil elsewhere).
type TimerState struct {
	Active bool  `json:"active"`
	Linger *bool `json:"linger,omitempty"`
	Extra  Extra `json:"-"`
}

type timerStateAlias TimerState

func (p TimerState) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(timerStateAlias(p), p.Extra)
}
func (p *TimerState) UnmarshalJSON(b []byte) error {
	var a timerStateAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = TimerState(a)
	return nil
}

// UpdateState is machine_info.update, from `sy update --check` when it ran.
type UpdateState struct {
	LastCheck string `json:"last_check,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Extra     Extra  `json:"-"`
}

type updateStateAlias UpdateState

func (p UpdateState) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(updateStateAlias(p), p.Extra)
}
func (p *UpdateState) UnmarshalJSON(b []byte) error {
	var a updateStateAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = UpdateState(a)
	return nil
}

// MachineInfoPayload is the payload of a machine_info record (record.md 4.8).
type MachineInfoPayload struct {
	OS            string          `json:"os"`
	Arch          string          `json:"arch"`
	Sy            string          `json:"sy"`
	RecordV       int             `json:"record_v"`
	Reach         string          `json:"reach"`
	Dial          string          `json:"dial,omitempty"` // "tcp" or "unix", never the address
	Outbox        OutboxHealth    `json:"outbox"`
	DirectPush    DirectPushState `json:"direct_push"`
	Timer         *TimerState     `json:"timer,omitempty"`
	ClaudeVersion string          `json:"claude_version,omitempty"`
	Update        *UpdateState    `json:"update,omitempty"`
	// Name is the machine's own name (<state>/machine.json, `sy machine rename`). Optional:
	// older clients leave it out and a hub that does not know it keeps it in info only.
	Name  string `json:"name,omitempty"`
	Extra Extra  `json:"-"`
}

type machineInfoAlias MachineInfoPayload

func (p MachineInfoPayload) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(machineInfoAlias(p), p.Extra)
}
func (p *MachineInfoPayload) UnmarshalJSON(b []byte) error {
	var a machineInfoAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = MachineInfoPayload(a)
	return nil
}

// Validate checks the required fields and value sets of record.md 4.8. The hub uses it to
// decide typed vs raw; a failure is a `bad_payload` for a v1 record.
func (p *MachineInfoPayload) Validate() error {
	switch {
	case p.OS == "":
		return fmt.Errorf("machine_info: os is required")
	case p.Arch == "":
		return fmt.Errorf("machine_info: arch is required")
	case p.Sy == "":
		return fmt.Errorf("machine_info: sy is required")
	case p.RecordV < 1:
		return fmt.Errorf("machine_info: record_v must be at least 1")
	}
	switch p.Reach {
	case ReachDirect, ReachForward, ReachPull, ReachNone:
	default:
		return fmt.Errorf("machine_info: bad reach %q", p.Reach)
	}
	switch p.Dial {
	case "", "tcp", "unix":
	default:
		return fmt.Errorf("machine_info: bad dial %q (tcp, unix or absent)", p.Dial)
	}
	o := p.Outbox
	if o.Backlog < 0 || o.UnflushedBytes < 0 || o.Held < 0 || o.RejectedTotal < 0 || o.CorruptTotal < 0 || o.CapDroppedTotal < 0 {
		return fmt.Errorf("machine_info: outbox counters must not be negative")
	}
	if o.OldestUnflushed != nil {
		if _, err := time.Parse(time.RFC3339Nano, *o.OldestUnflushed); err != nil {
			return fmt.Errorf("machine_info: bad outbox.oldest_unflushed %q", *o.OldestUnflushed)
		}
	}
	if p.DirectPush.PolicyAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *p.DirectPush.PolicyAt); err != nil {
			return fmt.Errorf("machine_info: bad direct_push.policy_at %q", *p.DirectPush.PolicyAt)
		}
	}
	return nil
}

// NewMachineInfo builds a machine_info record. It has no slot and no seat (record.md 4.8).
func NewMachineInfo(m Meta, p MachineInfoPayload) (*Record, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	m.Slot, m.Seat = "", nil
	r := New(KindMachineInfo, m)
	return r, r.SetPayload(p)
}

// DirectPushPayload is the payload of a direct_push record (record.md 4.9).
type DirectPushPayload struct {
	Episode    string `json:"episode"` // s:<session>:<state>:<since_at ms>
	Trigger    string `json:"trigger"`
	SessionID  string `json:"session_id"`
	Since      string `json:"since"`   // RFC 3339 ts of the record that entered the state
	Record     string `json:"record"`  // its id
	SentAt     string `json:"sent_at"` // RFC 3339
	Channel    string `json:"channel"`
	ServerHost string `json:"server_host"`
	OK         bool   `json:"ok"`
	Status     int    `json:"status,omitempty"`
	Error      string `json:"error,omitempty"`
	Private    *bool  `json:"private,omitempty"`
	Extra      Extra  `json:"-"`
}

type directPushAlias DirectPushPayload

func (p DirectPushPayload) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(directPushAlias(p), p.Extra)
}
func (p *DirectPushPayload) UnmarshalJSON(b []byte) error {
	var a directPushAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = DirectPushPayload(a)
	return nil
}

// Validate checks record.md 4.9. The episode must be a session episode of this session and
// trigger: `s:<session_id>:<trigger>:<digits>` (push.md 3.2).
func (p *DirectPushPayload) Validate() error {
	switch p.Trigger {
	case DirectTriggerNeedsPermission, DirectTriggerAsked, DirectTriggerFailed:
	default:
		return fmt.Errorf("direct_push: bad trigger %q", p.Trigger)
	}
	if !directSessionID.MatchString(p.SessionID) {
		return fmt.Errorf("direct_push: bad session_id %q", p.SessionID)
	}
	prefix := "s:" + p.SessionID + ":" + p.Trigger + ":"
	if !strings.HasPrefix(p.Episode, prefix) || !episodeTail.MatchString(p.Episode[len(prefix):]) {
		return fmt.Errorf("direct_push: episode %q is not %s<since ms>", p.Episode, prefix)
	}
	if _, err := strconv.ParseInt(p.Episode[len(prefix):], 10, 64); err != nil {
		return fmt.Errorf("direct_push: bad episode %q", p.Episode)
	}
	if _, err := time.Parse(time.RFC3339Nano, p.Since); err != nil {
		return fmt.Errorf("direct_push: bad since %q", p.Since)
	}
	if !idPattern.MatchString(p.Record) {
		return fmt.Errorf("direct_push: bad record id %q", p.Record)
	}
	if _, err := time.Parse(time.RFC3339Nano, p.SentAt); err != nil {
		return fmt.Errorf("direct_push: bad sent_at %q", p.SentAt)
	}
	if p.Channel != DirectChannelNtfy {
		return fmt.Errorf("direct_push: bad channel %q", p.Channel)
	}
	if p.ServerHost == "" {
		return fmt.Errorf("direct_push: server_host is required")
	}
	if p.OK && (p.Status != 0 || p.Error != "") {
		return fmt.Errorf("direct_push: status and error belong to a failed push")
	}
	return nil
}

// NewDirectPush builds a direct_push record. `slot` is kept when m has one; it never has a seat.
func NewDirectPush(m Meta, p DirectPushPayload) (*Record, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	m.Seat = nil
	r := New(KindDirectPush, m)
	return r, r.SetPayload(p)
}
