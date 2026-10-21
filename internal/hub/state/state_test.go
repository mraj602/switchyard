package state

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// loadRun reads a testdata/hooks/runs file; events are spaced 1s apart.
func loadRun(t *testing.T, path string) []Input {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Input
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for i := 0; sc.Scan(); i++ {
		var env struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		in, err := HookInput(fmt.Sprintf("%04d", i), t0.Add(time.Duration(i)*time.Second), env.Payload, nil)
		if err != nil {
			t.Fatal(err)
		}
		if in.Hook.Name != env.Event {
			t.Fatalf("line %d: event %q payload %q", i, env.Event, in.Hook.Name)
		}
		out = append(out, in)
	}
	return out
}

// steps returns "state:detail" after each input.
func steps(inputs []Input) []string {
	var s Session
	var out []string
	for _, in := range inputs {
		s.Apply(in)
		out = append(out, string(s.State)+":"+s.Detail)
	}
	return out
}

func TestFoldFixtureRuns(t *testing.T) {
	// Expected state after every event of each run (events with no row in the table keep
	// the state). Derived from states.md section 3.
	want := map[string][]string{
		"01-write-and-bash": {"done:started", "working:", "working:", "working:", "working:", "working:", "done:", "ended:other"},
		"02-permission-needed": {"done:started", "working:", "working:", "needs_permission:permission_request",
			"done:", "ended:other"},
		"03-question":       {"done:started", "working:", "asked:question", "ended:other"},
		"04-subagent":       {"done:started", "working:", "working:", "working:", "working:", "working:", "done:", "ended:other"},
		"05-tool-failure":   {"done:started", "working:", "working:", "working:", "done:", "ended:other"},
		"06-stop-failure":   {"done:started", "working:", "failed:model_not_found", "ended:other"},
		"07-compact":        {"done:started", "done:started", "ended:other"},
		"08-plain-with-env": {"done:started", "working:", "done:", "ended:other"},
	}
	files, _ := filepath.Glob("../../../testdata/hooks/runs/*.jsonl")
	if len(files) != len(want) {
		t.Fatalf("found %d runs, expect %d", len(files), len(want))
	}
	for _, f := range files {
		name := filepath.Base(f)
		name = name[:len(name)-len(".jsonl")]
		t.Run(name, func(t *testing.T) {
			in := loadRun(t, f)
			w, ok := want[name]
			if !ok {
				t.Fatalf("no expectation for %s", name)
			}
			if got := steps(in); !reflect.DeepEqual(got, w) {
				t.Errorf("steps\n got %q\nwant %q", got, w)
			}
			s := Fold(in)
			if s.State != Ended || s.EndReason != "other" || !s.EndedAt.Equal(in[len(in)-1].TS) {
				t.Errorf("final = %s/%s ended_at %v", s.State, s.EndReason, s.EndedAt)
			}
			if s.Detector != "q2" || s.NeedsYou() {
				t.Errorf("detector %q needs_you %v", s.Detector, s.NeedsYou())
			}
		})
	}
}

func TestFoldFixtureDetails(t *testing.T) {
	runs := "../../../testdata/hooks/runs/"
	// 03: asked just before SessionEnd, with the question text and since.
	in := loadRun(t, runs+"03-question.jsonl")
	s := Fold(in[:len(in)-1])
	if s.State != Asked || s.Detail != "question" || !s.NeedsYou() {
		t.Fatalf("03: %s:%s needs_you=%v", s.State, s.Detail, s.NeedsYou())
	}
	if s.Question != "Which colour do you prefer: red, green or blue?" || s.LastMessage != s.Question {
		t.Errorf("03: question %q last %q", s.Question, s.LastMessage)
	}
	if !s.Since.Equal(in[2].TS) {
		t.Errorf("03: since %v", s.Since)
	}
	if end := Fold(in); end.Question != "" {
		t.Errorf("question survives SessionEnd: %q", end.Question)
	}
	// 06: failed:model_not_found needs you.
	in = loadRun(t, runs+"06-stop-failure.jsonl")
	s = Fold(in[:len(in)-1])
	if s.State != Failed || s.Detail != "model_not_found" || !s.NeedsYou() || s.LastMessage == "" {
		t.Fatalf("06: %+v", s)
	}
	// 02: passes through needs_permission and the history shows it.
	in = loadRun(t, runs+"02-permission-needed.jsonl")
	var seen bool
	for _, tr := range Fold(in).History {
		if tr.To == NeedsPermission {
			seen = true
		}
	}
	if !seen {
		t.Error("02: history has no needs_permission")
	}
	// 03 without last messages allowed: never a question.
	in = loadRun(t, runs+"03-question.jsonl")
	in[2].Hook.HasLastMessage = false
	if s := Fold(in[:3]); s.State != Done {
		t.Errorf("no last message: state %s", s.State)
	}
}

// ---- synthetic scenarios ----

type ev struct {
	at   time.Duration
	hook *Hook
	list *ListObs
}

func fold(evs ...ev) Session {
	var in []Input
	for i, e := range evs {
		in = append(in, Input{ID: fmt.Sprintf("%03d", i), TS: t0.Add(e.at), Hook: e.hook, List: e.list})
	}
	return Fold(in)
}

func h(name string) *Hook { return &Hook{Name: name} }

func TestSyntheticNotifications(t *testing.T) {
	sec := time.Second
	t.Run("SYNTHETIC permission_prompt then prompt", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil},
			ev{sec, &Hook{Name: "Notification", NotificationType: "permission_prompt"}, nil})
		if s.State != NeedsPermission || s.Detail != "permission_prompt" || !s.NeedsYou() || !s.Since.Equal(t0.Add(sec)) {
			t.Fatalf("%+v", s)
		}
		s = fold(ev{0, h("UserPromptSubmit"), nil},
			ev{sec, &Hook{Name: "Notification", NotificationType: "permission_prompt"}, nil},
			ev{2 * sec, h("UserPromptSubmit"), nil})
		if s.State != Working {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("SYNTHETIC repeated permission_prompt keeps since", func(t *testing.T) {
		p := &Hook{Name: "Notification", NotificationType: "permission_prompt"}
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{sec, p, nil}, ev{5 * sec, p, nil})
		if !s.Since.Equal(t0.Add(sec)) || len(s.History) != 2 {
			t.Fatalf("since %v history %d", s.Since, len(s.History))
		}
	})
	t.Run("SYNTHETIC elicitation_dialog", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil},
			ev{sec, &Hook{Name: "Notification", NotificationType: "elicitation_dialog"}, nil})
		if s.State != Asked || s.Detail != "elicitation" || !s.NeedsYou() {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("SYNTHETIC idle_prompt repairs working only", func(t *testing.T) {
		idle := &Hook{Name: "Notification", NotificationType: "idle_prompt"}
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{sec, idle, nil})
		if s.State != Done || s.Detail != "repaired" {
			t.Fatalf("%+v", s)
		}
		s = fold(ev{0, h("PermissionRequest"), nil}, ev{sec, idle, nil})
		if s.State != NeedsPermission {
			t.Fatalf("idle cleared permission: %+v", s)
		}
		s = fold(ev{0, &Hook{Name: "Notification", NotificationType: "elicitation_dialog"}, nil}, ev{sec, idle, nil})
		if s.State != Asked {
			t.Fatalf("idle cleared asked: %+v", s)
		}
	})
	t.Run("SYNTHETIC other notification types change nothing", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil},
			ev{sec, &Hook{Name: "Notification", NotificationType: "auth_success"}, nil},
			ev{2 * sec, &Hook{Name: "Notification", NotificationType: "brand_new"}, nil})
		if s.State != Working {
			t.Fatalf("%+v", s)
		}
	})
}

func TestTransitions(t *testing.T) {
	sec := time.Second
	tests := []struct {
		name string
		evs  []ev
		want string
	}{
		{"ask tool", []ev{{0, h("UserPromptSubmit"), nil}, {sec, &Hook{Name: "PreToolUse", ToolName: "AskUserQuestion", Question: "red?"}, nil}}, "asked:tool"},
		{"plan ready", []ev{{0, &Hook{Name: "PreToolUse", ToolName: "ExitPlanMode"}, nil}}, "asked:tool"},
		{"tool event clears permission", []ev{{0, h("PermissionRequest"), nil}, {sec, &Hook{Name: "PostToolUse", ToolName: "Bash"}, nil}}, "working:"},
		{"PermissionDenied", []ev{{0, h("PermissionRequest"), nil}, {sec, h("PermissionDenied"), nil}}, "working:"},
		{"PermissionDenied elsewhere", []ev{{0, h("UserPromptSubmit"), nil}, {sec, h("PermissionDenied"), nil}}, "working:"},
		{"stop hook active", []ev{{0, h("UserPromptSubmit"), nil}, {sec, &Hook{Name: "Stop", StopHookActive: true}, nil}}, "working:"},
		{"stop overloaded", []ev{{0, h("UserPromptSubmit"), nil}, {sec, &Hook{Name: "StopFailure", Error: "overloaded"}, nil}}, "failed:overloaded"},
		{"resume", []ev{{0, &Hook{Name: "SessionStart", Source: "resume"}, nil}}, "done:resumed"},
		{"unknown event", []ev{{0, h("UserPromptSubmit"), nil}, {sec, h("FutureEvent"), nil}}, "working:"},
		{"subagent ignored", []ev{{0, h("UserPromptSubmit"), nil}, {sec, h("SubagentStop"), nil}}, "working:"},
		{"compact keeps state", []ev{{0, h("UserPromptSubmit"), nil}, {sec, &Hook{Name: "SessionStart", Source: "compact"}, nil}}, "working:"},
		{"compact as first event", []ev{{0, &Hook{Name: "SessionStart", Source: "compact"}, nil}}, "done:"},
		{"compact reopens as done", []ev{{0, h("SessionEnd"), nil}, {sec, &Hook{Name: "SessionStart", Source: "compact"}, nil}}, "done:compact"},
		{"restart after end", []ev{{0, h("SessionEnd"), nil}, {sec, h("UserPromptSubmit"), nil}}, "working:"},
		{"stop after end reopens", []ev{{0, h("SessionEnd"), nil}, {sec, h("Stop"), nil}}, "done:"},
		{"idle_prompt after end stays ended", []ev{{0, h("SessionEnd"), nil}, {sec, &Hook{Name: "Notification", NotificationType: "idle_prompt"}, nil}}, "ended:other"},
		{"second SessionEnd ignored", []ev{{0, &Hook{Name: "SessionEnd", Reason: "clear"}, nil}, {sec, &Hook{Name: "SessionEnd", Reason: "other"}, nil}}, "ended:clear"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := fold(tc.evs...)
			if got := string(s.State) + ":" + s.Detail; got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestReopenClearsEnd(t *testing.T) {
	s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{time.Second, h("SessionEnd"), nil}, ev{2 * time.Second, h("UserPromptSubmit"), nil})
	if s.State != Working || !s.EndedAt.IsZero() || s.EndReason != "" {
		t.Fatalf("%+v", s)
	}
}

func TestNeedsYou(t *testing.T) {
	for _, tc := range []struct {
		s    Session
		want bool
	}{
		{Session{State: Working}, false}, {Session{State: NeedsPermission}, true}, {Session{State: Asked}, true},
		{Session{State: Done}, false}, {Session{State: Failed, Detail: "rate_limit"}, true},
		{Session{State: Failed, Detail: "something_new"}, true}, {Session{State: Failed, Detail: "overloaded"}, false},
		{Session{State: Failed, Detail: "server_error"}, false}, {Session{State: Ended}, false}, {Session{State: Gone}, false},
	} {
		if got := tc.s.NeedsYou(); got != tc.want {
			t.Errorf("%s:%s = %v", tc.s.State, tc.s.Detail, got)
		}
	}
}

func list(complete, listed bool, status string) *ListObs {
	return &ListObs{Complete: complete, Listed: listed, Status: status}
}

func TestGone(t *testing.T) {
	m := time.Minute
	t.Run("two complete misses", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(true, false, "")}, ev{10 * m, nil, list(true, false, "")})
		if s.State != Gone || s.EndReason != "gone" || !s.EndedAt.Equal(t0.Add(10*m)) || s.NeedsYou() {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("one miss is not enough", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(true, false, "")})
		if s.State != Working || s.MissedLists != 1 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("incomplete lists do not count", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(false, false, "")}, ev{10 * m, nil, list(false, false, "")})
		if s.State != Working || s.MissedLists != 0 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("miss within 60s of last event does not count", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{30 * time.Second, nil, list(true, false, "")},
			ev{59 * time.Second, nil, list(true, false, "")})
		if s.MissedLists != 0 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("exactly 60s counts", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{60 * time.Second, nil, list(true, false, "")})
		if s.MissedLists != 1 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("listing in between resets", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(true, false, "")},
			ev{10 * m, nil, list(true, true, "busy")}, ev{15 * m, nil, list(true, false, "")})
		if s.State != Working || s.MissedLists != 1 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("an event in between resets", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(true, false, "")},
			ev{6 * m, h("Stop"), nil}, ev{15 * m, nil, list(true, false, "")})
		if s.State != Done || s.MissedLists != 1 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("done sessions also go gone", func(t *testing.T) {
		s := fold(ev{0, h("Stop"), nil}, ev{5 * m, nil, list(true, false, "")}, ev{10 * m, nil, list(true, false, "")})
		if s.State != Gone {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("ended sessions do not become gone", func(t *testing.T) {
		s := fold(ev{0, h("SessionEnd"), nil}, ev{5 * m, nil, list(true, false, "")}, ev{10 * m, nil, list(true, false, "")})
		if s.State != Ended {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("gone comes back on event", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(true, false, "")}, ev{10 * m, nil, list(true, false, "")},
			ev{11 * m, h("UserPromptSubmit"), nil})
		if s.State != Working || !s.EndedAt.IsZero() || s.EndReason != "" {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("gone comes back on list entry", func(t *testing.T) {
		s := fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(true, false, "")}, ev{10 * m, nil, list(true, false, "")},
			ev{15 * m, nil, list(true, true, "busy")})
		if s.State != Working || s.Detail != "repaired" {
			t.Fatalf("%+v", s)
		}
		s = fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(true, false, "")}, ev{10 * m, nil, list(true, false, "")},
			ev{15 * m, nil, list(true, true, "idle")})
		if s.State != Done {
			t.Fatalf("idle: %+v", s)
		}
		s = fold(ev{0, h("UserPromptSubmit"), nil}, ev{5 * m, nil, list(true, false, "")}, ev{10 * m, nil, list(true, false, "")},
			ev{15 * m, nil, list(true, true, "waiting")})
		if s.State != Done {
			t.Fatalf("waiting: %+v", s)
		}
	})
}

func TestListRepair(t *testing.T) {
	m := time.Minute
	tests := []struct {
		name string
		evs  []ev
		want string
	}{
		{"busy repairs done", []ev{{0, h("Stop"), nil}, {m, nil, list(true, true, "busy")}}, "working:repaired"},
		{"busy keeps permission", []ev{{0, h("PermissionRequest"), nil}, {m, nil, list(true, true, "busy")}}, "needs_permission:permission_request"},
		{"busy keeps failed", []ev{{0, &Hook{Name: "StopFailure", Error: "rate_limit"}, nil}, {m, nil, list(true, true, "busy")}}, "failed:rate_limit"},
		{"idle repairs working", []ev{{0, h("UserPromptSubmit"), nil}, {m, nil, list(true, true, "idle")}}, "done:repaired"},
		{"idle keeps asked", []ev{{0, &Hook{Name: "Elicitation"}, nil}, {m, nil, list(true, true, "idle")}}, "asked:elicitation"},
		{"idle keeps permission", []ev{{0, h("PermissionRequest"), nil}, {m, nil, list(true, true, "idle")}}, "needs_permission:permission_request"},
		{"waiting changes nothing", []ev{{0, h("UserPromptSubmit"), nil}, {m, nil, list(true, true, "waiting")}}, "working:"},
		{"incomplete list still repairs", []ev{{0, h("UserPromptSubmit"), nil}, {m, nil, list(false, true, "idle")}}, "done:repaired"},
		{"list does not touch last_event_at", []ev{{0, h("UserPromptSubmit"), nil}, {m, nil, list(true, true, "busy")}}, "working:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := fold(tc.evs...)
			if got := string(s.State) + ":" + s.Detail; got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if !s.LastEventAt.Equal(t0) && tc.evs[0].hook != nil && !s.LastEventAt.Equal(t0.Add(tc.evs[0].at)) {
				t.Errorf("last_event_at moved: %v", s.LastEventAt)
			}
		})
	}
}

func TestFoldOrdersAndIsPure(t *testing.T) {
	a := Input{ID: "a", TS: t0, Hook: h("UserPromptSubmit")}
	b := Input{ID: "b", TS: t0.Add(time.Second), Hook: h("Stop")}
	c := Input{ID: "c", TS: t0.Add(time.Second), Hook: h("UserPromptSubmit")}
	s1 := Fold([]Input{a, b, c})
	s2 := Fold([]Input{c, b, a}) // late/unordered delivery: same result
	if !reflect.DeepEqual(s1, s2) || s1.State != Working {
		t.Fatalf("\n%+v\n%+v", s1, s2)
	}
	in := []Input{c, a}
	Fold(in)
	if in[0].ID != "c" {
		t.Error("Fold reordered its argument")
	}
}

func TestHistory(t *testing.T) {
	s := fold(ev{0, h("SessionStart"), nil}, ev{time.Second, h("UserPromptSubmit"), nil}, ev{2 * time.Second, h("Stop"), nil})
	var got []string
	for _, tr := range s.History {
		got = append(got, fmt.Sprintf("%s>%s:%s(%s)", tr.From, tr.To, tr.Detail, tr.Cause))
	}
	want := []string{">done:started(SessionStart)", "done>working:(UserPromptSubmit)", "working>done:(Stop)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if !s.Since.Equal(t0.Add(2 * time.Second)) {
		t.Errorf("since %v", s.Since)
	}
}

func TestStopMessageHandling(t *testing.T) {
	msg := "Should I continue?"
	s := fold(ev{0, &Hook{Name: "Stop", LastMessage: msg, HasLastMessage: true}, nil})
	if s.State != Asked || s.Question != msg || s.LastMessage != msg {
		t.Fatalf("%+v", s)
	}
	// Stop with stop_hook_active changes nothing, including the message.
	s = fold(ev{0, &Hook{Name: "Stop", LastMessage: "ok", HasLastMessage: true}, nil},
		ev{time.Second, &Hook{Name: "Stop", StopHookActive: true, LastMessage: msg, HasLastMessage: true}, nil})
	if s.State != Done || s.LastMessage != "ok" {
		t.Fatalf("%+v", s)
	}
}

func TestHookInputRecordMessageWins(t *testing.T) {
	m := "derived tail"
	in, err := HookInput("x", t0, []byte(`{"hook_event_name":"Stop","last_assistant_message":"raw"}`), &m)
	if err != nil || in.Hook.LastMessage != m {
		t.Fatalf("%v %+v", err, in.Hook)
	}
	in, _ = HookInput("x", t0, []byte(`{"hook_event_name":"Stop"}`), nil)
	if in.Hook.HasLastMessage {
		t.Error("message from nowhere")
	}
	if _, err := HookInput("x", t0, []byte(`{`), nil); err == nil {
		t.Error("bad json accepted")
	}
}
