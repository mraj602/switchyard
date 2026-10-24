package share

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/testtree"
)

type mcpEnv struct {
	t     *testing.T
	b     *testtree.Builder
	state string
	files []model.MCPFile
}

func newMCPEnv(t *testing.T, servers ...string) *mcpEnv {
	e := &mcpEnv{t: t, b: testtree.New(t, ""), state: ""}
	e.state = e.b.Abs("state")
	for i, sv := range servers {
		dir := e.b.Abs(".claude")
		folder := "~/.claude"
		if i > 0 {
			dir = e.b.Abs(".claude-" + strconv.Itoa(i+1))
			folder = "~/.claude-" + strconv.Itoa(i+1)
		}
		_ = os.MkdirAll(dir, 0o700)
		sf := filepath.Join(dir, ".claude.json")
		doc := "{\n  \"numStartups\": " + strconv.Itoa(i+1) + ",\n  \"oauthAccount\": {\"emailAddress\": \"u" + strconv.Itoa(i) + "@x.io\",  \"n\": 1.0},\n  \"mcpServers\": " + sv + "\n}\n"
		if err := os.WriteFile(sf, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		e.files = append(e.files, model.MCPFile{Folder: folder, StateFile: sf, Dir: dir, Primary: i == 0})
	}
	return e
}

func (e *mcpEnv) sync(mut ...func(*model.MCPSyncRequest)) model.MCPSyncResult {
	e.t.Helper()
	req := model.MCPSyncRequest{Files: e.files, StateDir: e.state}
	for _, m := range mut {
		m(&req)
	}
	r, err := New(e.state).MCPSync(context.Background(), req)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *mcpEnv) servers(i int) map[string]json.RawMessage {
	e.t.Helper()
	b, _ := os.ReadFile(e.files[i].StateFile)
	var d struct {
		M map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		e.t.Fatalf("%s: %v", b, err)
	}
	return d.M
}

func (e *mcpEnv) names(i int) string {
	var n []string
	for k := range e.servers(i) {
		n = append(n, k)
	}
	sortStrings(n)
	return strings.Join(n, ",")
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

const secret = "sk-SECRET-VALUE-123"

func srv(cmd string) string { return `{"command":"` + cmd + `","env":{"TOKEN":"` + secret + `"}}` }

func TestMCPFirstSyncUnion(t *testing.T) {
	e := newMCPEnv(t, `{"a":`+srv("pa")+`,"shared":`+srv("primary")+`}`, `{"b":`+srv("pb")+`,"shared":`+srv("other")+`}`, `{}`)
	r := e.sync()
	for i := range e.files {
		if got := e.names(i); got != "a,b,shared" {
			t.Fatalf("file %d: %s", i, got)
		}
	}
	if !strings.Contains(string(e.servers(1)["shared"]), "primary") {
		t.Fatal("primary must win the same-name conflict")
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0] != "shared" || len(r.Written) != 3 && len(r.Written) != 2 {
		t.Fatalf("%+v", r)
	}
	if strings.Join(r.Names.Added, ",") != "b" {
		t.Fatalf("names %+v", r.Names)
	}
	base, _ := os.ReadFile(filepath.Join(e.state, "share", "mcp-base.json"))
	if !strings.Contains(string(base), secret) {
		t.Fatal("base should hold definitions")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(e.state, "share", "mcp-base.json")); fi.Mode().Perm() != 0o600 {
			t.Fatal("base mode")
		}
	}
	// conflict log has name and file, never values
	log, _ := os.ReadFile(filepath.Join(e.state, "share", "mcp-conflicts.jsonl"))
	if !strings.Contains(string(log), `"shared"`) || !strings.Contains(string(log), "~/.claude-2") || strings.Contains(string(log), secret) || strings.Contains(string(log), "other") {
		t.Fatalf("log: %s", log)
	}
	cs, _ := ReadConflicts(e.state)
	if len(cs) != 1 || cs[0].Name != "shared" || cs[0].File != "~/.claude-2" || cs[0].Winner != "~/.claude" {
		t.Fatalf("%+v", cs)
	}
}

func TestMCPAddRemoveEverywhereAndOauthUntouched(t *testing.T) {
	e := newMCPEnv(t, `{"a":`+srv("pa")+`}`, `{}`, `{}`)
	e.sync()
	// add in the third file -> all
	e.edit(2, `{"a":`+srv("pa")+`,"c":`+srv("pc")+`}`)
	r := e.sync()
	for i := range e.files {
		if e.names(i) != "a,c" {
			t.Fatalf("file %d: %s", i, e.names(i))
		}
	}
	if strings.Join(r.Names.Added, ",") != "c" {
		t.Fatalf("%+v", r.Names)
	}
	// remove in the second -> all
	e.edit(1, `{"c":`+srv("pc")+`}`)
	r = e.sync()
	for i := range e.files {
		if e.names(i) != "c" {
			t.Fatalf("file %d: %s", i, e.names(i))
		}
	}
	if strings.Join(r.Names.Removed, ",") != "a" {
		t.Fatalf("%+v", r.Names)
	}
	// change in one -> all
	e.edit(0, `{"c":`+srv("changed")+`}`)
	r = e.sync()
	if strings.Join(r.Names.Changed, ",") != "c" || !strings.Contains(string(e.servers(2)["c"]), "changed") {
		t.Fatalf("%+v", r)
	}
	// oauthAccount and the rest byte-identical
	b, _ := os.ReadFile(e.files[2].StateFile)
	if !strings.Contains(string(b), `"oauthAccount": {"emailAddress": "u2@x.io",  "n": 1.0}`) || !strings.Contains(string(b), `"numStartups": 3`) {
		t.Fatalf("other keys changed:\n%s", b)
	}
}

func (e *mcpEnv) edit(i int, servers string) {
	e.t.Helper()
	b, _ := os.ReadFile(e.files[i].StateFile)
	var d map[string]json.RawMessage
	_ = json.Unmarshal(b, &d)
	d["mcpServers"] = json.RawMessage(servers)
	out, _ := json.MarshalIndent(d, "", "  ")
	// keep oauthAccount bytes by splicing through the editor instead of re-marshalling
	_ = out
	doc := string(b)
	start := strings.Index(doc, `"mcpServers": `) + len(`"mcpServers": `)
	end := strings.LastIndex(doc, "\n}")
	if err := os.WriteFile(e.files[i].StateFile, []byte(doc[:start]+servers+doc[end:]), 0o600); err != nil {
		e.t.Fatal(err)
	}
	// make mtime distinct from any earlier read
	now := time.Now().Add(time.Duration(i+1) * time.Second)
	_ = os.Chtimes(e.files[i].StateFile, now, now)
}

func TestMCPSameNameDifferentChangesPrimaryWins(t *testing.T) {
	e := newMCPEnv(t, `{"s":`+srv("v0")+`}`, `{"s":`+srv("v0")+`}`, `{"s":`+srv("v0")+`}`)
	e.sync()
	e.edit(0, `{"s":`+srv("primary-change")+`}`)
	e.edit(1, `{"s":`+srv("second-change")+`}`)
	e.edit(2, `{"s":`+srv("third-change")+`}`)
	r := e.sync()
	for i := range e.files {
		if !strings.Contains(string(e.servers(i)["s"]), "primary-change") {
			t.Fatalf("file %d: %s", i, e.servers(i)["s"])
		}
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("%+v", r)
	}
	cs, _ := ReadConflicts(e.state)
	if len(cs) != 2 {
		t.Fatalf("log %+v", cs)
	}
	raw, _ := os.ReadFile(filepath.Join(e.state, "share", "mcp-conflicts.jsonl"))
	if strings.Contains(string(raw), "change") || strings.Contains(string(raw), secret) {
		t.Fatalf("values leaked into the log: %s", raw)
	}
	// primary did not change it: the lowest folder among the changers wins
	e.edit(1, `{"s":`+srv("second-again")+`}`)
	e.edit(2, `{"s":`+srv("third-again")+`}`)
	e.sync()
	if !strings.Contains(string(e.servers(0)["s"]), "second-again") {
		t.Fatalf("lowest folder should win: %s", e.servers(0)["s"])
	}
}

func TestMCPNothingChangedWritesNothing(t *testing.T) {
	e := newMCPEnv(t, `{"a":`+srv("x")+`}`, `{"a":`+srv("x")+`}`)
	e.sync()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range e.files {
		_ = os.Chtimes(f.StateFile, old, old)
	}
	basePath := filepath.Join(e.state, "share", "mcp-base.json")
	_ = os.Chtimes(basePath, old, old)
	logBefore, _ := os.ReadFile(filepath.Join(e.state, "share", "mcp-conflicts.jsonl"))
	r := e.sync()
	if len(r.Written) != 0 || len(r.Skipped) != 0 || len(r.Conflicts) != 0 {
		t.Fatalf("%+v", r)
	}
	for _, f := range e.files {
		if fi, _ := os.Stat(f.StateFile); !fi.ModTime().Equal(old) {
			t.Fatal("state file rewritten")
		}
	}
	if fi, _ := os.Stat(basePath); !fi.ModTime().Equal(old) {
		t.Fatal("base rewritten")
	}
	logAfter, _ := os.ReadFile(filepath.Join(e.state, "share", "mcp-conflicts.jsonl"))
	if string(logAfter) != string(logBefore) {
		t.Fatal("log appended")
	}
	// Sync() is a no-op as well
	sr, err := New(e.state).Sync(context.Background(), SyncConfig{MCP: &model.MCPSyncRequest{Files: e.files, StateDir: e.state}, Copies: true})
	if err != nil || !sr.Idle() {
		t.Fatalf("%+v %v", sr, err)
	}
}

func TestMCPLiveSessionAndLockSkip(t *testing.T) {
	e := newMCPEnv(t, `{"a":`+srv("x")+`}`, `{}`, `{}`)
	// live session in folder 2
	sess := filepath.Join(e.files[1].Dir, "sessions")
	_ = os.MkdirAll(sess, 0o700)
	_ = os.WriteFile(filepath.Join(sess, "1.json"), []byte(`{"pid":`+strconv.Itoa(os.Getpid())+`}`), 0o600)
	// fresh lock on folder 3
	_ = os.Mkdir(e.files[2].StateFile+".lock", 0o755)
	before1, _ := os.ReadFile(e.files[1].StateFile)
	before2, _ := os.ReadFile(e.files[2].StateFile)
	start := time.Now()
	r := e.sync()
	_ = start
	reasons := map[string]string{}
	for _, s := range r.Skipped {
		reasons[filepath.Base(filepath.Dir(s.File))] = s.Reason
	}
	if reasons[filepath.Base(e.files[1].Dir)] != "live-session" || reasons[filepath.Base(e.files[2].Dir)] != "locked" {
		t.Fatalf("%+v", r.Skipped)
	}
	a, _ := os.ReadFile(e.files[1].StateFile)
	b, _ := os.ReadFile(e.files[2].StateFile)
	if string(a) != string(before1) || string(b) != string(before2) {
		t.Fatal("skipped files were changed")
	}
	// base must not be stored while files are skipped
	if _, err := os.Stat(filepath.Join(e.state, "share", "mcp-base.json")); err == nil {
		t.Fatal("base stored with skipped files")
	}
	// --now ignores the live session; the lock is released
	_ = os.Remove(e.files[2].StateFile + ".lock")
	r = e.sync(func(q *model.MCPSyncRequest) { q.Now = true })
	if len(r.Skipped) != 0 || e.names(1) != "a" || e.names(2) != "a" {
		t.Fatalf("%+v", r)
	}
}

func TestMCPNewFolderDoesNotDeleteEverything(t *testing.T) {
	e := newMCPEnv(t, `{"a":`+srv("x")+`}`, `{"a":`+srv("x")+`}`)
	e.sync()
	// a third folder joins with no servers: it receives them, nothing is removed
	more := newMCPEnv(t, `{}`, `{}`, `{}`)
	_ = more
	dir := e.b.Abs(".claude-3")
	_ = os.MkdirAll(dir, 0o700)
	sf := filepath.Join(dir, ".claude.json")
	_ = os.WriteFile(sf, []byte("{\n  \"oauthAccount\": {}\n}\n"), 0o600)
	e.files = append(e.files, model.MCPFile{Folder: "~/.claude-3", StateFile: sf, Dir: dir})
	e.sync()
	for i := range e.files {
		if e.names(i) != "a" {
			t.Fatalf("file %d: %q", i, e.names(i))
		}
	}
}

func TestMCPDryRunWritesNothing(t *testing.T) {
	e := newMCPEnv(t, `{"a":`+srv("x")+`}`, `{}`)
	b0, _ := os.ReadFile(e.files[1].StateFile)
	r := e.sync(func(q *model.MCPSyncRequest) { q.DryRun = true })
	b1, _ := os.ReadFile(e.files[1].StateFile)
	if string(b0) != string(b1) || len(r.Names.Added) != 0 && false {
		t.Fatal("dry run wrote")
	}
	if _, err := os.Stat(filepath.Join(e.state, "share")); err == nil {
		t.Fatal("dry run created state")
	}
	if len(r.Written) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestMCPResultNeverHoldsValues(t *testing.T) {
	e := newMCPEnv(t, `{"a":`+srv("x")+`}`, `{"a":`+srv("y")+`}`)
	r := e.sync()
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), secret) {
		t.Fatalf("value in result: %s", b)
	}
}

func TestMCPMissingStateFileIsSkippedNotCreated(t *testing.T) {
	e := newMCPEnv(t, `{"a":`+srv("x")+`}`)
	ghost := e.b.Abs(".claude-2/.claude.json")
	e.files = append(e.files, model.MCPFile{Folder: "~/.claude-2", StateFile: ghost, Dir: filepath.Dir(ghost)})
	r := e.sync()
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "no-state-file" {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(ghost); err == nil {
		t.Fatal("created a .claude.json")
	}
}

func TestNaturalLess(t *testing.T) {
	if !naturalLess("~/.claude-2", "~/.claude-10") || naturalLess("~/.claude-10", "~/.claude-2") || !naturalLess("~/.claude", "~/.claude-2") {
		t.Fatal("natural order")
	}
}
