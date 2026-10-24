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
	"/bin/wy statusline --wrap",
}

const syPath = "/home/u/.local/bin/wy"

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
	old := Wrap("bash x.sh", "/old/bin/wy")
	got := Wrap(old, "/new/bin/wy")
	if got != "/new/bin/wy statusline --wrap 'bash x.sh'" {
		t.Errorf("got %q", got)
	}
	if got := Wrap("/old/wy statusline", "/new/wy"); got != "/new/wy statusline" {
		t.Errorf("bare: %q", got)
	}
	if got := Wrap(`C:\bin\wy.exe statusline`, "/new/wy"); got != "/new/wy statusline" {
		t.Errorf("windows path: %q", got)
	}
}

func TestSyPathWithSpaces(t *testing.T) {
	p := "/home/my user/.local/bin/wy"
	w := Wrap("echo hi", p)
	if w != `'/home/my user/.local/bin/wy' statusline --wrap 'echo hi'` {
		t.Fatalf("got %q", w)
	}
	if c, ok := Unwrap(w); !ok || c != "echo hi" {
		t.Fatalf("unwrap %q %v", c, ok)
	}
}

func TestIsOurs(t *testing.T) {
	yes := []string{"wy statusline", "/x/wy statusline", "/x/wy.exe statusline --wrap 'a b'", `/x/wy statusline --wrap "a b"`, "/x/wy statusline --wrap abc"}
	no := []string{"", "bash x.sh", "/x/wy status", "/x/notsy statusline", "/x/wy statusline --wrap", "/x/wy statusline --wrap a b", "/x/wy statusline --other 'a'", "wy statusline 'unterminated"}
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
	if _, ok := Unwrap("/x/wy statusline"); ok {
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
