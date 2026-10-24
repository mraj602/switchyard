package termctl

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestTitleGoldens(t *testing.T) {
	cases := []struct{ label, project, word, want string }{
		{"work", "api", WordWorking, "work · api · working"},
		{"work", "api", WordPermission, "work · api · permission"},
		{"work", "api", WordAsked, "work · api · asked"},
		{"work", "api", WordDone, "work · api · done"},
		{"work", "api", WordFailed, "work · api · failed"},
		{"work", "api", WordLimit, "work · api · limit"},
		{"work", "api", WordEnded, "work · api · ended"},
		{"work", "", WordDone, "work · done"},
		{"", "api", WordDone, ""},
		{"a;b", "x\x07y", WordDone, "a,b · xy · done"},
	}
	for _, c := range cases {
		if got := Title(c.label, c.project, c.word); got != c.want {
			t.Errorf("Title(%q,%q,%q) = %q, want %q", c.label, c.project, c.word, got, c.want)
		}
	}
	long := Title("work", strings.Repeat("p", 80), WordDone)
	if n := utf8.RuneCountInString(long); n != MaxTitleRunes || !strings.HasSuffix(long, "…") {
		t.Errorf("long title: %d runes, %q", n, long)
	}
}

func TestTerminalSequenceShape(t *testing.T) {
	b := TerminalSequence("work · api · done")
	want := `{"terminalSequence":"\u001b]2;work · api · done\u0007"}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
	if TerminalSequence("") != nil {
		t.Fatal("empty title must give no output")
	}
}

// A hostile label or cwd never leaves the OSC 2 payload: the decoded sequence is exactly
// ESC ] 2 ; <printable text> BEL, with no other control or separator inside.
func TestHostileTitleStaysInPayload(t *testing.T) {
	seeds := []string{
		"a;b", "a\x07b", "a\x1bb", "a\u009db", "a\u009cb", "\x1b]0;pwn\x07", "\x1b\\", "x‮y", "a b",
		"a\nb", "\u0000", "\x1b[31m", "\xff\xfe", "a​b", "a⁦b",
	}
	for _, s := range seeds {
		checkPayload(t, s)
	}
}

func FuzzHostileTitle(f *testing.F) {
	for _, s := range []string{"a;b", "a\x07", "\x1b]2;x", "\u009d", "plain", "\xc2"} {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, label, cwd string) {
		checkPayload(t, label)
		checkPayload(t, ProjectOf(cwd))
	})
}

func checkPayload(t *testing.T, s string) {
	t.Helper()
	out := TerminalSequence(Title(s, s, WordDone))
	if out == nil {
		return // nothing printable: nothing is printed
	}
	var o map[string]string
	if err := json.Unmarshal(out, &o); err != nil || len(o) != 1 {
		t.Fatalf("not one JSON object: %q (%v)", out, err)
	}
	seq := o["terminalSequence"]
	if !strings.HasPrefix(seq, "\x1b]2;") || !strings.HasSuffix(seq, "\x07") {
		t.Fatalf("bad framing %q", seq)
	}
	body := seq[len("\x1b]2;") : len(seq)-1]
	if !utf8.ValidString(body) {
		t.Fatalf("invalid UTF-8 in %q", body)
	}
	if strings.ContainsRune(body, ';') {
		t.Fatalf("; inside payload %q", body)
	}
	for _, r := range body {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			t.Fatalf("control %U in payload %q", r, body)
		}
	}
	if utf8.RuneCountInString(body) > MaxTitleRunes {
		t.Fatalf("title too long: %d", utf8.RuneCountInString(body))
	}
}

func TestAllowList(t *testing.T) {
	hex32 := strings.Repeat("0123456789abcdef", 2)
	ok := []string{"warp://session/" + hex32, "warppreview://session/" + hex32}
	for _, u := range ok {
		if !ValidFocusURL(u) {
			t.Errorf("rejects the real format %q", u)
		}
	}
	bad := []string{
		"file:///etc/passwd", "https://claude.ai/x", "javascript:alert(1)",
		"warp://session/" + strings.ToUpper(hex32),
		"warp://session/" + hex32[:31], "warp://session/" + hex32 + "0",
		"warp://session/%2f" + hex32[:29], "warp://session/" + hex32 + " ", "warp://session/" + hex32 + "\n",
		" warp://session/" + hex32, "warp://tab_config/watchyard-work", "warp://session/", "",
		"warp://session/" + hex32 + "/../x", "WARP://session/" + hex32,
	}
	for _, u := range bad {
		if ValidFocusURL(u) {
			t.Errorf("accepts %q", u)
		}
	}
	if !ValidTabConfigURL("warp://tab_config/watchyard-work") || ValidTabConfigURL("warp://tab_config/other") ||
		ValidTabConfigURL("warp://tab_config/watchyard-Work") || ValidTabConfigURL("warp://tab_config/watchyard-a b") {
		t.Error("tab config pattern")
	}
	if !ValidRemoteURL("https://claude.ai/code/session_1") || ValidRemoteURL("https://evil.example/") ||
		ValidRemoteURL("http://claude.ai/x") || ValidRemoteURL("https://claude.ai/x y") {
		t.Error("remote pattern")
	}
}

func TestOpenerArgvGolden(t *testing.T) {
	u := "warp://session/" + strings.Repeat("ab", 16)
	want := map[string][]string{
		"linux":   {"xdg-open", u},
		"darwin":  {"open", u},
		"windows": {"rundll32.exe", "url.dll,FileProtocolHandler", u},
	}
	for goos, w := range want {
		var got []string
		o := ExecOpener{GOOS: goos, Run: func(_ context.Context, argv []string) ([]byte, error) {
			got = argv
			return nil, nil
		}}
		if err := o.Open(context.Background(), u); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("%s: argv %v, want %v", goos, got, w)
		}
	}
}

func TestOpenRefusesAndReportsFailure(t *testing.T) {
	called := false
	var logged strings.Builder
	o := ExecOpener{GOOS: "linux", Stderr: &logged, Run: func(context.Context, []string) ([]byte, error) {
		called = true
		return []byte("boom\n"), errors.New("exit 1")
	}}
	if err := o.Open(context.Background(), "file:///etc/passwd"); !errors.Is(err, ErrNotAllowed) || called {
		t.Fatalf("err %v, called %v", err, called)
	}
	err := o.Open(context.Background(), "warp://session/"+strings.Repeat("0", 32))
	if err == nil || logged.String() != "boom\n" {
		t.Fatalf("err %v, stderr %q", err, logged.String())
	}
}
