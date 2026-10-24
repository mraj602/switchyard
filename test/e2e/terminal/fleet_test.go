package terminal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/board"
)

// seats rewrites the Claude state files of a home so each slot has a real seat: slot 1 is
// the personal account, slot 2 "work", slot 3 "team" (the same three accounts on every
// machine, so the hub sees three seats and rows of both machines in each).
func seats(t *testing.T, home string) {
	t.Helper()
	acct := func(uuid, org, email string) string {
		return fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":%q,"emailAddress":%q},"mcpServers":{}}`, uuid, org, email)
	}
	writeFile(t, filepath.Join(home, ".claude.json"), acct("aaaaaaaa-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000001", "me@example.com"), 0o600)
	writeFile(t, filepath.Join(home, ".claude-2", ".claude.json"), acct("aaaaaaaa-0000-4000-8000-000000000002", "bbbbbbbb-0000-4000-8000-000000000002", "work@example.com"), 0o600)
	writeFile(t, filepath.Join(home, ".claude-3", ".claude.json"), acct("aaaaaaaa-0000-4000-8000-000000000003", "bbbbbbbb-0000-4000-8000-000000000003", "team@example.com"), 0o600)
}

// slotOf is the environment extra of Claude Code slot n in any home (1 = ~/.claude).
func (m *machine) slot(n int) []string {
	if n <= 1 {
		return nil
	}
	return []string{fmt.Sprintf("CLAUDE_CONFIG_DIR=%s/.claude-%d", m.home, n)}
}

// boardToken makes m a board reader of the hub by hand (the admin channel of
// `wy board connect` only exists on the hub's own host): a desktop device bound to m's
// machine id, written as <state>/board/token.json.
func (h *hubProc) boardToken(m *machine) {
	h.t.Helper()
	var tok struct{ Device, Token string }
	h.m.mustJSON(&tok, "hub", "token", "create", "--kind", "desktop", "--name", "terminal-"+m.name, "--machine", m.machineID(), "--json")
	if err := board.SaveToken(m.stateDir(), board.Token{URL: h.base, Device: tok.Device, Token: tok.Token}); err != nil {
		h.t.Fatal(err)
	}
}

// ownerGet reads an API path as the owner of the hub, with the session cookie the web board
// uses (the one-time link from the hub's shell, swapped for a session).
func (h *hubProc) ownerGet(path string, v any) {
	h.t.Helper()
	if h.cookie == "" {
		var link struct{ Secret string }
		h.m.mustJSON(&link, "hub", "owner-link", "--json")
		body := strings.NewReader(fmt.Sprintf(`{"secret":%q}`, link.Secret))
		req, _ := http.NewRequest("POST", h.base+"/v1/session/owner", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", publicURL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			h.t.Fatal(err)
		}
		resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == "wy_session" {
				h.cookie = c.Name + "=" + c.Value
			}
		}
		if h.cookie == "" {
			h.t.Fatalf("no owner session cookie (status %d)", resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", h.base+path, nil)
	req.Header.Set("Cookie", h.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		h.t.Fatalf("GET %s as owner: %d %s", path, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, v); err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
}

// fleet is the shared setup of E1..E7 and E11: the laptop (which hosts the hub), a hub on a
// loopback port, and the gpu-box (its own HOME and WY_HOME, a machine token, a board token).
type fleet struct {
	t   *testing.T
	l   *laptop
	gpu *machine
	hub *hubProc
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	l := newLaptop(t)
	seats(t, l.home)
	hub := startHub(t, l.machine)
	hub.connect(l.machine)
	hub.connectBoard(l.machine)
	gpu := newBox(t, "gpu-box")
	seats(t, gpu.home)
	hub.connect(gpu)
	hub.boardToken(gpu)
	return &fleet{t: t, l: l, gpu: gpu, hub: hub}
}

// seed is E1's data: three seats on two machines, among them a plain-text question, a
// permission and a rate limit. It returns the session ids.
type seeded struct {
	asked, perm, done      string // laptop: work, personal, team
	limited, working, gdon string // gpu-box: work, personal, team
}

func (f *fleet) seed() seeded {
	f.t.Helper()
	s := seeded{
		asked: "e1000000-0000-4000-8000-00000000000a", perm: "e1000000-0000-4000-8000-00000000000b", done: "e1000000-0000-4000-8000-00000000000c",
		limited: "e1000000-0000-4000-8000-00000000000d", working: "e1000000-0000-4000-8000-00000000000e", gdon: "e1000000-0000-4000-8000-00000000000f",
	}
	l, g := f.l.machine, f.gpu
	l.hook(l.slot(2), s.asked, "Stop", map[string]any{"last_assistant_message": "Reworked the orders table.\n\nShould I keep the old column?"})
	l.hook(l.slot(1), s.perm, "PermissionRequest", map[string]any{"tool_name": "Bash"})
	l.hook(l.slot(3), s.done, "Stop", map[string]any{"last_assistant_message": "Everything is done:\n```\ngo test ./...\n```\nAll green."})
	g.hook(g.slot(2), s.limited, "StopFailure", map[string]any{"error": "rate_limit"})
	g.hook(g.slot(1), s.working, "UserPromptSubmit", map[string]any{"prompt": "go"})
	g.hook(g.slot(3), s.gdon, "Stop", map[string]any{"last_assistant_message": "Refactor finished."})
	return s
}

// ---- the hub's own reads (what the web board renders) ----

type hubSession struct {
	ID          string  `json:"id"`
	State       string  `json:"state"`
	Detail      *string `json:"detail"`
	NeedsYou    bool    `json:"needs_you"`
	Question    *string `json:"question"`
	LastMessage *string `json:"last_message"`
	Seat        *string `json:"seat"`
	Machine     *string `json:"machine"`
	Tool        *string `json:"tool"`
	FocusURL    *string `json:"focus_url"`
}

type hubAccount struct {
	ID string `json:"id"`
}

func sp(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

var workingStatus = regexp.MustCompile(`^\d+[smhd]$`)

// diffHub lists how a View differs from what /v1/sessions + /v1/accounts say (the web
// board's data, read with an owner session): the ids and their order (needs-you list, groups
// in account order, rows in session order), and per row the state, detail, `status` word and
// `line2`. An empty result means they are equal.
func (f *fleet) diffHub(v boardView) []string {
	var ss struct {
		Sessions []hubSession `json:"sessions"`
	}
	var as struct {
		Accounts []hubAccount `json:"accounts"`
	}
	f.hub.ownerGet("/v1/sessions?limit=300", &ss)
	f.hub.ownerGet("/v1/accounts", &as)

	var bad []string
	var needs []string
	for _, s := range ss.Sessions {
		if s.NeedsYou {
			needs = append(needs, s.ID)
		}
	}
	if got := strings.Join(v.NeedsYou, ","); got != strings.Join(needs, ",") {
		bad = append(bad, fmt.Sprintf("needs_you = %s, hub = %s", got, strings.Join(needs, ",")))
	}
	var want []string
	for _, a := range as.Accounts {
		for _, s := range ss.Sessions {
			if sp(s.Seat) == a.ID {
				want = append(want, s.ID)
			}
		}
	}
	for _, s := range ss.Sessions {
		if sp(s.Seat) == "" {
			want = append(want, s.ID) // Unattributed comes last
		}
	}
	if got := v.order(); strings.Join(got, ",") != strings.Join(want, ",") {
		bad = append(bad, fmt.Sprintf("order = %v, hub = %v", got, want))
	}
	if len(v.Rows) != len(ss.Sessions) {
		bad = append(bad, fmt.Sprintf("%d rows, the hub lists %d sessions", len(v.Rows), len(ss.Sessions)))
	}
	for _, s := range ss.Sessions {
		r, ok := v.Rows[s.ID]
		if !ok {
			bad = append(bad, "row "+s.ID+" missing")
			continue
		}
		if r.State != s.State || r.Detail != sp(s.Detail) || r.NeedsYou != s.NeedsYou {
			bad = append(bad, fmt.Sprintf("%s: %s/%s/%v, hub %s/%s/%v", s.ID, r.State, r.Detail, r.NeedsYou, s.State, sp(s.Detail), s.NeedsYou))
		}
		var wantStatus, wantLine2 string
		switch s.State {
		case "asked":
			wantStatus = "asked"
			if sp(s.Detail) == "tool" && sp(s.Tool) != "" {
				wantStatus = "asked · " + sp(s.Tool)
			}
			wantLine2 = board.Cut(board.Line(sp(s.Question)), 200)
		case "needs_permission":
			wantStatus = "perm"
			if sp(s.Tool) != "" {
				wantStatus = "perm · " + sp(s.Tool)
			}
		case "failed":
			if sp(s.Detail) == "rate_limit" && !strings.HasPrefix(r.Status, "limit") {
				bad = append(bad, fmt.Sprintf("%s: status %q, want limit…", s.ID, r.Status))
			}
		case "done":
			wantStatus = "done"
			if sp(s.Detail) == "started" {
				wantStatus = "new"
			}
			wantLine2 = board.Recap(sp(s.LastMessage), 200)
		case "working":
			if !workingStatus.MatchString(r.Status) {
				bad = append(bad, fmt.Sprintf("%s: working status %q", s.ID, r.Status))
			}
			wantLine2 = board.Recap(sp(s.LastMessage), 200)
		}
		if wantStatus != "" && r.Status != wantStatus {
			bad = append(bad, fmt.Sprintf("%s: status %q, want %q", s.ID, r.Status, wantStatus))
		}
		if s.State != "failed" && r.Line2 != wantLine2 {
			bad = append(bad, fmt.Sprintf("%s: line2 %q, want %q", s.ID, r.Line2, wantLine2))
		}
	}
	return bad
}

// waitSame polls fn until its View equals the hub's reads (events and refetches settle
// within a few hundred ms) and returns it; it fails with the last differences otherwise.
func (f *fleet) waitSame(d time.Duration, fn func() boardView) boardView {
	f.t.Helper()
	deadline := time.Now().Add(d)
	var bad []string
	for {
		v := fn()
		if bad = f.diffHub(v); len(bad) == 0 {
			return v
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the board never matched the hub's reads; last differences:\n  %s", strings.Join(bad, "\n  "))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// openerArgv is what the opener gets on this OS for a URL (termctl.Argv).
func openerArgv(url string) []string {
	if runtime.GOOS == "darwin" {
		return []string{"open", url}
	}
	return []string{"xdg-open", url}
}
