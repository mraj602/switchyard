package scan

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// slotWith builds <home>/.claude/projects/p/<files...> and returns the slot and home.
func slotWith(t *testing.T, files map[string]string) (Slot, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(dir, "projects", "p", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Slot{Folder: "~/.claude", Dir: dir}, home
}

type parityOut struct {
	Rows    []map[string]any `json:"rows"`
	Hits    []map[string]any `json:"hits"`
	Owners  []map[string]any `json:"owners"`
	Pricing []struct {
		Model string   `json:"model"`
		Cost  *float64 `json:"cost"`
		Label string   `json:"label"`
	} `json:"pricing"`
}

// toMap turns a Go value into the JSON object form node prints.
func toMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(v)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParityWithPrototype(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	var names []string
	srcs := map[string]string{}
	add := func(path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		srcs[filepath.Base(path)] = string(b)
		names = append(names, filepath.Base(path))
	}
	m, _ := filepath.Glob("../../testdata/transcripts/*.jsonl")
	for _, p := range m {
		add(p)
	}
	add("testdata/edge.jsonl")
	sort.Strings(names)

	slot, _ := slotWith(t, srcs)
	dir := filepath.Join(slot.Dir, "projects", "p")
	args := []string{"testdata/parity.js"}
	for _, n := range names {
		args = append(args, filepath.Join(dir, n))
	}
	cmd := exec.Command("node", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("prototype parser not runnable here: %v %s", err, stderr.String())
	}
	var want parityOut
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}

	sc, err := New(Options{Slots: []Slot{slot}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := sc.Scan()
	if err != nil {
		t.Fatal(err)
	}
	var rows, hits, owners []map[string]any
	for _, b := range res.Batches {
		for _, r := range b.Rows {
			rows = append(rows, toMap(t, struct {
				TokenRow
				SessionID string `json:"session_id"`
				CWD       string `json:"cwd"`
				Model     string `json:"model"`
			}{r, r.SessionID, r.CWD, r.Model}))
		}
		for _, h := range b.LimitHits {
			hits = append(hits, toMap(t, struct {
				LimitHit
				Status   string `json:"status"`
				ResetsAt int64  `json:"resets_at"`
			}{h, h.Status, h.ResetsAt}))
		}
		for _, s := range b.Sessions {
			if s.OwnerAccount != "" || s.OwnerOrg != "" {
				owners = append(owners, map[string]any{"session_id": s.SessionID, "owner_account": s.OwnerAccount, "owner_org": s.OwnerOrg})
			}
		}
	}
	if len(want.Rows) < 6 || len(want.Hits) == 0 || len(want.Owners) == 0 {
		t.Fatalf("fixture too thin: %d rows %d hits %d owners", len(want.Rows), len(want.Hits), len(want.Owners))
	}
	if !reflect.DeepEqual(rows, want.Rows) {
		t.Errorf("rows differ\n go:   %v\n node: %v", rows, want.Rows)
	}
	if !reflect.DeepEqual(hits, want.Hits) {
		t.Errorf("limit hits differ\n go:   %v\n node: %v", hits, want.Hits)
	}
	if !reflect.DeepEqual(owners, want.Owners) {
		t.Errorf("owners differ\n go:   %v\n node: %v", owners, want.Owners)
	}
	tk := Tokens{Input: 1234567, Output: 76543, Cache5m: 345678, Cache1h: 23456, CacheRead: 9876543}
	for _, p := range want.Pricing {
		cost, ok := CostOf(p.Model, tk)
		if ok != (p.Cost != nil) || (ok && math.Abs(cost-*p.Cost) > 1e-9*math.Max(1, *p.Cost)) {
			t.Errorf("cost %q: go %v/%v node %v", p.Model, cost, ok, p.Cost)
		}
		if got := ModelLabel(p.Model); got != p.Label {
			t.Errorf("label %q: go %q node %q", p.Model, got, p.Label)
		}
	}
}

func line(id string, out int) string {
	return `{"type":"assistant","timestamp":"2026-10-12T10:00:00.000Z","sessionId":"s1","message":{"id":"` + id +
		`","model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":` + strconv.Itoa(out) + `}}}` + "\n"
}

func keys(res *Result) []string {
	var k []string
	for _, b := range res.Batches {
		for _, r := range b.Rows {
			k = append(k, r.Key)
		}
	}
	return k
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestIncremental(t *testing.T) {
	slot, home := slotWith(t, map[string]string{"a.jsonl": line("m1", 5) + line("m2", 6)})
	file := filepath.Join(slot.Dir, "projects", "p", "a.jsonl")
	offs := filepath.Join(home, "state", "scan", "offsets.json")
	scan := func() *Result {
		sc, err := New(Options{Slots: []Slot{slot}, OffsetsPath: offs, Home: home})
		if err != nil {
			t.Fatal(err)
		}
		res, err := sc.Scan()
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Commit(); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if k := keys(scan()); !reflect.DeepEqual(k, []string{"m1", "m2"}) {
		t.Fatalf("first scan: %v", k)
	}
	if res := scan(); len(res.Batches) != 0 || res.Files != 0 {
		t.Fatalf("unchanged file rescanned: %+v", res)
	}
	// Append a new row and a partial line (not consumed yet), then complete it.
	part := line("m4", 9)
	appendTo(t, file, line("m3", 7)+part[:20])
	res := scan()
	if k := keys(res); !reflect.DeepEqual(k, []string{"m3"}) {
		t.Fatalf("append: %v", k)
	}
	if s := res.Batches[0].Source; s == nil || !strings.HasPrefix(s.Path, "~/.claude/projects/") {
		t.Fatalf("source: %+v", s)
	}
	appendTo(t, file, part[20:])
	if k := keys(scan()); !reflect.DeepEqual(k, []string{"m4"}) {
		t.Fatalf("completed partial: %v", k)
	}
	// Truncated (rotated to something shorter): rescan from 0.
	if err := os.WriteFile(file, []byte(line("n1", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if k := keys(scan()); !reflect.DeepEqual(k, []string{"n1"}) {
		t.Fatalf("truncate: %v", k)
	}
	// Rotated to a longer, different file: the head fingerprint changes, rescan from 0.
	if err := os.WriteFile(file, []byte(line("r1", 1)+line("r2", 2)+line("r3", 3)+line("r4", 4)), 0o644); err != nil {
		t.Fatal(err)
	}
	if k := keys(scan()); !reflect.DeepEqual(k, []string{"r1", "r2", "r3", "r4"}) {
		t.Fatalf("rotate longer: %v", k)
	}
	// Without Commit, the same bytes come back (at-least-once).
	appendTo(t, file, line("r5", 1))
	sc, _ := New(Options{Slots: []Slot{slot}, OffsetsPath: offs})
	r1, _ := sc.Scan()
	r2, _ := sc.Scan()
	if !reflect.DeepEqual(keys(r1), []string{"r5"}) || !reflect.DeepEqual(keys(r2), []string{"r5"}) {
		t.Fatalf("uncommitted: %v %v", keys(r1), keys(r2))
	}
}

func TestBatchingAndSharedProjects(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 4500; i++ {
		sb.WriteString(line("m"+strconv.Itoa(i), i))
	}
	slot, home := slotWith(t, map[string]string{"big.jsonl": sb.String()})
	// A second slot whose projects folder is a symlink to the first: shared, so no slot.
	dir2 := filepath.Join(home, ".claude-2")
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(slot.Dir, "projects"), filepath.Join(dir2, "projects")); err != nil {
		t.Fatal(err)
	}
	sc, _ := New(Options{Slots: DiscoverSlots(home)})
	res, err := sc.Scan()
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, b := range res.Batches {
		sizes = append(sizes, len(b.Rows))
		if b.Slot != "" {
			t.Errorf("shared projects folder got slot %q", b.Slot)
		}
	}
	if !reflect.DeepEqual(sizes, []int{2000, 2000, 500}) {
		t.Fatalf("batch sizes %v", sizes)
	}
	// Single slot: slot is set.
	sc, _ = New(Options{Slots: []Slot{slot}})
	res, _ = sc.Scan()
	if res.Batches[0].Slot != "~/.claude" {
		t.Fatalf("slot %q", res.Batches[0].Slot)
	}
}

func TestSessionsAndRemoteURL(t *testing.T) {
	body := `{"type":"bridge-session","sessionId":"s1","ownerAccountUuid":"acc","ownerOrganizationUuid":"org"}` + "\n" +
		`{"type":"attachment","sessionId":"s1","attachment":{"type":"remote_session_change","url":"https://claude.ai/code/session_AAA"}}` + "\n" +
		`{"type":"attachment","sessionId":"s1","attachment":{"type":"remote_session_change","url":"https://claude.ai/code/session_BBB"}}` + "\n"
	slot, _ := slotWith(t, map[string]string{"s1.jsonl": body})
	sc, _ := New(Options{Slots: []Slot{slot}})
	res, _ := sc.Scan()
	if len(res.Batches) != 1 || len(res.Batches[0].Sessions) != 1 {
		t.Fatalf("%+v", res.Batches)
	}
	s := res.Batches[0].Sessions[0]
	if s.OwnerAccount != "acc" || s.OwnerOrg != "org" || s.RemoteURL != "https://claude.ai/code/session_BBB" {
		t.Fatalf("%+v", s)
	}
}

func TestHistory(t *testing.T) {
	slot, _ := slotWith(t, nil)
	raw := `{"version":5,"lastComputedDate":"2026-09-14","dailyActivity":[{"date":"2026-05-21","messageCount":1}],"dailyModelTokens":[{"date":"2026-05-21","tokensByModel":{"claude-opus-5":10}}],"totalSessions":3}`
	if err := os.WriteFile(filepath.Join(slot.Dir, "stats-cache.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	h := ReadHistory([]Slot{slot, {Folder: "~/.claude-9", Dir: filepath.Join(t.TempDir(), "none")}})
	if len(h) != 1 || h[0].Slot != "~/.claude" || h[0].Source != "stats_cache" || string(h[0].Raw) != raw || h[0].Parts != 0 {
		t.Fatalf("%+v", h)
	}
	// Large files are split, and the parts together carry every day.
	var days []string
	for i := 0; i < 60000; i++ {
		days = append(days, `{"date":"d`+strconv.Itoa(i)+`","messageCount":1,"sessionCount":1,"toolCallCount":1}`)
	}
	big := `{"version":5,"lastComputedDate":"x","dailyActivity":[` + strings.Join(days, ",") + `],"dailyModelTokens":[],"totalSessions":3}`
	parts := splitHistory("~/.claude", []byte(big))
	if len(parts) < 2 || parts[0].Part != 1 || parts[0].Parts != len(parts) {
		t.Fatalf("parts: %d", len(parts))
	}
	total := 0
	for _, p := range parts {
		var m struct {
			DailyActivity []json.RawMessage `json:"dailyActivity"`
		}
		if err := json.Unmarshal(p.Raw, &m); err != nil {
			t.Fatal(err)
		}
		total += len(m.DailyActivity)
	}
	if total != 60000 {
		t.Fatalf("days across parts: %d", total)
	}
}

func TestParseLineMisc(t *testing.T) {
	if _, ok := ParseLine(""); ok {
		t.Fatal("empty line parsed")
	}
	r := TokenRow{TS: 1791821866495}
	if got := r.RFC3339(); got != "2026-10-12T16:17:46.495Z" {
		t.Fatalf("rfc3339 %q", got)
	}
}
