package scan

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// TokenRow is one API response found in a transcript (docs/spec/record.md 4.4). TS is epoch
// milliseconds, as in the prototype's tokens table; the record layer formats it as RFC 3339.
// Empty SessionID, CWD and Model mean "absent" (the prototype stores null for both).
type TokenRow struct {
	Key       string `json:"key"`
	TS        int64  `json:"ts"`
	SessionID string `json:"session_id,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Model     string `json:"model,omitempty"`
	Input     int64  `json:"input"`
	Output    int64  `json:"output"`
	Cache5m   int64  `json:"cache_5m"`
	Cache1h   int64  `json:"cache_1h"`
	CacheRead int64  `json:"cache_read"`
	Thinking  int64  `json:"thinking"`
	Sidechain bool   `json:"sidechain"`
}

// RFC3339 formats TS the way the record spec wants it.
func (r TokenRow) RFC3339() string {
	return time.UnixMilli(r.TS).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// merge folds a later line of the same API response into r: counters keep their largest
// value, everything else keeps the first line's value (the prototype's tokenUpsert).
func (r *TokenRow) merge(o TokenRow) {
	for _, p := range [][2]*int64{{&r.Input, &o.Input}, {&r.Output, &o.Output}, {&r.Cache5m, &o.Cache5m},
		{&r.Cache1h, &o.Cache1h}, {&r.CacheRead, &o.CacheRead}, {&r.Thinking, &o.Thinking}} {
		if *p[1] > *p[0] {
			*p[0] = *p[1]
		}
	}
}

// LimitHit is a "You've hit your limit" reply (synthetic assistant line with quotaLimits).
// ResetsAt is epoch ms, 0 when unknown.
type LimitHit struct {
	SessionID string `json:"session_id"`
	TS        int64  `json:"ts"`
	Type      string `json:"type"`
	Status    string `json:"status,omitempty"`
	ResetsAt  int64  `json:"resets_at,omitempty"`
}

// SessionInfo is a per-session fact found in transcripts: the owning account and org of a
// Remote Control session (bridge-session lines) and its claude.ai URL.
type SessionInfo struct {
	SessionID    string `json:"session_id"`
	OwnerAccount string `json:"owner_account,omitempty"`
	OwnerOrg     string `json:"owner_org,omitempty"`
	RemoteURL    string `json:"remote_url,omitempty"`
	Title        string `json:"title,omitempty"`
}

// Parsed is the result of one transcript line. At most one field is set.
type Parsed struct {
	Row   *TokenRow
	Owner *SessionInfo
	Hit   *LimitHit
	// Remote is set for a remote_session_change attachment: session id (may be empty, the
	// caller then uses the file's session) and URL.
	Remote *SessionInfo
}

var remoteRE = regexp.MustCompile(`"type":"remote_session_change","url":"(https://claude\.ai/code/session_[A-Za-z0-9]+)"`)

// jnum mirrors JS `x || 0` on a JSON number: non-numbers count as 0.
func jnum(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return 0
}

// jstr mirrors JS `x || null` for strings: only a non-empty string counts.
func jstr(v any) string {
	s, _ := v.(string)
	return s
}

func jobj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// parseTime mirrors Date.parse for the ISO timestamps Claude Code writes, in epoch ms.
func parseTime(s string) (int64, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// ParseLine is a port of parseLine in core/transcripts.js, plus remote-URL detection.
func ParseLine(line string) (Parsed, bool) {
	if strings.Contains(line, `"bridge-session"`) {
		var d map[string]any
		if json.Unmarshal([]byte(line), &d) != nil {
			return Parsed{}, false
		}
		sid, acct, org := jstr(d["sessionId"]), jstr(d["ownerAccountUuid"]), jstr(d["ownerOrganizationUuid"])
		if jstr(d["type"]) != "bridge-session" || sid == "" || (acct == "" && org == "") {
			return Parsed{}, false
		}
		return Parsed{Owner: &SessionInfo{SessionID: sid, OwnerAccount: acct, OwnerOrg: org}}, true
	}
	if strings.Contains(line, `"remote_session_change"`) {
		if m := remoteRE.FindAllStringSubmatch(line, -1); m != nil {
			var d struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal([]byte(line), &d)
			return Parsed{Remote: &SessionInfo{SessionID: d.SessionID, RemoteURL: m[len(m)-1][1]}}, true
		}
	}
	if !strings.Contains(line, `"usage"`) || !strings.Contains(line, `"assistant"`) {
		return Parsed{}, false
	}
	var d map[string]any
	if json.Unmarshal([]byte(line), &d) != nil || jstr(d["type"]) != "assistant" {
		return Parsed{}, false
	}
	m := jobj(d["message"])
	u := jobj(m["usage"])
	ts, tsOK := parseTime(jstr(d["timestamp"]))
	if jstr(m["model"]) == "<synthetic>" {
		ql := jobj(d["quotaLimits"])
		sid, typ := jstr(d["sessionId"]), jstr(ql["rateLimitType"])
		if ql == nil || typ == "" || sid == "" || !tsOK {
			return Parsed{}, false
		}
		h := &LimitHit{SessionID: sid, TS: ts, Type: typ, Status: jstr(ql["status"])}
		if r := jnum(ql["resetsAt"]); r != 0 {
			h.ResetsAt = r * 1000
		}
		return Parsed{Hit: h}, true
	}
	if u == nil {
		return Parsed{}, false
	}
	key := jstr(m["id"])
	if key == "" {
		key = jstr(d["requestId"])
	}
	if key == "" {
		key = jstr(d["uuid"])
	}
	if key == "" || !tsOK {
		return Parsed{}, false
	}
	cc := jobj(u["cache_creation"])
	c5v := cc["ephemeral_5m_input_tokens"]
	c1v := cc["ephemeral_1h_input_tokens"]
	var c5, c1 int64
	if c5v == nil && c1v == nil {
		c5 = jnum(u["cache_creation_input_tokens"])
	} else {
		c5, c1 = jnum(c5v), jnum(c1v)
	}
	side, _ := d["isSidechain"].(bool)
	return Parsed{Row: &TokenRow{
		Key: key, TS: ts, SessionID: jstr(d["sessionId"]), CWD: jstr(d["cwd"]), Model: jstr(m["model"]),
		Input: jnum(u["input_tokens"]), Output: jnum(u["output_tokens"]), Cache5m: c5, Cache1h: c1,
		CacheRead: jnum(u["cache_read_input_tokens"]),
		Thinking:  jnum(jobj(u["output_tokens_details"])["thinking_tokens"]),
		Sidechain: side,
	}}, true
}
