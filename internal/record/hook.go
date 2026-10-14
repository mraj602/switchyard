package record

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits from record.md 4.1.
const (
	lastMessageRunes   = 400
	questionRunes      = 300
	notificationRunes  = 300
	lastMessageEllipse = "…"
)

// HookOptions are the privacy switches of plan section 13.
type HookOptions struct {
	// LastMessages allows derived.last_message and derived.question (the tail of the last
	// assistant message and AskUserQuestion's first question).
	LastMessages bool
}

// contentFields is the closed list of record.md 4.1. Each is removed on EVERY event (a
// field that turns up on an event nobody listed is still content); keep names the events
// on which the field is not content and stays.
var contentFields = []struct {
	field string
	keep  []string
}{
	{"prompt", nil},
	{"tool_input", nil},
	{"tool_response", nil},
	{"permission_suggestions", nil},
	{"error", []string{"StopFailure"}}, // a short error code there, tool output elsewhere
	{"last_assistant_message", nil},
	{"custom_instructions", nil},
}

// countOnly fields hold lists whose items quote the user's commands and prompts; only the
// number of items is kept (derived.<field>).
var countOnly = []struct {
	field  string
	events []string
}{
	{"session_crons", []string{"Stop", "SubagentStop"}},
	{"background_tasks", []string{"Stop", "SubagentStop"}},
}

// NewHookEvent builds a hook_event record from the JSON Claude Code wrote to a hook's stdin:
// every field is kept except the content fields of record.md 4.1, which are removed, listed
// in redacted, and summarised in derived; then the section 1 rule 4 key filter runs over
// what is left. A payload that is not a JSON object is an error (the caller still exits 0).
func NewHookEvent(m Meta, stdin []byte, opt HookOptions) (*Record, error) {
	obj, err := parseObject(stdin)
	if err != nil {
		return nil, fmt.Errorf("hook payload: %w", err)
	}
	r := New(KindHookEvent, m)
	derived := Derived{}
	var redacted []string

	var event string
	_ = json.Unmarshal(obj["hook_event_name"], &event)

	for _, cf := range contentFields {
		raw, ok := obj[cf.field]
		if !ok || contains(cf.keep, event) {
			continue
		}
		switch cf.field {
		case "prompt":
			derived.Set("prompt_chars", runeCount(raw))
		case "tool_input":
			if opt.LastMessages {
				if q := firstQuestion(obj, raw); q != "" {
					derived.Set("question", truncRunes(q, questionRunes))
				}
			}
		case "permission_suggestions":
			derived.Set("permission_suggestions", arrayLen(raw))
		case "error":
			derived.Set("error_chars", runeCount(raw))
		case "last_assistant_message":
			s := stringValue(raw)
			derived.Set("last_message_chars", utf8.RuneCountInString(s))
			if opt.LastMessages {
				derived.Set("last_message", lastMessage(s))
			}
		case "custom_instructions":
			derived.Set("custom_instructions_chars", runeCount(raw))
		}
		delete(obj, cf.field)
		redacted = append(redacted, cf.field)
	}

	for _, cf := range countOnly {
		if raw, ok := obj[cf.field]; ok && contains(cf.events, event) {
			derived.Set(cf.field, arrayLen(raw))
			delete(obj, cf.field)
			redacted = append(redacted, cf.field)
		}
	}

	// Notification.message is Claude Code's own UI text: kept, cut to 300 characters.
	if event == "Notification" {
		if raw, ok := obj["message"]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && utf8.RuneCountInString(s) > notificationRunes {
				obj["message"] = mustJSON(truncRunes(s, notificationRunes))
				redacted = append(redacted, "message")
			}
		}
	}

	paths, err := filterObject(obj)
	if err != nil {
		return nil, fmt.Errorf("hook payload: %w", err)
	}
	redacted = append(redacted, paths...)

	if len(derived) > 0 {
		r.Derived = derived
	}
	if len(redacted) > 0 {
		r.Redacted = redacted
	}
	r.Payload, err = marshalObject(obj)
	return r, err
}

// lastMessage implements the derived.last_message rule: drop trailing whitespace, keep the
// last 400 characters, and if that cut a line, drop the partial first line and prefix "…".
func lastMessage(s string) string {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	if utf8.RuneCountInString(s) <= lastMessageRunes {
		return s
	}
	runes := []rune(s)
	cut := len(runes) - lastMessageRunes
	tail := string(runes[cut:])
	if runes[cut-1] == '\n' {
		return tail // the cut fell exactly on a line boundary
	}
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return lastMessageEllipse + tail
}

// firstQuestion returns tool_input.questions[0].question for an AskUserQuestion call.
func firstQuestion(obj map[string]json.RawMessage, toolInput json.RawMessage) string {
	var name string
	_ = json.Unmarshal(obj["tool_name"], &name)
	if name != "AskUserQuestion" {
		return ""
	}
	var in struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if json.Unmarshal(toolInput, &in) != nil || len(in.Questions) == 0 {
		return ""
	}
	return in.Questions[0].Question
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// runeCount is the length in code points of a JSON string value (0 for null or non-strings).
func runeCount(raw json.RawMessage) int { return utf8.RuneCountInString(stringValue(raw)) }

func stringValue(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func arrayLen(raw json.RawMessage) int {
	var a []json.RawMessage
	_ = json.Unmarshal(raw, &a)
	return len(a)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// parseObject decodes a JSON object into its raw top-level members.
func parseObject(b []byte) (map[string]json.RawMessage, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	return obj, nil
}

func marshalObject(obj map[string]json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(obj)
}

// NewLimitSample builds a limit_sample record from the status-line JSON on stdin: the payload
// is kept whole (key filter applied, token counters allow-listed) and derived is computed.
func NewLimitSample(m Meta, stdin []byte) (*Record, error) {
	obj, err := parseObject(stdin)
	if err != nil {
		return nil, fmt.Errorf("statusline payload: %w", err)
	}
	r := New(KindLimitSample, m)
	r.Derived = limitDerived(obj)
	paths, err := filterObject(obj)
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		r.Redacted = paths
	}
	r.Payload, err = marshalObject(obj)
	return r, err
}

func limitDerived(obj map[string]json.RawMessage) Derived {
	var p struct {
		RateLimits struct {
			FiveHour limitWindow `json:"five_hour"`
			SevenDay limitWindow `json:"seven_day"`
		} `json:"rate_limits"`
		ContextWindow struct {
			UsedPercentage *json.Number `json:"used_percentage"`
		} `json:"context_window"`
	}
	b, _ := json.Marshal(obj)
	_ = json.Unmarshal(b, &p) // wrong types just leave nulls
	d := Derived{}
	d["five_hour_pct"] = numOrNull(p.RateLimits.FiveHour.Used)
	d["five_hour_resets_at"] = resetOrNull(p.RateLimits.FiveHour.ResetsAt)
	d["seven_day_pct"] = numOrNull(p.RateLimits.SevenDay.Used)
	d["seven_day_resets_at"] = resetOrNull(p.RateLimits.SevenDay.ResetsAt)
	d["context_pct"] = numOrNull(p.ContextWindow.UsedPercentage)
	return d
}

type limitWindow struct {
	Used     *json.Number    `json:"used_percentage"`
	ResetsAt json.RawMessage `json:"resets_at"`
}

func numOrNull(n *json.Number) json.RawMessage {
	if n == nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(n.String())
}

// resetOrNull turns resets_at (epoch seconds or milliseconds, or an RFC 3339 string, record.md
// 4.2 "uncertain") into an RFC 3339 UTC string.
func resetOrNull(raw json.RawMessage) json.RawMessage {
	null := json.RawMessage("null")
	if len(raw) == 0 {
		return null
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		sec := int64(n)
		if sec > 1e11 { // milliseconds
			return mustJSON(FormatTime(time.UnixMilli(sec)))
		}
		return mustJSON(FormatTime(time.Unix(sec, int64((n-float64(sec))*1e9))))
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return mustJSON(FormatTime(t))
		}
	}
	return null
}
