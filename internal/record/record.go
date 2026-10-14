package record

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// Version is the record format version this package writes.
const Version = 1

// MaxEncodedBytes is the largest encoded record, newline included (record.md section 1
// rule 7, outbox.md section 2).
const MaxEncodedBytes = 1 << 20

// The seven kinds of record.md section 4. Any other kind matching KindPattern is legal and
// kept raw.
const (
	KindHookEvent     = "hook_event"
	KindLimitSample   = "limit_sample"
	KindWebUsage      = "web_usage"
	KindTokenRows     = "token_rows"
	KindSessionList   = "session_list"
	KindHistoryImport = "history_import"
	KindSlotInfo      = "slot_info"
)

// KindPattern is what a kind name must match.
var KindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

var idPattern = regexp.MustCompile(`^[0-9A-Za-z_-]{16,64}$`)

// Seat is the login a slot had at the record's time (record.md 3.2).
type Seat struct {
	Account string `json:"account"`
	Org     string `json:"org"`
	Email   string `json:"email,omitempty"`
	Src     string `json:"src,omitempty"`
	Extra   Extra  `json:"-"`
}

type seatAlias Seat

func (s Seat) MarshalJSON() ([]byte, error) { return marshalWithExtra(seatAlias(s), s.Extra) }
func (s *Seat) UnmarshalJSON(b []byte) error {
	var a seatAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*s = Seat(a)
	return nil
}

// Term is the terminal an event came from (record.md 3.3).
type Term struct {
	App      string `json:"app"`
	ID       string `json:"id,omitempty"`
	FocusURL string `json:"focus_url,omitempty"`
	Host     string `json:"host,omitempty"`
	Extra    Extra  `json:"-"`
}

type termAlias Term

func (t Term) MarshalJSON() ([]byte, error) { return marshalWithExtra(termAlias(t), t.Extra) }
func (t *Term) UnmarshalJSON(b []byte) error {
	var a termAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*t = Term(a)
	return nil
}

// Derived holds values computed from content that was then removed (record.md 3, 4.1).
type Derived map[string]json.RawMessage

// Set stores v (anything json can encode) under key.
func (d Derived) Set(key string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("record: derived %q: %v", key, err))
	}
	d[key] = b
}

// Record is the envelope of record.md section 3. Payload is kept as raw JSON so that no
// field of it can be lost; use DecodePayload for the typed payloads of section 4, or leave
// it raw for hook_event and limit_sample, whose payloads are Claude Code's own objects.
// Unknown envelope fields are in Extra.
type Record struct {
	V        int             `json:"v"`
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	TS       string          `json:"ts"`
	Machine  string          `json:"machine"`
	Slot     string          `json:"slot,omitempty"`
	Seat     *Seat           `json:"seat,omitempty"`
	Term     *Term           `json:"term,omitempty"`
	Client   string          `json:"client,omitempty"`
	Redacted []string        `json:"redacted,omitempty"`
	Derived  Derived         `json:"derived,omitempty"`
	Payload  json.RawMessage `json:"payload"`
	Extra    Extra           `json:"-"`
}

type recordAlias Record

func (r Record) MarshalJSON() ([]byte, error) {
	if len(r.Payload) == 0 {
		r.Payload = json.RawMessage(`{}`)
	}
	return marshalWithExtra(recordAlias(r), r.Extra)
}

func (r *Record) UnmarshalJSON(b []byte) error {
	var a recordAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*r = Record(a)
	return nil
}

// Encode returns the record as one compact JSON line without the newline, or an error if
// the line (with its newline) would be over MaxEncodedBytes.
func (r *Record) Encode() ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > MaxEncodedBytes {
		return nil, fmt.Errorf("record %s: %d bytes encoded, limit is %d", r.ID, len(b)+1, MaxEncodedBytes)
	}
	return b, nil
}

// DecodePayload decodes the payload into v, normally one of the typed payloads in kinds.go.
func (r *Record) DecodePayload(v any) error { return json.Unmarshal(r.Payload, v) }

// SetPayload encodes v as the payload.
func (r *Record) SetPayload(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r.Payload = b
	return nil
}

// Time parses ts.
func (r *Record) Time() (time.Time, error) { return time.Parse(time.RFC3339Nano, r.TS) }

// FormatTime formats t the way every record time is written: UTC, milliseconds, "Z".
func FormatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Meta is what every writer knows about a record besides its payload.
type Meta struct {
	Machine string
	Slot    string
	Seat    *Seat
	Term    *Term
	Client  string
	Now     time.Time // zero means time.Now()
}

// New makes an envelope of the given kind with a fresh id. The caller sets the payload.
func New(kind string, m Meta) *Record {
	now := m.Now
	if now.IsZero() {
		now = time.Now()
	}
	return &Record{
		V: Version, ID: NewID(now), Kind: kind, TS: FormatTime(now),
		Machine: m.Machine, Slot: m.Slot, Seat: m.Seat, Term: m.Term, Client: m.Client,
	}
}

// Validate checks what the hub checks first (record.md section 5, bad_id and bad_envelope).
func (r *Record) Validate() error {
	if !idPattern.MatchString(r.ID) {
		return fmt.Errorf("bad id %q", r.ID)
	}
	if !KindPattern.MatchString(r.Kind) {
		return fmt.Errorf("bad kind %q", r.Kind)
	}
	if _, err := r.Time(); err != nil {
		return fmt.Errorf("bad ts %q", r.TS)
	}
	if len(r.Payload) == 0 || r.Payload[0] != '{' {
		return fmt.Errorf("payload must be a JSON object")
	}
	return nil
}
