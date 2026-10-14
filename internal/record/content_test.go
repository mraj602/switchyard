package record

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func hook(t *testing.T, payload string, opt HookOptions) (*Record, map[string]any) {
	t.Helper()
	r, err := NewHookEvent(testMeta, []byte(payload), opt)
	if err != nil {
		t.Fatal(err)
	}
	return r, decodeAny(t, r.Payload).(map[string]any)
}

func derivedIs(t *testing.T, r *Record, key, want string) {
	t.Helper()
	if got := string(r.Derived[key]); got != want {
		t.Errorf("derived.%s = %s, want %s", key, got, want)
	}
}

func listed(r *Record, field string) bool {
	for _, p := range r.Redacted {
		if p == field {
			return true
		}
	}
	return false
}

// One test per content field of record.md 4.1: it is removed from the payload, listed in
// redacted, and the derived value is right.

func TestContentPrompt(t *testing.T) {
	r, p := hook(t, `{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"héllo wörld 😀"}`, HookOptions{LastMessages: true})
	if _, ok := p["prompt"]; ok || !listed(r, "prompt") {
		t.Fatalf("prompt kept or not listed: %s %v", r.Payload, r.Redacted)
	}
	derivedIs(t, r, "prompt_chars", "13") // code points, not bytes or UTF-16 units
	if p["session_id"] != "s" {
		t.Fatal("other fields lost")
	}
}

func TestContentToolInput(t *testing.T) {
	for _, ev := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest", "PermissionDenied"} {
		r, p := hook(t, `{"hook_event_name":"`+ev+`","tool_name":"Bash","tool_use_id":"tu1","tool_input":{"command":"cat secret.txt"}}`, HookOptions{})
		if _, ok := p["tool_input"]; ok || !listed(r, "tool_input") {
			t.Fatalf("%s: tool_input kept or not listed", ev)
		}
		if p["tool_name"] != "Bash" || p["tool_use_id"] != "tu1" {
			t.Fatalf("%s: tool_name or tool_use_id lost", ev)
		}
		if _, ok := r.Derived["question"]; ok {
			t.Fatalf("%s: question derived for Bash", ev)
		}
	}
}

func TestContentAskUserQuestion(t *testing.T) {
	long := strings.Repeat("é", 350)
	in := `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_use_id":"x","tool_input":{"questions":[{"question":"` + long + `","options":[]},{"question":"second"}]}}`
	r, _ := hook(t, in, HookOptions{LastMessages: true})
	var q string
	_ = json.Unmarshal(r.Derived["question"], &q)
	if q != strings.Repeat("é", 300) {
		t.Fatalf("question = %d chars", len([]rune(q)))
	}
	r, _ = hook(t, in, HookOptions{LastMessages: false})
	if _, ok := r.Derived["question"]; ok {
		t.Fatal("question derived while last messages are not allowed")
	}
	if !listed(r, "tool_input") {
		t.Fatal("tool_input not listed")
	}
}

func TestContentToolResponse(t *testing.T) {
	r, p := hook(t, `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_response":{"stdout":"hi","usage":{"totalTokens":5}},"duration_ms":4}`, HookOptions{})
	if _, ok := p["tool_response"]; ok || !listed(r, "tool_response") {
		t.Fatal("tool_response kept or not listed")
	}
	if p["duration_ms"] != json.Number("4") {
		t.Fatal("duration_ms lost")
	}
}

func TestContentPermissionSuggestions(t *testing.T) {
	r, p := hook(t, `{"hook_event_name":"PermissionRequest","tool_name":"Bash","permission_suggestions":[{"rules":[{"ruleContent":"cat x"}]},{"a":1}]}`, HookOptions{})
	if _, ok := p["permission_suggestions"]; ok || !listed(r, "permission_suggestions") {
		t.Fatal("permission_suggestions kept or not listed")
	}
	derivedIs(t, r, "permission_suggestions", "2")
}

func TestContentErrorToolFailure(t *testing.T) {
	r, p := hook(t, `{"hook_event_name":"PostToolUseFailure","tool_name":"Bash","error":"Exit code 1\ncat: x","is_interrupt":false,"duration_ms":3}`, HookOptions{})
	if _, ok := p["error"]; ok || !listed(r, "error") {
		t.Fatal("error kept or not listed")
	}
	derivedIs(t, r, "error_chars", "18")
	if p["is_interrupt"] != false || p["duration_ms"] != json.Number("3") {
		t.Fatal("is_interrupt or duration_ms lost")
	}
}

func TestStopFailureErrorKept(t *testing.T) {
	r, p := hook(t, `{"hook_event_name":"StopFailure","error":"model_not_found","last_assistant_message":"oops"}`, HookOptions{})
	if p["error"] != "model_not_found" || listed(r, "error") {
		t.Fatalf("StopFailure error removed: %s %v", r.Payload, r.Redacted)
	}
	if _, ok := p["last_assistant_message"]; ok || !listed(r, "last_assistant_message") {
		t.Fatal("last_assistant_message kept on StopFailure")
	}
}

func TestContentLastAssistantMessage(t *testing.T) {
	for _, ev := range []string{"Stop", "SubagentStop", "StopFailure"} {
		r, p := hook(t, `{"hook_event_name":"`+ev+`","last_assistant_message":"Should I also update the migration?\n\n"}`, HookOptions{LastMessages: true})
		if _, ok := p["last_assistant_message"]; ok || !listed(r, "last_assistant_message") {
			t.Fatalf("%s: message kept or not listed", ev)
		}
		derivedIs(t, r, "last_message", `"Should I also update the migration?"`)
		derivedIs(t, r, "last_message_chars", "37") // full length, trailing whitespace included
	}
	r, _ := hook(t, `{"hook_event_name":"Stop","last_assistant_message":"secret answer"}`, HookOptions{})
	if _, ok := r.Derived["last_message"]; ok {
		t.Fatal("last_message derived while not allowed")
	}
	derivedIs(t, r, "last_message_chars", "13")
}

func TestLastMessageTail(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"short", "hello  \n", "hello"},
		{"exactly 400", strings.Repeat("a", 400), strings.Repeat("a", 400)},
		{"one long line", strings.Repeat("a", 500), "…" + strings.Repeat("a", 400)},
		{"cut inside a line drops the partial line",
			strings.Repeat("a", 380) + "\nTail line one\n1. x\n2. y", "…Tail line one\n1. x\n2. y"},
		{"cut right after a newline keeps the tail as is",
			strings.Repeat("a", 9) + "\n" + strings.Repeat("b", 400), strings.Repeat("b", 400)},
	}
	for _, c := range cases {
		if got := lastMessage(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestContentCustomInstructions(t *testing.T) {
	r, p := hook(t, `{"hook_event_name":"PreCompact","trigger":"manual","custom_instructions":"focus on the API"}`, HookOptions{})
	if _, ok := p["custom_instructions"]; ok || !listed(r, "custom_instructions") {
		t.Fatal("custom_instructions kept or not listed")
	}
	derivedIs(t, r, "custom_instructions_chars", "16")
	r, p = hook(t, `{"hook_event_name":"PreCompact","trigger":"manual","custom_instructions":null}`, HookOptions{})
	if _, ok := p["custom_instructions"]; ok {
		t.Fatal("null custom_instructions kept")
	}
	derivedIs(t, r, "custom_instructions_chars", "0")
}

func TestNotificationMessageCut(t *testing.T) {
	r, p := hook(t, `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`, HookOptions{})
	if p["message"] != "Claude needs your permission to use Bash" || len(r.Redacted) != 0 {
		t.Fatalf("short message changed: %s %v", r.Payload, r.Redacted)
	}
	r, p = hook(t, `{"hook_event_name":"Notification","message":"`+strings.Repeat("ü", 500)+`"}`, HookOptions{})
	if p["message"] != strings.Repeat("ü", 300) || !listed(r, "message") {
		t.Fatalf("long message not cut to 300 characters: %v", r.Redacted)
	}
}

func TestContentFieldsAreRemovedOnEveryEvent(t *testing.T) {
	// A content field on an event nobody listed is still content.
	for _, ev := range []string{"SessionStart", "SubagentStart", "SomeFutureEvent"} {
		r, p := hook(t, `{"hook_event_name":"`+ev+`","prompt":"secret","tool_input":{"command":"x"},"tool_response":"y","error":"boom","last_assistant_message":"hello"}`, HookOptions{})
		for _, f := range []string{"prompt", "tool_input", "tool_response", "error", "last_assistant_message"} {
			if _, ok := p[f]; ok {
				t.Errorf("%s: %s kept: %s", ev, f, r.Payload)
			}
			if !listed(r, f) {
				t.Errorf("%s: %s not in redacted", ev, f)
			}
		}
		derivedIs(t, r, "prompt_chars", "6")
	}
	// StopFailure.error is an error code, not content.
	_, p := hook(t, `{"hook_event_name":"StopFailure","error":"rate_limit"}`, HookOptions{})
	if p["error"] != "rate_limit" {
		t.Errorf("StopFailure error removed: %v", p)
	}
}

func TestStopListsKeepCountsOnly(t *testing.T) {
	for _, ev := range []string{"Stop", "SubagentStop"} {
		r, p := hook(t, `{"hook_event_name":"`+ev+`","session_crons":[{"prompt":"run my secret thing"}],"background_tasks":[{"command":"make secret"},{"command":"b"}]}`, HookOptions{})
		if _, ok := p["session_crons"]; ok {
			t.Errorf("%s: session_crons kept: %s", ev, r.Payload)
		}
		if _, ok := p["background_tasks"]; ok {
			t.Errorf("%s: background_tasks kept: %s", ev, r.Payload)
		}
		derivedIs(t, r, "session_crons", "1")
		derivedIs(t, r, "background_tasks", "2")
	}
}

func TestKeyFilter(t *testing.T) {
	in := `{"hook_event_name":"SessionStart","a":{"access_token":"x","list":[{"Password":"p","ok":1},{"sessionKey":"k"}]},` +
		`"apikey":"x","api-key":"y","client_secret":"z","my_cookie":"c","credentials":{"x":1},"session_key":"s","fine":"yes"}`
	r, p := hook(t, in, HookOptions{})
	want := []string{"a.access_token", "a.list[].Password", "a.list[].sessionKey", "api-key", "apikey", "client_secret", "credentials", "my_cookie", "session_key"}
	if !reflect.DeepEqual(r.Redacted, want) {
		t.Fatalf("redacted\n got %v\nwant %v", r.Redacted, want)
	}
	if p["fine"] != "yes" || len(p) != 3 {
		t.Fatalf("payload = %s", r.Payload)
	}
}

func TestKeyFilterKeepsTokenCounters(t *testing.T) {
	in := `{"hook_event_name":"SessionStart","input_tokens":5,"usage":{"cache_read_input_tokens":1,"output_tokens_details":{"thinking_tokens":2}},` +
		`"exceeds_200k_tokens":false,"id_token":"abc","tokens":"abc","token":12}`
	r, p := hook(t, in, HookOptions{})
	want := []string{"id_token", "token", "tokens"}
	if !reflect.DeepEqual(r.Redacted, want) {
		t.Fatalf("redacted = %v, want %v", r.Redacted, want)
	}
	if p["input_tokens"] != json.Number("5") || p["exceeds_200k_tokens"] != false {
		t.Fatalf("counter lost: %s", r.Payload)
	}
	if !strings.Contains(string(r.Payload), "thinking_tokens") {
		t.Fatalf("nested counter lost: %s", r.Payload)
	}
}

func TestHookEventRejectsNonObject(t *testing.T) {
	for _, in := range []string{``, `[]`, `"x"`, `{`, `null`} {
		if _, err := NewHookEvent(testMeta, []byte(in), HookOptions{}); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
