package jsonedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func mustSet(t *testing.T, doc, ptr, val string) string {
	t.Helper()
	out, _, err := Set([]byte(doc), ptr, []byte(val))
	if err != nil {
		t.Fatalf("Set(%s): %v", ptr, err)
	}
	if !json.Valid(out) {
		t.Fatalf("result does not parse: %s", out)
	}
	return string(out)
}

func TestSetReplaceKeepsEverythingElse(t *testing.T) {
	doc := "{\n  \"b\": 1.0e0,\n  \"a\": {\"x\": [1,  2]},\n    \"c\":\"é\\u00e9\"\n}\n"
	got := mustSet(t, doc, "/a/x", `[3]`)
	want := "{\n  \"b\": 1.0e0,\n  \"a\": {\"x\": [\n    3\n  ]},\n    \"c\":\"é\\u00e9\"\n}\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestSetInsertStyles(t *testing.T) {
	cases := []struct{ name, doc, ptr, val, want string }{
		{"pretty", "{\n  \"a\": 1\n}\n", "/b", `2`, "{\n  \"a\": 1,\n  \"b\": 2\n}\n"},
		{"tabs", "{\n\t\"a\": 1\n}", "/b", `{"c":true}`, "{\n\t\"a\": 1,\n\t\"b\": {\n\t\t\"c\": true\n\t}\n}"},
		{"four", "{\n    \"a\": {\n        \"k\": 1\n    }\n}", "/a/z", `"v"`, "{\n    \"a\": {\n        \"k\": 1,\n        \"z\": \"v\"\n    }\n}"},
		{"empty root", "{}\n", "/mcpServers", `{"s":{"command":"x"}}`, "{\n  \"mcpServers\": {\n    \"s\": {\n      \"command\": \"x\"\n    }\n  }\n}\n"},
		{"compact", `{"a":1}`, "/b", `{ "c" : 1 }`, `{"a":1,"b":{"c":1}}`},
		{"nested create", "{\n  \"a\": 1\n}", "/env/K", `"v"`, "{\n  \"a\": 1,\n  \"env\": {\n    \"K\": \"v\"\n  }\n}"},
		{"empty inner pretty", "{\n  \"env\": {}\n}", "/env/K", `"v"`, "{\n  \"env\": {\n    \"K\": \"v\"\n  }\n}"},
		{"array append", "{\"l\": [1]}", "/l/-", `2`, `{"l": [1,2]}`},
		{"escape key", "{}", "/a~1b", `1`, "{\n  \"a/b\": 1\n}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mustSet(t, c.doc, c.ptr, c.val); got != c.want {
				t.Fatalf("got\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestSetUnchangedIsNoop(t *testing.T) {
	doc := "{\n  \"a\": {  \"x\":1 }\n}"
	out, ch, err := Set([]byte(doc), "/a", []byte(`{"x":1}`))
	if err != nil || ch.Changed || string(out) != doc || !ch.BeforePresent {
		t.Fatalf("out=%q ch=%+v err=%v", out, ch, err)
	}
}

func TestDelete(t *testing.T) {
	cases := []struct{ doc, ptr, want string }{
		{"{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}", "/a", "{\n  \"b\": 2,\n  \"c\": 3\n}"},
		{"{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}", "/b", "{\n  \"a\": 1,\n  \"c\": 3\n}"},
		{"{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}", "/c", "{\n  \"a\": 1,\n  \"b\": 2\n}"},
		{"{\n  \"a\": 1\n}", "/a", "{}"},
		{`{"l":[1,2,3]}`, "/l/1", `{"l":[1,3]}`},
		{`{"a":1}`, "/zzz", `{"a":1}`},
		{`{"a":1}`, "/a/b", `{"a":1}`},
	}
	for _, c := range cases {
		out, _, err := Delete([]byte(c.doc), c.ptr)
		if err != nil || string(out) != c.want {
			t.Errorf("Delete(%s) = %q, %v; want %q", c.ptr, out, err, c.want)
		}
	}
}

func TestErrors(t *testing.T) {
	if _, _, err := Set([]byte(`{"a":1}`), "/a/b", []byte(`1`)); !errors.Is(err, ErrNotObject) {
		t.Errorf("scalar parent: %v", err)
	}
	if _, _, err := Set([]byte(`{`), "/a", []byte(`1`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid doc: %v", err)
	}
	if _, _, err := Set([]byte(`{}`), "/a", []byte(`{`)); !errors.Is(err, ErrBadValue) {
		t.Errorf("bad value: %v", err)
	}
	if _, _, err := Set([]byte(`{}`), "", []byte(`1`)); !errors.Is(err, ErrBadPointer) {
		t.Errorf("empty pointer: %v", err)
	}
	if _, _, err := Set([]byte(`{"l":[1]}`), "/l/5", []byte(`1`)); !errors.Is(err, ErrOutOfRange) {
		t.Errorf("range: %v", err)
	}
}

func TestOauthAccountByteIdentical(t *testing.T) {
	oauth := `{"emailAddress":"a@b.c",  "organizationUuid":"x",
      "n": 1.50}`
	doc := "{\n  \"oauthAccount\": " + oauth + ",\n  \"mcpServers\": {\"old\": {}},\n  \"numStartups\": 12\n}\n"
	out := mustSet(t, doc, "/mcpServers", `{"new":{"command":"c","args":["a"]}}`)
	if !strings.Contains(out, oauth) {
		t.Fatalf("oauthAccount bytes changed:\n%s", out)
	}
}

// checkChange asserts the splice invariants: bytes outside the span are unchanged and the
// result parses.
func checkChange(t testing.TB, doc []byte, c Change) []byte {
	out := c.Apply(doc)
	if !c.Changed {
		if !bytes.Equal(out, doc) {
			t.Fatalf("unchanged but bytes differ")
		}
		return out
	}
	if c.Start < 0 || c.End < c.Start || c.End > len(doc) {
		t.Fatalf("bad span %d..%d of %d", c.Start, c.End, len(doc))
	}
	if !bytes.Equal(out[:c.Start], doc[:c.Start]) {
		t.Fatalf("prefix changed")
	}
	if !bytes.Equal(out[len(out)-(len(doc)-c.End):], doc[c.End:]) {
		t.Fatalf("suffix changed")
	}
	if !json.Valid(out) {
		t.Fatalf("result does not parse:\n%s\n->\n%s", doc, out)
	}
	return out
}

func checkSetDelete(t testing.TB, doc []byte, ptr string, val []byte) {
	if !json.Valid(doc) || !json.Valid(val) {
		return
	}
	c, err := PlanSet(doc, ptr, val)
	if err == nil {
		out := checkChange(t, doc, c)
		if c.BeforePresent && !strings.Contains(ptr, "/-") {
			got, ok, gerr := Get(out, ptr)
			if gerr != nil || !ok || !sameValue(got, val) {
				t.Fatalf("Set then Get(%q): %q %v %v", ptr, got, ok, gerr)
			}
			_, ch2, err := Set(out, ptr, val)
			if err != nil || ch2.Changed {
				t.Fatalf("Set not idempotent: %v %+v", err, ch2)
			}
		}
	}
	d, err := PlanDelete(doc, ptr)
	if err == nil {
		out := checkChange(t, doc, d)
		if d.Changed {
			if !d.BeforePresent {
				t.Fatalf("deleted a missing key")
			}
			_ = out
		}
	}
}

func FuzzSplice(f *testing.F) {
	seeds := []struct{ doc, ptr, val string }{
		{"{\n  \"a\": 1,\n  \"b\": [1, 2, {\"c\": null}]\n}\n", "/b/2/c", `{"x": [true]}`},
		{`{"a":1}`, "/b", `"v"`},
		{"{}", "/env/K", `"v"`},
		{"{\n\t\"mcpServers\": {\n\t\t\"s\": {}\n\t},\n\t\"oauthAccount\": {\"e\": 1}\n}", "/mcpServers", `{"t": {"command": "x"}}`},
		{`{"a":{"b":{"c":[]}}}`, "/a/b/c/-", `1`},
		{`{"a~b":{"c/d":1},"a":2,"a":3}`, "/a~0b/c~1d", `2.50`},
		{`[1,2,3]`, "/1", `{}`},
		{" \r\n{ \"k\" : \"\\\"q\\\\\" , \"z\":-0.0e+1 } ", "/z", `[ ]`},
	}
	for _, s := range seeds {
		f.Add([]byte(s.doc), s.ptr, []byte(s.val))
	}
	f.Fuzz(func(t *testing.T, doc []byte, ptr string, val []byte) {
		checkSetDelete(t, doc, ptr, val)
	})
}

// randJSON builds a random document and returns a pointer into it (or to a new key).
func randValue(r *rand.Rand, depth int, indent string, pretty bool) string {
	nl, in, in2 := "", "", ""
	if pretty {
		nl, in, in2 = "\n", indent+"  ", indent
	}
	switch k := r.Intn(8); {
	case k == 0 || depth > 3:
		return []string{"1", "-2.50e3", "true", "null", `"s\"éé"`, "0"}[r.Intn(6)]
	case k < 4:
		var parts []string
		for i := r.Intn(4); i > 0; i-- {
			parts = append(parts, nl+in+randValue(r, depth+1, in, pretty))
		}
		if len(parts) == 0 {
			return "[]"
		}
		return "[" + strings.Join(parts, ",") + nl + in2 + "]"
	default:
		var parts []string
		for i := r.Intn(4); i > 0; i-- {
			parts = append(parts, fmt.Sprintf("%s%s\"k%d\": %s", nl, in, r.Intn(5), randValue(r, depth+1, in, pretty)))
		}
		if len(parts) == 0 {
			return "{}"
		}
		return "{" + strings.Join(parts, ",") + nl + in2 + "}"
	}
}

func randPointer(r *rand.Rand, doc []byte) string {
	var v any
	_ = json.Unmarshal(doc, &v)
	ptr := ""
	for i := 0; i < 5; i++ {
		switch x := v.(type) {
		case map[string]any:
			if len(x) == 0 || r.Intn(6) == 0 {
				return ptr + fmt.Sprintf("/k%d", r.Intn(7))
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			k := keys[r.Intn(len(keys))]
			ptr += "/" + k
			v = x[k]
		case []any:
			if len(x) == 0 || r.Intn(6) == 0 {
				return ptr + fmt.Sprintf("/%d", len(x)+r.Intn(2)-1)
			}
			n := r.Intn(len(x))
			ptr += fmt.Sprintf("/%d", n)
			v = x[n]
		default:
			return ptr
		}
	}
	return ptr
}

func TestRandomDocuments(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 4000; i++ {
		pretty := r.Intn(3) != 0
		doc := []byte(`{"root": ` + randValue(r, 0, "", pretty) + ", \"other\": " + randValue(r, 0, "", pretty) + "}")
		if pretty {
			doc = []byte(strings.Replace(string(doc), `{"root"`, "{\n  \"root\"", 1))
		}
		if !json.Valid(doc) {
			t.Fatalf("generator made invalid doc: %s", doc)
		}
		ptr := randPointer(r, doc)
		if ptr == "" {
			continue
		}
		checkSetDelete(t, doc, ptr, []byte(randValue(r, 2, "", pretty)))
	}
}

func FuzzGenerated(f *testing.F) {
	for _, s := range []int64{1, 2, 3, 42, 1000} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed int64) {
		r := rand.New(rand.NewSource(seed))
		pretty := r.Intn(2) == 0
		doc := []byte("{\"a\": " + randValue(r, 0, "", pretty) + "}")
		ptr := randPointer(r, doc)
		if ptr == "" {
			return
		}
		checkSetDelete(t, doc, ptr, []byte(randValue(r, 1, "", pretty)))
	})
}
