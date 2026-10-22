package record

import (
	"encoding/json"
	"fmt"
)

// MaxTokenRows is the most rows one token_rows record may carry (record.md 4.4).
const MaxTokenRows = 2000

// WebUsagePayload is the payload of a web_usage record (4.3). Usage is the raw usage
// response, kept whole and not key-filtered.
type WebUsagePayload struct {
	TabID   string          `json:"tab_id"`
	Active  *bool           `json:"active,omitempty"`
	Org     json.RawMessage `json:"org,omitempty"`
	Account json.RawMessage `json:"account,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
	FetchMS *int64          `json:"fetch_ms,omitempty"`
	Extra   Extra           `json:"-"`
}

type webUsageAlias WebUsagePayload

func (p WebUsagePayload) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(webUsageAlias(p), p.Extra)
}
func (p *WebUsagePayload) UnmarshalJSON(b []byte) error {
	var a webUsageAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = WebUsagePayload(a)
	return nil
}

// TokenRow is one API response's counters (4.4). Counts are integers.
type TokenRow struct {
	Key       string `json:"key"`
	TS        string `json:"ts"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd,omitempty"`
	Model     string `json:"model,omitempty"`
	Input     int64  `json:"input"`
	Output    int64  `json:"output"`
	Cache5m   int64  `json:"cache_5m"`
	Cache1h   int64  `json:"cache_1h"`
	CacheRead int64  `json:"cache_read"`
	Thinking  *int64 `json:"thinking,omitempty"`
	Sidechain *bool  `json:"sidechain,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	Extra     Extra  `json:"-"`
}

type tokenRowAlias TokenRow

func (p TokenRow) MarshalJSON() ([]byte, error) { return marshalWithExtra(tokenRowAlias(p), p.Extra) }
func (p *TokenRow) UnmarshalJSON(b []byte) error {
	var a tokenRowAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = TokenRow(a)
	return nil
}

// TokenRowsPayload is the payload of a token_rows record (4.4).
type TokenRowsPayload struct {
	Rows      []TokenRow        `json:"rows"`
	LimitHits []json.RawMessage `json:"limit_hits,omitempty"`
	Sessions  []json.RawMessage `json:"sessions,omitempty"`
	Source    json.RawMessage   `json:"source,omitempty"`
	Extra     Extra             `json:"-"`
}

type tokenRowsAlias TokenRowsPayload

func (p TokenRowsPayload) MarshalJSON() ([]byte, error) {
	if p.Rows == nil {
		p.Rows = []TokenRow{}
	}
	return marshalWithExtra(tokenRowsAlias(p), p.Extra)
}
func (p *TokenRowsPayload) UnmarshalJSON(b []byte) error {
	var a tokenRowsAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = TokenRowsPayload(a)
	return nil
}

// SessionListPayload is the payload of a session_list record (4.5). Each session item is
// kept whole as the source wrote it.
type SessionListPayload struct {
	Complete bool              `json:"complete"`
	Source   string            `json:"source"`
	Sessions []json.RawMessage `json:"sessions"`
	Error    string            `json:"error,omitempty"`
	Extra    Extra             `json:"-"`
}

type sessionListAlias SessionListPayload

func (p SessionListPayload) MarshalJSON() ([]byte, error) {
	if p.Sessions == nil {
		p.Sessions = []json.RawMessage{}
	}
	return marshalWithExtra(sessionListAlias(p), p.Extra)
}
func (p *SessionListPayload) UnmarshalJSON(b []byte) error {
	var a sessionListAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = SessionListPayload(a)
	return nil
}

// HistoryImportPayload is the payload of a history_import record (4.6).
type HistoryImportPayload struct {
	Source string          `json:"source"`
	Raw    json.RawMessage `json:"raw"`
	Part   int             `json:"part,omitempty"`
	Parts  int             `json:"parts,omitempty"`
	Extra  Extra           `json:"-"`
}

type historyImportAlias HistoryImportPayload

func (p HistoryImportPayload) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(historyImportAlias(p), p.Extra)
}
func (p *HistoryImportPayload) UnmarshalJSON(b []byte) error {
	var a historyImportAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = HistoryImportPayload(a)
	return nil
}

// SlotInfoPayload is the payload of a slot_info record (4.7).
type SlotInfoPayload struct {
	Folder        string          `json:"folder"`
	RealPath      string          `json:"real_path,omitempty"`
	Default       bool            `json:"default"`
	Launcher      string          `json:"launcher,omitempty"`
	LoggedIn      bool            `json:"logged_in"`
	OAuthAccount  json.RawMessage `json:"oauth_account,omitempty"`
	AuthStatus    json.RawMessage `json:"auth_status,omitempty"`
	ClaudeVersion string          `json:"claude_version,omitempty"`
	Sharing       json.RawMessage `json:"sharing,omitempty"`
	Extra         Extra           `json:"-"`
}

type slotInfoAlias SlotInfoPayload

func (p SlotInfoPayload) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(slotInfoAlias(p), p.Extra)
}
func (p *SlotInfoPayload) UnmarshalJSON(b []byte) error {
	var a slotInfoAlias
	if err := unmarshalWithExtra(b, &a, &a.Extra); err != nil {
		return err
	}
	*p = SlotInfoPayload(a)
	return nil
}

// NewWebUsage builds a web_usage record. The key filter is not applied to payload.usage
// (record.md 4.3); the desktop app builds the rest of the payload itself.
func NewWebUsage(m Meta, p WebUsagePayload) (*Record, error) {
	if p.TabID == "" {
		return nil, fmt.Errorf("web_usage: tab_id is required")
	}
	if len(p.Usage) == 0 && len(p.Error) == 0 {
		return nil, fmt.Errorf("web_usage: usage or error is required")
	}
	r := New(KindWebUsage, m)
	r.Slot = "" // web_usage has no slot
	return r, r.SetPayload(p)
}

// NewTokenRows builds a token_rows record; more than MaxTokenRows rows is an error, the
// caller splits.
func NewTokenRows(m Meta, p TokenRowsPayload) (*Record, error) {
	if len(p.Rows) > MaxTokenRows {
		return nil, fmt.Errorf("token_rows: %d rows, limit is %d", len(p.Rows), MaxTokenRows)
	}
	r := New(KindTokenRows, m)
	r.Seat = nil // rows are attributed through their session
	return r, r.SetPayload(p)
}

// NewSessionList builds a session_list record. As record.md 4.5 says, it drops
// messagingSocketPath from every item and applies the key filter to each item.
func NewSessionList(m Meta, p SessionListPayload) (*Record, error) {
	r := New(KindSessionList, m)
	var redacted []string
	items := make([]json.RawMessage, 0, len(p.Sessions))
	for i, raw := range p.Sessions {
		obj, err := parseObject(raw)
		if err != nil {
			items = append(items, raw)
			continue
		}
		if _, ok := obj["messagingSocketPath"]; ok {
			delete(obj, "messagingSocketPath")
			redacted = append(redacted, "sessions[].messagingSocketPath")
		}
		paths, err := filterObject(obj)
		if err != nil {
			return nil, err
		}
		for _, q := range paths {
			redacted = append(redacted, "sessions[]."+q)
		}
		b, err := marshalObject(obj)
		if err != nil {
			return nil, fmt.Errorf("session %d: %w", i, err)
		}
		items = append(items, b)
	}
	p.Sessions = items
	r.Redacted = dedupe(redacted)
	return r, r.SetPayload(p)
}

// NewHistoryImport builds a history_import record.
func NewHistoryImport(m Meta, p HistoryImportPayload) (*Record, error) {
	r := New(KindHistoryImport, m)
	return r, r.SetPayload(p)
}

// NewSlotInfo builds a slot_info record; auth_status goes through the key filter.
func NewSlotInfo(m Meta, p SlotInfoPayload) (*Record, error) {
	r := New(KindSlotInfo, m)
	if len(p.AuthStatus) > 0 {
		cleaned, paths, err := FilterKeys(p.AuthStatus)
		if err != nil {
			return nil, err
		}
		p.AuthStatus = cleaned
		for _, q := range paths {
			r.Redacted = append(r.Redacted, "auth_status."+q)
		}
	}
	if p.Folder == "" {
		p.Folder = m.Slot
	}
	return r, r.SetPayload(p)
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
