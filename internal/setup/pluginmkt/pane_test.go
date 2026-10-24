package pluginmkt

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/plugin"
)

func TestPaneFileRewritesOnlyRegister(t *testing.T) {
	src := []byte("// c\nexport const WY = \"__WY_PATH__\"\nuse(WY)\n")
	got, err := paneFile("hooks/register.tsx", src, "/home/u/.local/bin/wy")
	if err != nil {
		t.Fatal(err)
	}
	want := "// c\nexport const WY = \"/home/u/.local/bin/wy\"\nuse(WY)\n"
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Any other file is passed through untouched, placeholder or not.
	for _, p := range []string{"hooks/model.ts", "hooks/register.test.ts", "types/index.d.ts", "hooks/hooks.json"} {
		out, err := paneFile(p, src, "/bad\"path")
		if err != nil || !bytes.Equal(out, src) {
			t.Errorf("%s changed or failed: %v", p, err)
		}
	}
}

func TestPaneFileRefusesUnsafePaths(t *testing.T) {
	src := []byte(`export const WY = "__WY_PATH__"`)
	for _, p := range []string{"", `/a"b/wy`, "/a\nb/wy", "/a\rb/wy", `/a\b/wy`, `/a"; evil(); "/wy`} {
		if runtime.GOOS == "windows" && p == `/a\b/wy` {
			continue // on Windows a backslash is a separator and is written as "/"
		}
		if _, err := paneFile("hooks/register.tsx", src, p); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	if _, err := paneFile("hooks/register.tsx", []byte("no placeholder"), "/x/wy"); err == nil {
		t.Error("a register.tsx without the placeholder was accepted")
	}
}

// The written mod has the absolute wy path and nothing else changed in it.
func TestWrittenPaneHasAbsoluteWyPath(t *testing.T) {
	state := t.TempDir()
	wy := filepath.Join(state, "bin", "wy")
	dir := filepath.Join(state, "marketplace")
	if err := New(claudecli.NewFake()).WriteMarketplace(dir, wy, "0.7.0"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(PaneDir(dir), "hooks", "register.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := fs.ReadFile(plugin.PaneFS(), "hooks/register.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(embedded, []byte(panePlaceholder)); n != 1 {
		t.Fatalf("the embedded register.tsx has %d placeholders, want exactly 1", n)
	}
	lit := filepath.ToSlash(wy)
	if runtime.GOOS != "windows" {
		lit = wy
	}
	want := bytes.Replace(embedded, []byte(panePlaceholder), []byte(`"`+lit+`"`), 1)
	if !bytes.Equal(got, want) {
		t.Fatal("the written register.tsx differs from the embedded one by more than the wy path")
	}
	if !regexp.MustCompile(`export const WY = "[^"]+/bin/wy"`).Match(got) || bytes.Contains(got, []byte("__WY_PATH__")) {
		t.Fatalf("WY not set to the absolute path:\n%s", grepLine(got, "export const WY"))
	}
	// A path the rewrite refuses stops the whole write, not just the file.
	bad := filepath.Join(state, `has"quote`, "wy")
	if runtime.GOOS != "windows" {
		if err := New(claudecli.NewFake()).WriteMarketplace(filepath.Join(state, "m2"), bad, "0.7.0"); err == nil {
			t.Error("WriteMarketplace accepted a wy path with a quote")
		}
	}
}

func grepLine(b []byte, sub string) string {
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// The recorded stream the mod's tests read (testdata/view-stream.ndjson) is the same text as
// hooks/fixtures.ts (tests cannot read files), its first line is the golden View, and the
// lines the mod must drop do not decode while the rest do.
func TestPaneViewStreamFixture(t *testing.T) {
	paneFS := plugin.PaneFS()
	nd, err := fs.ReadFile(paneFS, "testdata/view-stream.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(nd), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("view-stream.ndjson has %d lines, want 5", len(lines))
	}

	ts, err := fs.ReadFile(paneFS, "hooks/fixtures.ts")
	if err != nil {
		t.Fatal(err)
	}
	i, j := bytes.Index(ts, []byte("= [")), bytes.LastIndex(ts, []byte("]"))
	var fromTS []string
	if i < 0 || j < i || json.Unmarshal(ts[i+2:j+1], &fromTS) != nil {
		t.Fatal("hooks/fixtures.ts: cannot read the STREAM array")
	}
	if strings.Join(fromTS, "\n") != strings.Join(lines, "\n") {
		t.Error("hooks/fixtures.ts STREAM differs from testdata/view-stream.ndjson")
	}

	golden, err := os.ReadFile(filepath.Join("..", "..", "board", "testdata", "view-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if json.Unmarshal(golden, &a) != nil || json.Unmarshal([]byte(lines[0]), &b) != nil {
		t.Fatal("bad JSON")
	}
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if !bytes.Equal(ga, gb) {
		t.Error("line 1 is not the golden View")
	}

	wantOK := []bool{true, false, true, false, true}
	for n, l := range lines {
		_, err := board.Decode([]byte(l))
		if (err == nil) != wantOK[n] {
			t.Errorf("line %d: Decode error = %v, want ok = %v", n+1, err, wantOK[n])
		}
	}
}
