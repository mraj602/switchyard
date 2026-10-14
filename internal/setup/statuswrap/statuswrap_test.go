package statuswrap

import (
	"reflect"
	"testing"
)

var corpus = []string{
	"bash /home/u/.claude/statusline-command.sh",
	"echo 'hi'",
	`echo "it's $HOME"`,
	`printf '%s\n' "a\\b"`,
	"line1\nline2",
	"café ☕ — ünï",
	`a\b\\c`,
	"x'y'z''",
	"'",
	"$(date) `id` $X ${Y}",
	"  leading and trailing  ",
	"tab\tseparated",
	`C:\Program Files\x.exe --flag`,
	"echo statusline --wrap foo",
	"/bin/sy statusline --wrap",
}

const syPath = "/home/u/.local/bin/sy"

func TestRoundTrip(t *testing.T) {
	for _, c := range corpus {
		w := Wrap(c, syPath)
		got, ok := Unwrap(w)
		if !ok || got != c {
			t.Errorf("Unwrap(Wrap(%q)) = %q, %v (wrapped %q)", c, got, ok, w)
		}
		if !IsOurs(w) {
			t.Errorf("IsOurs(%q) = false", w)
		}
		if again := Wrap(w, syPath); again != w {
			t.Errorf("Wrap not idempotent for %q: %q != %q", c, again, w)
		}
	}
}

func TestWrapShapes(t *testing.T) {
	if got := Wrap("", syPath); got != syPath+" statusline" {
		t.Errorf("empty: %q", got)
	}
	want := syPath + ` statusline --wrap 'bash /h/s.sh'`
	if got := Wrap("bash /h/s.sh", syPath); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := Wrap("echo 'a'", syPath); got != syPath+` statusline --wrap 'echo '\''a'\'''` {
		t.Errorf("quote: %q", got)
	}
}

func TestDifferentSyPathUpdatedNotNested(t *testing.T) {
	old := Wrap("bash x.sh", "/old/bin/sy")
	got := Wrap(old, "/new/bin/sy")
	if got != "/new/bin/sy statusline --wrap 'bash x.sh'" {
		t.Errorf("got %q", got)
	}
	if got := Wrap("/old/sy statusline", "/new/sy"); got != "/new/sy statusline" {
		t.Errorf("bare: %q", got)
	}
	if got := Wrap(`C:\bin\sy.exe statusline`, "/new/sy"); got != "/new/sy statusline" {
		t.Errorf("windows path: %q", got)
	}
}

func TestSyPathWithSpaces(t *testing.T) {
	p := "/home/my user/.local/bin/sy"
	w := Wrap("echo hi", p)
	if w != `'/home/my user/.local/bin/sy' statusline --wrap 'echo hi'` {
		t.Fatalf("got %q", w)
	}
	if c, ok := Unwrap(w); !ok || c != "echo hi" {
		t.Fatalf("unwrap %q %v", c, ok)
	}
}

func TestIsOurs(t *testing.T) {
	yes := []string{"sy statusline", "/x/sy statusline", "/x/sy.exe statusline --wrap 'a b'", `/x/sy statusline --wrap "a b"`, "/x/sy statusline --wrap abc"}
	no := []string{"", "bash x.sh", "/x/sy status", "/x/notsy statusline", "/x/sy statusline --wrap", "/x/sy statusline --wrap a b", "/x/sy statusline --other 'a'", "sy statusline 'unterminated"}
	for _, c := range yes {
		if !IsOurs(c) {
			t.Errorf("IsOurs(%q) = false", c)
		}
	}
	for _, c := range no {
		if IsOurs(c) {
			t.Errorf("IsOurs(%q) = true", c)
		}
	}
	if _, ok := Unwrap("/x/sy statusline"); ok {
		t.Error("Unwrap of bare form reported ok")
	}
}

func TestWords(t *testing.T) {
	cases := map[string][]string{
		`a b  c`:       {"a", "b", "c"},
		`'a b' "c d"`:  {"a b", "c d"},
		`a\ b`:         {"a b"},
		`"a\"b" 'c\d'`: {`a"b`, `c\d`},
		`''`:           {""},
		"a\\\nb":       {"ab"},
		`"$HOME"`:      {"$HOME"},
		`it"'"s`:       {"it's"},
		`x'unclosed`:   nil,
	}
	for in, want := range cases {
		got, err := Words(in)
		if want == nil {
			if err == nil {
				t.Errorf("Words(%q) want error", in)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Words(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
