package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestMissingFileGivesDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "switchyard")
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("Load must not create the folder")
	}
	c, err := f.Typed()
	if err != nil || !reflect.DeepEqual(c, Defaults()) {
		t.Fatalf("typed = %+v, %v", c, err)
	}
	for key, want := range map[string]any{"outbox.max_mb": int64(50), "privacy.last_message": true, "hub.url": ""} {
		if got, err := f.Get(key); err != nil || got != want {
			t.Errorf("Get(%s) = %v, %v; want %v", key, got, err, want)
		}
	}
	if _, err := f.Get("no.such"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestSetSaveReloadKeepsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	orig := `version = 1
future_top = "keep me"
iguana = 3

[hub]
url = "https://hub.example"
future_nested = [1, 2, 3]

[experimental.deep]
flag = true
`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Set("hub.insecure_lan", []string{"true"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("slots.labels.~/.claude-2", []string{"work"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("slots.extra", []string{"~/a", "~/b"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	g, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"future_top":               "keep me",
		"iguana":                   int64(3),
		"hub.url":                  "https://hub.example",
		"hub.insecure_lan":         true,
		"slots.labels.~/.claude-2": "work",
		"experimental.deep.flag":   true,
		"hub.future_nested":        []any{int64(1), int64(2), int64(3)},
		"slots.extra":              []any{"~/a", "~/b"},
	} {
		got, err := g.Get(key)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Get(%s) = %#v, %v; want %#v", key, got, err, want)
		}
	}
	if u := g.Unknown(); !reflect.DeepEqual(u, []string{"experimental", "future_top", "hub.future_nested", "iguana"}) {
		t.Errorf("Unknown = %v", u)
	}
	c, err := g.Typed()
	if err != nil || c.Hub.URL != "https://hub.example" || !c.Hub.InsecureLAN || c.Slots.Labels["~/.claude-2"] != "work" {
		t.Errorf("typed %+v %v", c, err)
	}
	st, _ := os.Stat(filepath.Join(dir, FileName))
	if runtimeHasModes() && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
}

func runtimeHasModes() bool { return filepath.Separator == '/' }

func TestSetValidation(t *testing.T) {
	f, _ := Load(t.TempDir())
	bad := []struct {
		key  string
		args []string
	}{
		{"nope", []string{"x"}},
		{"version", []string{"2"}},
		{"hub.insecure_lan", []string{"maybe"}},
		{"outbox.max_mb", []string{"-1"}},
		{"outbox.max_mb", []string{"5x"}},
		{"outbox.max_mb", []string{"1", "2"}},
		{"hub.url", []string{"ftp://x"}},
		{"slots.labels.", []string{"x"}},
	}
	for _, b := range bad {
		if err := f.Set(b.key, b.args); err == nil {
			t.Errorf("Set(%s, %v) should fail", b.key, b.args)
		}
	}
	if err := f.Set("slots.extra", []string{`["~/x", "~/y"]`}); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get("slots.extra"); !reflect.DeepEqual(v, []any{"~/x", "~/y"}) {
		t.Errorf("list = %#v", v)
	}
}

func TestTypedReportsWrongType(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("[outbox]\nmax_mb = \"lots\"\n"), 0o600)
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Typed(); err == nil {
		t.Fatal("want type error")
	}
}

func TestBrokenTOML(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("this is = = not toml"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Fatal("want parse error")
	}
}

// The config folder is shared with other tools: Save may add config.toml and
// nothing else, and must leave every other entry (and no temp file) behind.
func TestSaveOnlyTouchesConfigToml(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".config", "switchyard")
	if err := os.MkdirAll(filepath.Join(dir, "Partitions", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	proto := map[string]string{"config.json": `{"token":"proto"}`, "accounts.json": "[]", "usage.db": "db"}
	for n, c := range proto {
		os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600)
	}
	before := names(t, dir)

	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.Set("hub.url", []string{"https://h.example"})
	for i := 0; i < 3; i++ {
		if err := f.Save(); err != nil {
			t.Fatal(err)
		}
	}
	after := names(t, dir)
	want := append(append([]string{}, before...), FileName)
	sort.Strings(want)
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("entries after save\n got %v\nwant %v", after, want)
	}
	for n, c := range proto {
		got, _ := os.ReadFile(filepath.Join(dir, n))
		if string(got) != c {
			t.Errorf("%s was modified", n)
		}
	}
	// Save creates a missing folder with mode 0700 and still only config.toml.
	fresh := filepath.Join(t.TempDir(), "new")
	g, _ := Load(fresh)
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	if got := names(t, fresh); !reflect.DeepEqual(got, []string{FileName}) {
		t.Fatalf("fresh folder holds %v", got)
	}
	if st, _ := os.Stat(fresh); runtimeHasModes() && st.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", st.Mode().Perm())
	}
}

func TestSaveWritesVersionAndHeader(t *testing.T) {
	dir := t.TempDir()
	f, _ := Load(dir)
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), "version = 1") || !strings.HasPrefix(string(b), "# Switchyard") {
		t.Errorf("unexpected file:\n%s", b)
	}
}
