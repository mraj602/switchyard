package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/view-v1.json")

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr[T any](v T) *T { return &v }

// goldenView is the T2 example made concrete: three seats' worth of rows on two machines, a
// plain-text question on another machine, a working row here and a rate-limited row.
func goldenView() View {
	const (
		laptop = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"
		gpu    = "m_01JA2W9R7TQ4M3D1C8B5E6F2HV"
		asked  = "c41e2a90-5b7d-4c1e-9f0a-2d3b4c5e6f70"
		work   = "9a0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
		limit  = "5f6e7d8c-9b0a-4b1c-8d2e-3f4a5b6c7d8e"
	)
	return View{
		V:           1,
		GeneratedAt: ts("2026-10-20T14:06:00Z"),
		Source:      SourceHub,
		Hub:         HubInfo{State: HubLive, URL: "https://hub.tailnet-name.ts.net", OfflineSince: nil},
		Machine:     Machine{ID: laptop, Name: "laptop"},
		Counts:      Counts{Sessions: 3, Machines: 2, NeedsYou: 1},
		NeedsYou:    []string{asked},
		Groups: []Group{
			{Seat: "s_01JA2WB3K5M7N9P1Q3R5S7T9V1", Label: "work", Colour: "#f59e0b", WarpColour: "yellow",
				FiveHour: &Window{Pct: 41, ResetsAt: ptr(ts("2026-10-20T15:10:00Z")), Text: "41% · 15:10"},
				SevenDay: &Window{Pct: 30, Text: "30%"}, Limited: false, Rows: []string{asked, work}},
			{Seat: "s_01JA2WB3K5M7N9P1Q3R5S7T9V2", Label: "personal", Colour: "#3b82f6", WarpColour: "blue",
				FiveHour: &Window{Pct: 100, ResetsAt: ptr(ts("2026-10-20T17:00:00Z")), Text: "100% · 17:00"},
				SevenDay: &Window{Pct: 64, Text: "64%"}, Limited: true, Rows: []string{limit}},
		},
		Rows: map[string]Row{
			asked: {ID: asked, State: "asked", Detail: "question", NeedsYou: true, Glyph: GlyphNeeds, Status: "asked",
				Project: "api", Title: "migrate-orders", Account: "work", Colour: "#f59e0b", Seat: "s_01JA2WB3K5M7N9P1Q3R5S7T9V1",
				Machine: gpu, MachineName: "gpu-box", Here: false, ThisSession: false,
				ContextPct: ptr(47), ContextLevel: ContextOK, Since: ts("2026-10-20T14:02:00Z"), Age: "4m",
				Line2: "Update the migration, or keep the old column?", Line2Kind: Line2Question,
				CanFocus: false, Resume: "claude-2 --resume " + asked, RemoteURL: "https://claude.ai/code/session_01AbCdEf"},
			work: {ID: work, State: "working", Detail: "tool", Glyph: GlyphWorking, Status: "40s",
				Project: "switchyard", Title: "phase-7-board", Account: "work", Colour: "#f59e0b", Seat: "s_01JA2WB3K5M7N9P1Q3R5S7T9V1",
				Machine: laptop, MachineName: "laptop", Here: true, ThisSession: true,
				ContextPct: ptr(81), ContextLevel: ContextWarn, Since: ts("2026-10-20T14:05:20Z"), Age: "40s",
				Line2: "Writing internal/board/view.go and its golden.", Line2Kind: Line2Recap,
				CanFocus: true, Resume: "claude-2 --resume " + work},
			limit: {ID: limit, State: "failed", Detail: "rate_limit", Glyph: GlyphFailed, Status: "limit · 17:00",
				Project: "notes", Title: "weekly-review", Account: "personal", Colour: "#3b82f6", Seat: "s_01JA2WB3K5M7N9P1Q3R5S7T9V2",
				Machine: laptop, MachineName: "laptop", Here: true,
				ContextPct: nil, ContextLevel: ContextOK, Since: ts("2026-10-20T13:40:00Z"), Age: "26m",
				Line2: "Usage limit reached for this account.", Line2Kind: Line2Error,
				Stale: true, CanFocus: true, Resume: "claude --resume " + limit},
		},
	}
}

func marshalView(t *testing.T, v View) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestViewGoldenRoundTrip(t *testing.T) {
	path := filepath.Join("testdata", "view-v1.json")
	got := marshalView(t, goldenView())
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("View JSON differs from %s (run with -update after checking the change against T2):\n%s", path, got)
	}
	back, err := Decode(want)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, goldenView()) {
		t.Fatalf("round trip lost data:\n%+v", back)
	}
	if again := marshalView(t, back); !bytes.Equal(again, want) {
		t.Fatal("decode then encode is not byte-identical")
	}
}

// The JSON keys are exactly those of docs/spec/terminal.md T2 (plus "truncated", present
// only when rows were dropped).
func TestViewKeysAreT2(t *testing.T) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(marshalView(t, goldenView()), &doc); err != nil {
		t.Fatal(err)
	}
	keysOf := func(raw json.RawMessage) []string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	same := func(what string, got []string, want string) {
		t.Helper()
		w := strings.Fields(want)
		sort.Strings(w)
		if !reflect.DeepEqual(got, w) {
			t.Errorf("%s keys = %v, want %v", what, got, w)
		}
	}
	top := make([]string, 0, len(doc))
	for k := range doc {
		top = append(top, k)
	}
	sort.Strings(top)
	same("view", top, "v generated_at source hub machine counts needs_you groups rows")
	same("hub", keysOf(doc["hub"]), "state url offline_since")
	same("machine", keysOf(doc["machine"]), "id name")
	same("counts", keysOf(doc["counts"]), "sessions machines needs_you")
	var groups []json.RawMessage
	_ = json.Unmarshal(doc["groups"], &groups)
	same("group", keysOf(groups[0]), "seat label colour warp_colour five_hour seven_day limited rows")
	var g struct {
		FiveHour json.RawMessage `json:"five_hour"`
	}
	_ = json.Unmarshal(groups[0], &g)
	same("five_hour", keysOf(g.FiveHour), "pct resets_at text")
	var rows map[string]json.RawMessage
	_ = json.Unmarshal(doc["rows"], &rows)
	same("row", keysOf(rows["c41e2a90-5b7d-4c1e-9f0a-2d3b4c5e6f70"]),
		"id state detail needs_you glyph status project title account colour seat machine machine_name here "+
			"this_session context_pct context_level since age line2 line2_kind stale greyed can_focus resume remote_url")

	v := goldenView()
	v.Truncated = 4
	if !bytes.Contains(marshalView(t, v), []byte(`"truncated": 4`)) {
		t.Error("truncated is not written when rows were dropped")
	}
}

func TestDecodeRefusesOtherVersionsAndDanglingIds(t *testing.T) {
	for _, line := range []string{`{"v":2,"rows":{}}`, `{"rows":{}}`, `{"v":"1"}`} {
		if _, err := Decode([]byte(line)); err == nil {
			t.Errorf("Decode(%s) accepted", line)
		}
	}
	if _, err := Decode([]byte(`{"v":2}`)); !errors.Is(err, ErrVersion) {
		t.Errorf("v 2: %v, want ErrVersion", err)
	}
	if _, err := Decode([]byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
	for name, mutate := range map[string]func(*View){
		"needs_you": func(v *View) { v.NeedsYou = append(v.NeedsYou, "nope") },
		"group":     func(v *View) { v.Groups[0].Rows = append(v.Groups[0].Rows, "nope") },
		"key": func(v *View) {
			r := v.Rows["9a0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"]
			r.ID = "x"
			v.Rows["9a0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"] = r
		},
	} {
		v := goldenView()
		mutate(&v)
		if _, err := Decode(marshalView(t, v)); err == nil {
			t.Errorf("%s: dangling id accepted", name)
		}
	}
	// An unknown field from a newer writer of the same v is ignored.
	b := bytes.Replace(marshalView(t, goldenView()), []byte(`"v": 1,`), []byte(`"v": 1, "future": {"x": 1},`), 1)
	if _, err := Decode(b); err != nil {
		t.Errorf("unknown field refused: %v", err)
	}
}

// The golden fits the caps with room to spare, so fixtures built from it stay valid.
func TestGoldenIsWithinCaps(t *testing.T) {
	v := goldenView()
	if len(v.Rows) > MaxRows || len(marshalView(t, v)) > MaxBytes {
		t.Fatal("golden exceeds the T2 caps")
	}
}
