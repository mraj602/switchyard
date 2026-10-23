// Package detecttest builds fixture homes for the setup tests (fresh machine, the laptop of
// setup.md 11) in a temporary folder and the detect.Deps that read them with a fake claude.
// It is only for tests.
package detecttest

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/claudecli"
	"github.com/mraj602/switchyard/internal/setup/detect"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

// jsonEsc escapes s for the inside of a JSON string literal (Windows paths carry backslashes).
func jsonEsc(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// Home is a fixture home folder.
type Home struct {
	T    testing.TB
	Root string
	B    *testtree.Builder
	Fake *claudecli.Fake
}

// New returns an empty home with a fake claude binary in ~/.local/bin and a fake Runner.
func New(t testing.TB) *Home {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &Home{T: t, Root: root, B: testtree.New(t, root), Fake: claudecli.NewFake()}
	h.B.File(".local/bin/claude", "#!/bin/sh\n", 0o755)
	return h
}

// Abs returns the absolute path of rel under the home.
func (h *Home) Abs(rel string) string { return h.B.Abs(rel) }

// Deps returns detect.Deps for the home: PATH is only ~/.local/bin, the shell probe finds
// nothing, Tailscale is up, and the hub probe answers "not healthy".
func (h *Home) Deps() detect.Deps {
	return detect.Deps{
		Home: h.Root,
		GOOS: "linux",
		Getenv: func(k string) string {
			if k == "PATH" {
				return h.Abs(".local/bin")
			}
			return ""
		},
		Claude:     h.Fake,
		ConfigDir:  h.Abs(".config/switchyard"),
		StateDir:   h.Abs(".local/state/switchyard"),
		Sy:         h.Abs(".local/bin/sy"),
		LookPath:   func(string) (string, error) { return h.Abs(".local/bin/claude"), nil },
		ShellProbe: func(context.Context, []string) (string, error) { return "", nil },
		HubProbe:   func(context.Context, string) model.HubState { return model.HubState{} },
		Tailscale: func(context.Context) *model.Tailscale {
			return &model.Tailscale{Up: true, DNSName: "laptop.example.ts.net"}
		},
	}
}

// State runs detect on the home.
func (h *Home) State() model.State {
	h.T.Helper()
	st, err := detect.Detect(context.Background(), h.Deps())
	if err != nil {
		h.T.Fatal(err)
	}
	return st
}

// Fresh is a machine with one logged-in ~/.claude holding only its own settings and login.
func Fresh(t testing.TB) *Home {
	h := New(t)
	h.B.File(".claude/settings.json", "{\n  \"theme\": \"dark\"\n}\n", 0o644)
	h.B.File(".claude/CLAUDE.md", "# me\n", 0o644)
	h.B.File(".claude/.credentials.json", `{"secret":"do-not-read"}`, 0o600)
	h.B.File(".claude.json", `{"oauthAccount":{"emailAddress":"you@example.com"},"mcpServers":{}}`, 0o600)
	h.Fake.SetLogin(h.Abs(".claude"), "you@example.com", "Me", "max")
	return h
}

// SharedItems are the eight items each linked folder of the laptop holds.
var SharedItems = []string{"settings.json", "CLAUDE.md", "commands", "skills", "plugins", "projects", "history.jsonl", "plans"}

// Laptop reproduces setup.md 11: seven folders, six of them with eight links to ~/.claude,
// a status line, three hook entries of another tool, three plugins off, shell-function launchers calling
// ~/bin/claude-launch, a hand-made MCP sync script, `.claude-api-env` (a file) and leftover
// `.claude.json.tmp.*` files.
func Laptop(t testing.TB) *Home {
	h := New(t)
	b := h.B
	hook := h.Abs(".claude/notify-hook.sh")
	b.File(".claude/notify-hook.sh", "#!/bin/sh\n", 0o755)
	b.File(".claude/statusline-command.sh", "#!/bin/sh\necho line\n", 0o755)
	settings := fmt.Sprintf(`{
  "statusLine": {
    "type": "command",
    "command": "bash %[1]s"
  },
  "enabledPlugins": {
    "alpha@market": false,
    "beta@market": false,
    "gamma@market": false
  },
  "extraKnownMarketplaces": {
    "market": {"source": {"source": "github", "repo": "someone/market"}}
  },
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "bash %[2]s # notify-hook"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "bash %[2]s # notify-hook"}]}],
    "PostToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "bash %[2]s # notify-hook"}]}]
  }
}
`, jsonEsc(h.Abs(".claude/statusline-command.sh")), jsonEsc(hook))
	b.File(".claude/settings.json", settings, 0o644)
	b.File(".claude/CLAUDE.md", "# instructions\n", 0o644)
	b.File(".claude/commands/hello.md", "hello\n", 0o644)
	b.File(".claude/skills/one/SKILL.md", "one\n", 0o644)
	b.File(".claude/projects/-home-u-p/s1.jsonl", "{}\n", 0o600)
	b.File(".claude/history.jsonl", "{\"timestamp\":1}\n", 0o600)
	b.File(".claude/plans/plan.md", "plan\n", 0o644)
	b.File(".claude/plugins/installed_plugins.json", fmt.Sprintf(`{"version":2,"plugins":{"alpha@market":[{"installPath":"%s"}],"beta@market":[{}],"gamma@market":[{}]}}`, jsonEsc(h.Abs(".claude-2/plugins/cache/market/alpha/1"))), 0o644)
	b.File(".claude/plugins/known_marketplaces.json", `{"market":{"source":{"source":"github","repo":"someone/market"}}}`, 0o644)
	b.File(".claude/sync-mcp-to-2.sh", "#!/bin/sh\njq '.mcpServers' \"$HOME/.claude.json\"\n", 0o755)
	b.File(".claude/.credentials.json", `{"secret":"do-not-read"}`, 0o600)
	b.File(".claude/.claude.json", `{"mcpServers":{}}`, 0o600)
	b.File(".claude.json", `{"oauthAccount":{"emailAddress":"you@example.com"},"mcpServers":{}}`, 0o600)
	b.File(".claude-api-env", "ANTHROPIC_API_KEY=sk-secret\n", 0o600)
	b.File("bin/claude-launch", "#!/bin/bash\nN=$1; shift\n[ \"$N\" -gt 1 ] && ~/.claude/sync-mcp-to-2.sh\nexec claude \"$@\"\n", 0o755)
	var rc strings.Builder
	rc.WriteString("export PATH=$HOME/bin:$PATH\nclaude() { ~/bin/claude-launch 1 \"$@\"; }\n")
	for n := 2; n <= 7; n++ {
		fmt.Fprintf(&rc, "claude-%d() { ~/bin/claude-launch %d \"$@\"; }\n", n, n)
	}
	b.File(".bashrc", rc.String(), 0o644)
	for n := 2; n <= 7; n++ {
		d := fmt.Sprintf(".claude-%d", n)
		b.Dir(d, 0o700)
		for _, it := range SharedItems {
			b.Symlink(d+"/"+it, "$ROOT/.claude/"+it)
		}
		b.File(d+"/.claude.json", fmt.Sprintf(`{"oauthAccount":{"emailAddress":"c%d@example.com"},"mcpServers":{}}`, n), 0o600)
		if n <= 3 {
			b.File(d+"/.credentials.json", `{"secret":"do-not-read"}`, 0o600)
		}
	}
	b.File(".claude-5/.claude.json.tmp.123.456", "{}", 0o600)
	h.Fake.SetLogin(h.Abs(".claude"), "you@example.com", "Me", "max")
	h.Fake.SetLogin(h.Abs(".claude-2"), "you@work.example", "Work Inc", "max")
	h.Fake.SetLogin(h.Abs(".claude-3"), "you@team.example", "Team Org", "team")
	return h
}
