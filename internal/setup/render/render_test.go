package render_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
	"github.com/mraj602/watchyard/internal/setup/render"
)

func specBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../docs/spec/setup.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "wy setup — plan (dry run: nothing changed)")
	if i < 0 {
		t.Fatal("block not found in setup.md")
	}
	j := strings.Index(s[i:], "\n```")
	return s[i : i+j+1]
}

var idRE = regexp.MustCompile(`p_[0-9a-f]{12}`)

func normalise(s, root string) string {
	s = strings.ReplaceAll(s, root, "/home/u")
	s = idRE.ReplaceAllString(s, "p_…")
	lines := strings.Split(s, "\n")
	// The header pads the id to the right; the spec's exact width is not part of the contract.
	lines[0] = regexp.MustCompile(` +`).ReplaceAllString(lines[0], " ")
	return strings.Join(lines, "\n")
}

func laptopInput(t *testing.T) (render.Input, *detecttest.Home) {
	h := detecttest.Laptop(t)
	st := h.State()
	p, err := plan.PlanWith(st, model.Choices{}, plan.Options{Version: "0.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	return render.Input{Mode: model.ModeDryRun, Primary: "~/.claude", State: &st, Plan: p}, h
}

// The text of the laptop plan equals the block of setup.md 11.
func TestLaptopGoldenText(t *testing.T) {
	in, h := laptopInput(t)
	got := normalise(render.Text(in), h.Root)
	want := normalise(specBlock(t), h.Root)
	if got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Errorf("line %d\n got: %q\nwant: %q", i+1, g, w)
			}
		}
		t.Logf("full output:\n%s", got)
	}
}

func TestJSONShape(t *testing.T) {
	in, _ := laptopInput(t)
	b, err := render.JSON(in)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["v"] != float64(1) || m["mode"] != "dry-run" || m["primary"] != "~/.claude" || m["plan_id"] == "" {
		t.Errorf("envelope: %v", m)
	}
	if len(m["actions"].([]any)) != 5 || m["summary"].(map[string]any)["links_changed"] != float64(0) {
		t.Errorf("body: %v", m["summary"])
	}
	for _, k := range []string{"state", "keep", "conflicts", "needs"} {
		if m[k] == nil {
			t.Errorf("missing %s", k)
		}
	}
	// An empty plan still has arrays, not nulls.
	b, _ = render.JSON(render.Input{Mode: model.ModeDryRun})
	var e map[string]any
	_ = json.Unmarshal(b, &e)
	if e["actions"] == nil || e["keep"] == nil || e["conflicts"] == nil || e["needs"] == nil {
		t.Errorf("nulls in empty report: %s", b)
	}
}

// Secret-looking env and MCP values appear in neither output.
func TestRedaction(t *testing.T) {
	secrets := []string{"hunter2-env-secret", "sk-live-MCP-secret"}
	p := model.Plan{PlanID: "p_000000000000", Actions: []model.Action{
		{ID: "a1", Step: model.StepSharing, Op: model.OpJSONSet, File: "/h/.claude/settings.json", Pointer: "/env/API_TOKEN",
			Before: json.RawMessage(`"` + secrets[0] + `-old"`), Value: json.RawMessage(`"` + secrets[0] + `"`)},
		{ID: "a2", Step: model.StepSharing, Op: model.OpJSONSet, File: "/h/.claude/settings.json", Pointer: "/env",
			Value: json.RawMessage(`{"K":"` + secrets[0] + `"}`)},
		{ID: "a3", Step: model.StepMCP, Op: model.OpJSONSet, File: "/h/.claude.json", Pointer: "/mcpServers/x",
			Value: json.RawMessage(`{"command":"x","env":{"KEY":"` + secrets[1] + `"}}`)},
		{ID: "a4", Step: model.StepMCP, Op: model.OpJSONDelete, File: "/h/.claude.json", Pointer: "/mcpServers/y",
			Before: json.RawMessage(`{"env":{"KEY":"` + secrets[1] + `"}}`)},
	}}
	in := render.Input{Mode: model.ModeDryRun, Primary: "~/.claude", Plan: p, State: &model.State{Home: "/h"}}
	txt := render.Text(in)
	js, err := render.JSON(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if strings.Contains(txt, s) || strings.Contains(string(js), s) {
			t.Errorf("secret %q leaked\n%s\n%s", s, txt, js)
		}
	}
	if !strings.Contains(string(js), render.Redacted) || !strings.Contains(txt, "env.API_TOKEN") {
		t.Errorf("expected pointers and redaction markers:\n%s\n%s", txt, js)
	}
	// The input plan is not modified.
	if !strings.Contains(string(p.Actions[0].Value), secrets[0]) {
		t.Error("Redact modified its argument")
	}
}

func TestTextOtherPlans(t *testing.T) {
	h := detecttest.Fresh(t)
	st := h.State()
	p, err := plan.PlanWith(st, model.Choices{Add: []model.LabelColour{{Label: "work"}}, Hub: model.HubChoice{Mode: model.HubHere}}, plan.Options{Version: "0.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	txt := render.Text(render.Input{Mode: model.ModeDryRun, Primary: "~/.claude", State: &st, Plan: p})
	for _, want := range []string{"+ ~/.claude-2", "link settings.json", "env", "+ connect http://127.0.0.1:8787", "log in ~/.claude-2"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in:\n%s", want, txt)
		}
	}
	// Rerun-style empty plan.
	txt = render.Text(render.Input{Mode: model.ModeDryRun, State: &st})
	if !strings.Contains(txt, "0 changes · 0 files backed up · 0 links changed · 0 logins touched") {
		t.Errorf("count line:\n%s", txt)
	}
}
