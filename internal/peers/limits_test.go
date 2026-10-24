package peers

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanText(t *testing.T) {
	in := "a\r\nb\x00c\x1bd\u0085e\u202ef\u200bg\u2066h\ti\nj\r"
	if got, want := CleanText(in), "a\nbcdefgh\ti\nj"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCheckText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code string
	}{
		{"ok", "hello there", ""},
		{"empty", "  \n\t ", CodeNotText},
		{"only controls", "\x00\x01\u200b", CodeNotText},
		{"not utf8", "a\xff\xfeb", CodeNotText},
		{"exactly 64k", strings.Repeat("a", MaxTextBytes), ""},
		{"over 64k", strings.Repeat("a", MaxTextBytes+1), CodeTooBig},
		{"size counts after cleaning", strings.Repeat("a", MaxTextBytes) + "\x00\x00", ""},
		{"pem", "x -----BEGIN OPENSSH PRIVATE KEY----- y", CodeSecret},
		{"pem rsa", "-----BEGIN RSA PRIVATE KEY-----", CodeSecret},
		{"wy token", "token wy_m_4fQx0Lr2abcdefghijklmnopqrstuvwxyzABCDEF", CodeSecret},
		{"anthropic key", "sk-ant-api03-abcdEFGH1234", CodeSecret},
		{"aws", "id AKIAIOSFODNN7EXAMPLE here", CodeSecret},
		{"github ghp", "ghp_" + strings.Repeat("a", 36), CodeSecret},
		{"github pat", "github_pat_" + strings.Repeat("A1", 20), CodeSecret},
		{"slack", "xoxb-1234567890-abcdefgh", CodeSecret},
		// Negative cases: look similar, must pass.
		{"public key header", "-----BEGIN PUBLIC KEY-----", ""},
		{"short wy_", "wy_m is the machine token kind", ""},
		{"akia lowercase", "akiaiosfodnn7example", ""},
		{"word ghp", "the ghp_ prefix is for GitHub", ""},
		{"xox without body", "xoxb-", ""},
	}
	for _, c := range cases {
		_, r := CheckText(c.in)
		got := ""
		if r != nil {
			got = r.Code
		}
		if got != c.code {
			t.Errorf("%s: code %q, want %q (%v)", c.name, got, c.code, r)
		}
	}
	if _, r := CheckText("sk-ant-api03-abcdEFGH1234"); r != nil && strings.Contains(r.Detail, "abcd") {
		t.Error("the refusal must not echo the secret")
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[string]int{
		CodeNotAllowed: 5, CodePaused: 5, CodeLoopGuard: 5, CodeOff: 5,
		CodeTooBig: 6, CodeRateLimited: 6, CodeSecret: 6, CodeNotText: 6,
		CodeExpired: 7, CodeTargetGone: 7, CodeRejected: 7, CodeFailed: 7,
		CodeAmbiguous: 2, CodeNotASession: 4, CodeUnsupported: 3, "mystery": 1,
	}
	for c, want := range cases {
		if got := ExitCode(c); got != want {
			t.Errorf("ExitCode(%s) = %d, want %d", c, got, want)
		}
	}
}

func TestRateSessionBurstAndRefill(t *testing.T) {
	st := NewRateState()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < SessionBurst; i++ {
		if _, ok := CheckRate(st, now, "s", PairKey("s", "t"+string(rune('a'+i)))); !ok {
			t.Fatalf("send %d refused inside the burst", i)
		}
	}
	wait, ok := CheckRate(st, now, "s", PairKey("s", "z"))
	if ok || wait <= 0 {
		t.Fatalf("burst exhausted: ok=%v wait=%v", ok, wait)
	}
	// 10 a minute: one token per 6 s.
	if _, ok := CheckRate(st, now.Add(5*time.Second), "s", PairKey("s", "z")); ok {
		t.Error("5 s later must still be refused")
	}
	if _, ok := CheckRate(st, now.Add(7*time.Second), "s", PairKey("s", "z")); !ok {
		t.Error("7 s later must pass")
	}
	// Another session has its own bucket.
	if _, ok := CheckRate(st, now, "other", PairKey("other", "z")); !ok {
		t.Error("a different session must not share the bucket")
	}
}

func TestRatePairPerHour(t *testing.T) {
	st := NewRateState()
	now := time.Unix(1_700_000_000, 0)
	pair := PairKey("a", "b")
	for i := 0; i < PairPerHour; i++ {
		// One a minute stays under the session rate.
		if _, ok := CheckRate(st, now.Add(time.Duration(i)*time.Minute*59/60), "a", pair); !ok {
			t.Fatalf("send %d refused", i)
		}
	}
	at := now.Add(58 * time.Minute)
	wait, ok := CheckRate(st, at, "b", pair)
	if ok {
		t.Fatal("61st message in the hour must be refused, whichever side sends")
	}
	if wait <= 0 || wait > 3*time.Minute {
		t.Errorf("retry hint %v", wait)
	}
	if _, ok := CheckRate(st, now.Add(61*time.Minute), "a", pair); !ok {
		t.Error("an hour later it must pass again")
	}
	if PairKey("a", "b") != PairKey("b", "a") {
		t.Error("PairKey must not depend on direction")
	}
}

func TestRateRefusalChangesNothing(t *testing.T) {
	st := NewRateState()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < PairPerHour; i++ {
		CheckRate(st, now.Add(time.Duration(i)*time.Second*50), "x"+string(rune(i)), PairKey("a", "b"))
	}
	n := len(st.Pairs[PairKey("a", "b")])
	CheckRate(st, now.Add(time.Hour/2), "late", PairKey("a", "b"))
	if len(st.Pairs[PairKey("a", "b")]) != n {
		t.Error("a refused check recorded a send")
	}
}

func TestRateStateFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "peers", "limits.json")
	if got := LoadRates(p); len(got.Sessions) != 0 {
		t.Fatal("missing file must give an empty state")
	}
	st := NewRateState()
	now := time.Unix(1_700_000_000, 0)
	CheckRate(st, now, "s", "p")
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}
	back := LoadRates(p)
	if len(back.Pairs["p"]) != 1 || back.Sessions["s"].Tokens != SessionBurst-1 {
		t.Errorf("round trip: %+v", back)
	}
	st.Prune(now.Add(3 * time.Hour))
	if len(st.Sessions)+len(st.Pairs) != 0 {
		t.Error("prune left idle entries")
	}
}

func TestLoopGuard(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	th := &Thread{ID: "t_x", A: "a", B: "b", OpenedAt: t0, LastAt: t0}
	human := t0
	for i := 0; i < 30; i++ {
		if LoopGuard(th, human, 30, t0) {
			t.Fatalf("message %d refused before the limit", i+1)
		}
		th.RecordMessage(t0)
	}
	if !LoopGuard(th, human, 30, t0.Add(time.Minute)) {
		t.Fatal("31st automated turn must be refused")
	}
	if th.PausedAt == nil {
		t.Error("thread must be marked paused")
	}
	// Still refused, same human time.
	if !LoopGuard(th, human, 30, t0.Add(2*time.Minute)) {
		t.Error("stays refused with no new human prompt")
	}
	// A new human prompt in either session resets turns and clears the pause.
	if LoopGuard(th, human.Add(time.Hour), 30, t0.Add(3*time.Minute)) {
		t.Fatal("a human prompt must lift the stop")
	}
	if th.Turns != 0 || th.PausedAt != nil {
		t.Errorf("turns=%d paused=%v", th.Turns, th.PausedAt)
	}
}

func TestLoopGuardEdges(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	// max_turns 2: two pass, third refused.
	th := &Thread{}
	for i := 0; i < 2; i++ {
		if LoopGuard(th, t0, 2, t0) {
			t.Fatalf("turn %d refused", i+1)
		}
		th.RecordMessage(t0)
	}
	if !LoopGuard(th, t0, 2, t0) {
		t.Error("third must be refused at max_turns 2")
	}
	// An older human time never resets.
	if !LoopGuard(th, t0.Add(-time.Hour), 2, t0) {
		t.Error("an older human prompt must not reset")
	}
	// max_turns 0 is no limit: any count passes, an earlier stop is lifted, the count is kept.
	stop := t0
	th2 := &Thread{Turns: 100_000, PausedAt: &stop, HumanAt: t0}
	if LoopGuard(th2, t0, NoTurnLimit, t0) {
		t.Error("no limit must never refuse")
	}
	if th2.PausedAt != nil || th2.Turns != 100_000 {
		t.Errorf("no limit: turns=%d paused=%v", th2.Turns, th2.PausedAt)
	}
	// A limit set later applies to the count kept meanwhile.
	if !LoopGuard(th2, t0, 500, t0) {
		t.Error("a limit set after no limit must refuse a thread already past it")
	}
}

func TestThreadIDAndKind(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_123)
	id := ThreadID("sess-a", "sess-b", at)
	if !strings.HasPrefix(id, "t_") || len(id) != 18 {
		t.Fatalf("id %q", id)
	}
	if id != ThreadID("sess-a", "sess-b", at) || id == ThreadID("sess-b", "sess-a", at) {
		t.Error("id must be stable and ordered (a, b)")
	}
	th := Thread{A: "a", B: "b"}
	if th.KindFor("b") != KindReply || th.KindFor("a") != KindStart {
		t.Error("kind: b replies, a starts")
	}
}

func TestThreadSetLifecycle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var s ThreadSet
	th := s.Start("a", "b", "acc1", "acc2", now)
	if s.FindOpen("b", "a") == nil || s.FindOpen("a", "c") != nil {
		t.Fatal("FindOpen")
	}
	if th.ID != ThreadID("a", "b", now) {
		t.Error("thread id")
	}
	// Idle 24 h closes; session end closes.
	s.Expire(now.Add(23*time.Hour), func(string) bool { return true })
	if s.FindOpen("a", "b") == nil {
		t.Error("closed too early")
	}
	s.Expire(now.Add(23*time.Hour), func(id string) bool { return id != "b" })
	if s.FindOpen("a", "b") != nil {
		t.Error("session b ended: thread must close")
	}
	s.Start("a", "b", "", "", now.Add(24*time.Hour))
	if s.FindOpen("a", "b") == nil {
		t.Error("a new thread can open after the old one closed")
	}
	s.Expire(now.Add(26*time.Hour), nil)
	if s.FindOpen("a", "b") == nil {
		t.Error("nil live must not close")
	}
	s.Expire(now.Add(24*time.Hour+25*time.Hour), nil)
	if s.FindOpen("a", "b") != nil {
		t.Error("idle 24 h must close")
	}
	s.Expire(now.Add(30*24*time.Hour), nil)
	if len(s.Threads) != 0 {
		t.Errorf("old closed threads must be forgotten: %d", len(s.Threads))
	}
}

func TestThreadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "peers", "threads.json")
	if len(LoadThreads(p).Threads) != 0 {
		t.Fatal("missing")
	}
	var s ThreadSet
	s.V = 1
	now := time.Unix(1_700_000_000, 0).UTC()
	s.Start("a", "b", "x", "y", now).RecordMessage(now)
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	back := LoadThreads(p)
	if len(back.Threads) != 1 || back.Threads[0].Turns != 1 || !back.Threads[0].OpenedAt.Equal(now) {
		t.Errorf("%+v", back)
	}
}
