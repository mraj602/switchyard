package share

import (
	"os"
	"strings"
	"testing"
)

// A state file recreated by Claude Code without the
// mcpServers key is not a vote to delete every server everywhere. It receives the list.
func TestMCPRecreatedStateFileWithoutKeyDeletesNothing(t *testing.T) {
	e := newMCPEnv(t, `{"gh":`+srv("gh")+`}`, `{}`)
	e.sync()
	if e.names(1) != "gh" {
		t.Fatal("first sync")
	}
	if err := os.WriteFile(e.files[1].StateFile, []byte(`{"oauthAccount":{"x":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.sync()
	if e.names(0) != "gh" || e.names(1) != "gh" || len(r.Names.Removed) != 0 {
		t.Fatalf("%+v: a=%s b=%s", r, e.names(0), e.names(1))
	}
}

// A pass that would strip every server of the base from the folders
// still holding them is refused and recorded for sy doctor; a later normal pass clears it.
func TestMCPGuardRefusesRemovingEveryServer(t *testing.T) {
	e := newMCPEnv(t, `{"gh":`+srv("gh")+`,"fs":`+srv("fs")+`}`, `{}`)
	e.sync()
	if err := os.WriteFile(e.files[1].StateFile, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.sync()
	if r.Guard != "would-remove-all" || len(r.Written) != 0 {
		t.Fatalf("%+v", r)
	}
	if e.names(0) != "fs,gh" {
		t.Fatalf("primary emptied: %s", e.names(0))
	}
	g, err := ReadMCPGuard(e.state)
	if err != nil || g == nil || strings.Join(g.Removed, ",") != "fs,gh" || strings.Join(g.By, ",") != "~/.claude-2" {
		t.Fatalf("%+v %v", g, err)
	}
	// deleting one server (not all) still syncs, is logged, and clears the guard
	if err := os.WriteFile(e.files[1].StateFile, []byte(`{"mcpServers":{"fs":`+srv("fs")+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = e.sync()
	if e.names(0) != "fs" || strings.Join(r.Names.Removed, ",") != "gh" {
		t.Fatalf("%+v a=%s", r, e.names(0))
	}
	if g, _ := ReadMCPGuard(e.state); g != nil {
		t.Fatal("guard kept after a pass that ran")
	}
	rm, _ := ReadMCPRemovals(e.state)
	if len(rm) != 1 || rm[0].Name != "gh" || rm[0].By != "~/.claude-2" {
		t.Fatalf("%+v", rm)
	}
}
