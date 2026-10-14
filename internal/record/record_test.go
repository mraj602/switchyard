package record

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var testMeta = Meta{
	Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
	Slot:    "~/.claude-2",
	Seat:    &Seat{Account: "aaaaaaaa-0000-4000-8000-000000000001", Org: "bbbbbbbb-0000-4000-8000-000000000001", Src: "claude_json"},
	Client:  "sy/0.1.0",
	Now:     time.Date(2026, 10, 11, 14, 3, 22, 418e6, time.UTC),
}

func decodeAny(t testing.TB, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode: %v\n%s", err, b)
	}
	return v
}

// roundTrip encodes r, decodes it again, encodes that, and requires both encodings to be
// semantically equal. It returns the decoded record.
func roundTrip(t testing.TB, r *Record) *Record {
	t.Helper()
	line, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsRune(line, '\n') {
		t.Fatal("encoded record contains a newline")
	}
	var back Record
	if err := json.Unmarshal(line, &back); err != nil {
		t.Fatal(err)
	}
	again, err := back.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeAny(t, line), decodeAny(t, again)) {
		t.Fatalf("decode/encode changed the record:\n%s\n%s", line, again)
	}
	if err := back.Validate(); err != nil {
		t.Fatal(err)
	}
	return &back
}

func TestULID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	t0 := time.Date(2026, 10, 11, 14, 3, 22, 418e6, time.UTC)
	seen := map[string]bool{}
	prev := ""
	for i := 0; i < 1000; i++ {
		id := NewID(t0.Add(time.Duration(i) * time.Millisecond))
		if !re.MatchString(id) {
			t.Fatalf("bad ULID %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate %q", id)
		}
		seen[id] = true
		if id <= prev {
			t.Fatalf("not sorted by time: %q after %q", id, prev)
		}
		prev = id
	}
	got, err := ULIDTime(NewID(t0))
	if err != nil || !got.Equal(t0) {
		t.Fatalf("ULIDTime = %v, %v", got, err)
	}
}

func TestEnvelopeKeepsUnknownFields(t *testing.T) {
	in := `{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T9B4CD","kind":"quantum_thing","ts":"2026-10-11T14:03:22.418Z","machine":"m_x",` +
		`"seat":{"account":"a","org":"o","future_seat_field":{"x":[1,2]}},"term":{"app":"warp","new_term":1.50},` +
		`"future_envelope":{"deep":[true,null,"é"]},"derived":{"z":1},"payload":{"anything":1.0,"big":12345678901234567890}}`
	var r Record
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(&r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeAny(t, []byte(in)), decodeAny(t, out)) {
		t.Fatalf("lost a field:\nin  %s\nout %s", in, out)
	}
	if !strings.Contains(string(out), "12345678901234567890") || !strings.Contains(string(out), "1.0") {
		t.Fatalf("number text changed: %s", out)
	}
}

func TestRecordKinds(t *testing.T) {
	t.Run("session_list", func(t *testing.T) {
		item := json.RawMessage(`{"pid":1,"sessionId":"s","messagingSocketPath":"/tmp/x.sock","future":{"a":1}}`)
		r, err := NewSessionList(testMeta, SessionListPayload{Complete: true, Source: "sessions_dir", Sessions: []json.RawMessage{item}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(r.Payload), "messagingSocketPath") || !strings.Contains(string(r.Payload), `"future":{"a":1}`) {
			t.Fatalf("payload: %s", r.Payload)
		}
		roundTrip(t, r)
	})
	t.Run("token_rows", func(t *testing.T) {
		raw := `{"rows":[{"key":"k","ts":"t","session_id":"s","input":1,"output":2,"cache_5m":0,"cache_1h":0,"cache_read":3,"new_counter":9}],"future":1}`
		var p TokenRowsPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		r, err := NewTokenRows(testMeta, p)
		if err != nil {
			t.Fatal(err)
		}
		if r.Seat != nil {
			t.Fatal("token_rows must not carry a seat")
		}
		if !reflect.DeepEqual(decodeAny(t, r.Payload), decodeAny(t, []byte(raw))) {
			t.Fatalf("payload changed: %s", r.Payload)
		}
		p.Rows = make([]TokenRow, MaxTokenRows+1)
		if _, err := NewTokenRows(testMeta, p); err == nil {
			t.Fatal("2001 rows accepted")
		}
	})
	t.Run("slot_info filters auth_status", func(t *testing.T) {
		auth := json.RawMessage(`{"loggedIn":true,"email":"a@b","accessToken":"sk-x","nested":{"api_key":"k","ok":1}}`)
		r, err := NewSlotInfo(testMeta, SlotInfoPayload{Folder: "~/.claude-2", LoggedIn: true, AuthStatus: auth})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(r.Payload), "sk-x") || strings.Contains(string(r.Payload), "api_key") {
			t.Fatalf("secret kept: %s", r.Payload)
		}
		want := []string{"auth_status.accessToken", "auth_status.nested.api_key"}
		if !reflect.DeepEqual(r.Redacted, want) {
			t.Fatalf("redacted = %v", r.Redacted)
		}
		roundTrip(t, r)
	})
	t.Run("history_import", func(t *testing.T) {
		r, err := NewHistoryImport(testMeta, HistoryImportPayload{Source: "stats_cache", Raw: json.RawMessage(`{"version":3,"hourCounts":{"1":2},"tokens":{"x":1}}`)})
		if err != nil {
			t.Fatal(err)
		}
		roundTrip(t, r)
	})
	t.Run("web_usage needs tab and usage", func(t *testing.T) {
		if _, err := NewWebUsage(testMeta, WebUsagePayload{}); err == nil {
			t.Fatal("empty web_usage accepted")
		}
	})
}

func TestEncodeTooLarge(t *testing.T) {
	r := New("big", testMeta)
	r.Payload = json.RawMessage(`{"x":"` + strings.Repeat("a", MaxEncodedBytes) + `"}`)
	if _, err := r.Encode(); err == nil {
		t.Fatal("over-limit record encoded")
	}
}

func fixtureFiles(t testing.TB, glob string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", glob))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures for %s (%v)", glob, err)
	}
	sort.Strings(files)
	return files
}

func readFixture(t testing.TB, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// checkPayload encodes and decodes r, then requires that the input object minus the fields
// the record says it removed equals the record's payload exactly: no field lost or changed.
func checkPayload(t *testing.T, name string, input []byte, r *Record) {
	t.Helper()
	back := roundTrip(t, r)
	want := decodeAny(t, input).(map[string]any)
	for _, p := range back.Redacted {
		delete(want, p) // fixtures only have top-level content fields
	}
	if !reflect.DeepEqual(want, decodeAny(t, back.Payload)) {
		t.Fatalf("%s: payload lost or changed a field\nwant %v\ngot  %s", name, want, back.Payload)
	}
	// On real data every redaction must be a documented content field, never the key filter.
	allowed := map[string]bool{"prompt": true, "tool_input": true, "tool_response": true, "permission_suggestions": true,
		"error": true, "last_assistant_message": true, "custom_instructions": true,
		"session_crons": true, "background_tasks": true}
	for _, p := range back.Redacted {
		if !allowed[p] {
			t.Errorf("%s: undocumented redaction %q", name, p)
		}
	}
}

func TestRoundTripHookFixtures(t *testing.T) {
	for _, f := range fixtureFiles(t, "hooks/by-event/*.json") {
		in, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, lm := range []bool{false, true} {
			r, err := NewHookEvent(testMeta, in, HookOptions{LastMessages: lm})
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			checkPayload(t, filepath.Base(f), in, r)
		}
	}
	// A new key Claude Code adds later survives.
	in := []byte(`{"hook_event_name":"Stop","session_id":"s","last_assistant_message":"hi","brand_new_field":{"a":[1,2]}}`)
	r, err := NewHookEvent(testMeta, in, HookOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkPayload(t, "unknown key", in, r)
}

func TestRoundTripHookRuns(t *testing.T) {
	for _, f := range fixtureFiles(t, "hooks/runs/*.jsonl") {
		data, _ := os.ReadFile(f)
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var env struct {
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(line, &env); err != nil {
				t.Fatal(err)
			}
			r, err := NewHookEvent(testMeta, env.Payload, HookOptions{LastMessages: true})
			if err != nil {
				t.Fatal(err)
			}
			checkPayload(t, filepath.Base(f), env.Payload, r)
		}
	}
}

func TestRoundTripStatusline(t *testing.T) {
	for _, f := range fixtureFiles(t, "statusline/*.json") {
		in, _ := os.ReadFile(f)
		r, err := NewLimitSample(testMeta, in)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(r.Redacted) != 0 {
			t.Errorf("%s: key filter removed %v from a status line (token counters must be allow-listed)", f, r.Redacted)
		}
		checkPayload(t, filepath.Base(f), in, r)
		if _, ok := r.Derived["five_hour_pct"]; !ok {
			t.Errorf("%s: no derived.five_hour_pct", f)
		}
	}
}

func TestLimitSampleDerived(t *testing.T) {
	r, err := NewLimitSample(testMeta, readFixture(t, "statusline/full.json"))
	if err != nil {
		t.Fatal(err)
	}
	derivedIs(t, r, "five_hour_pct", "23.5")
	derivedIs(t, r, "seven_day_pct", "41.2")
	derivedIs(t, r, "context_pct", "9")
	derivedIs(t, r, "five_hour_resets_at", fmt.Sprintf("%q", FormatTime(time.Unix(1791824400, 0))))
	derivedIs(t, r, "seven_day_resets_at", fmt.Sprintf("%q", FormatTime(time.Unix(1792256400, 0))))
	// A string resets_at is normalised, a missing window gives null.
	r2, _ := NewLimitSample(testMeta, []byte(`{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":"2026-10-11T17:00:00+00:00"}}}`))
	derivedIs(t, r2, "five_hour_resets_at", `"2026-10-11T17:00:00.000Z"`)
	derivedIs(t, r2, "seven_day_pct", "null")
	derivedIs(t, r2, "context_pct", "null")
}

func TestRoundTripUsage(t *testing.T) {
	for _, f := range fixtureFiles(t, "usage/response-*.json") {
		in, _ := os.ReadFile(f)
		active := true
		r, err := NewWebUsage(Meta{Machine: testMeta.Machine, Seat: testMeta.Seat, Client: "switchyard-desktop/0.6.0", Now: testMeta.Now}, WebUsagePayload{
			TabID: "t_1", Active: &active, Usage: json.RawMessage(in),
			Org: json.RawMessage(`{"uuid":"bbbbbbbb-0000-4000-8000-000000000001","name":"Org 1"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		back := roundTrip(t, r)
		var p WebUsagePayload
		if err := back.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decodeAny(t, in), decodeAny(t, p.Usage)) {
			t.Fatalf("%s: usage changed", f)
		}
		if back.Slot != "" {
			t.Fatal("web_usage has a slot")
		}
	}
	// iguana_necktie survives, and so does an injected unknown key, even one holding names
	// that the key filter would delete anywhere else.
	obj, _ := parseObject(readFixture(t, "usage/response-02.json"))
	if _, ok := obj["iguana_necktie"]; !ok {
		t.Fatal("fixture has no iguana_necktie")
	}
	obj["brand_new_counter"] = json.RawMessage(`{"utilization":3,"token_budget":7,"cookie_jar":1}`)
	injected, _ := marshalObject(obj)
	r, _ := NewWebUsage(testMeta, WebUsagePayload{TabID: "t", Usage: injected})
	back := roundTrip(t, r)
	var p WebUsagePayload
	_ = back.DecodePayload(&p)
	for _, want := range []string{`"iguana_necktie"`, `"brand_new_counter"`, `"token_budget":7`, `"cookie_jar":1`, `"tangelo"`} {
		if !strings.Contains(string(p.Usage), want) {
			t.Errorf("usage lost %s", want)
		}
	}
	if len(back.Redacted) != 0 {
		t.Errorf("web_usage redacted %v", back.Redacted)
	}
}

func TestRoundTripAuth(t *testing.T) {
	for _, f := range fixtureFiles(t, "auth/status-*.json") {
		in, _ := os.ReadFile(f)
		r, err := NewSlotInfo(testMeta, SlotInfoPayload{LoggedIn: true, AuthStatus: in})
		if err != nil {
			t.Fatal(err)
		}
		back := roundTrip(t, r)
		var p SlotInfoPayload
		_ = back.DecodePayload(&p)
		if !reflect.DeepEqual(decodeAny(t, in), decodeAny(t, p.AuthStatus)) || len(back.Redacted) != 0 {
			t.Fatalf("%s: auth status changed (redacted %v)", f, back.Redacted)
		}
	}
	for _, f := range fixtureFiles(t, "auth/oauth-*.json") {
		in, _ := os.ReadFile(f)
		r, err := NewSlotInfo(testMeta, SlotInfoPayload{LoggedIn: true, OAuthAccount: in})
		if err != nil {
			t.Fatal(err)
		}
		back := roundTrip(t, r)
		var p SlotInfoPayload
		_ = back.DecodePayload(&p)
		if !reflect.DeepEqual(decodeAny(t, in), decodeAny(t, p.OAuthAccount)) {
			t.Fatalf("%s: oauth account changed", f)
		}
	}
}
