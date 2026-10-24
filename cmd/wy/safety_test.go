package main

// Regression tests at the command layer: unshare, the setup
// lock, uninstall's confirmation and ledger, usage errors and --purge.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/share"
)

func (s *setupHome) state() string { return filepath.Join(s.wyHome, "state") }

// `account remove --unshare` makes independent copies; `wy tick` never copies a
// copy's edits over the primary.
func TestUnshareThenTickLeavesPrimary(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	s.mustWy(t, "setup", "--yes", "--hub", "later")
	s.mustWy(t, "account", "remove", "~/.claude-2", "--unshare", "--yes")
	if g, _ := share.LoadCopies(s.state()); len(g) != 0 {
		t.Fatalf("unshare registered synced copies: %+v", g)
	}
	before, _ := os.ReadFile(h.Abs(".claude/settings.json"))
	if fi, err := os.Lstat(h.Abs(".claude-2/settings.json")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("settings.json not unshared: %v", err)
	}
	if err := os.WriteFile(h.Abs(".claude-2/settings.json"), []byte(`{"theme":"mine-only"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.wy(t, "", "tick")
	if after, _ := os.ReadFile(h.Abs(".claude/settings.json")); string(after) != string(before) {
		t.Fatalf("primary overwritten by tick: %s", after)
	}
}

// Undo of an unshare keeps what was written in the copy
// since (skipped, exit 5); without new work it restores the link.
func TestUndoOfUnshareKeepsNewWork(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	s.mustWy(t, "setup", "--yes", "--hub", "later")
	s.mustWy(t, "account", "remove", "~/.claude-2", "--unshare", "--yes")
	work := h.Abs(".claude-2/skills/my-new-work.md")
	if err := os.WriteFile(work, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, code := s.wy(t, "", "setup", "--undo", "--yes")
	if code != backup.ExitSkipped {
		t.Fatalf("undo exit %d, want %d", code, backup.ExitSkipped)
	}
	if b, err := os.ReadFile(work); err != nil || string(b) != "precious" {
		t.Fatalf("new work lost: %v", err)
	}
	if fi, err := os.Lstat(h.Abs(".claude-2/commands")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("an untouched copy was not turned back into a link: %v", err)
	}
}

// A set left in-progress while another command holds setup.lock is that
// command's live run: wy setup refuses (exit 2) and never undoes it; a dry run does not
// report it as interrupted.
func TestInProgressSetIsNotUndoneWhileLocked(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	s.mustWy(t, "setup", "--yes", "--hub", "later")
	ents, _ := os.ReadDir(filepath.Join(s.state(), "backup"))
	if len(ents) == 0 {
		t.Fatal("no sets")
	}
	id := ents[len(ents)-1].Name()
	set, err := backup.Open(s.state(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.SetStatus(model.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	unlock, err := apply.Lock(s.state())
	if err != nil {
		t.Fatal(err)
	}
	_, errOut, code := s.wy(t, "", "setup", "--yes", "--hub", "later")
	if code != apply.ExitLocked || !strings.Contains(errOut, "setup.lock") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, args := range [][]string{{"setup", "--undo", "--yes"}, {"account", "remove", "~/.claude-2", "--yes"}, {"uninstall", "--yes"}} {
		if _, _, code := s.wy(t, "", args...); code != apply.ExitLocked {
			t.Fatalf("%v while locked: exit %d", args, code)
		}
	}
	out, _, _ := s.wy(t, "", "setup", "--dry-run", "--hub", "later")
	if strings.Contains(out, "interrupted") {
		t.Fatalf("a live run reported as interrupted:\n%s", out)
	}
	if st, _ := os.ReadFile(h.Abs(".claude/settings.json")); !strings.Contains(string(st), "watchyard@wy-local") {
		t.Fatal("the running set was undone")
	}
	unlock()
	// lock free: now it is an interrupted run, and --yes undoes it
	s.mustWy(t, "setup", "--yes", "--hub", "later")
	if st, _ := os.ReadFile(h.Abs(".claude/settings.json")); !strings.Contains(string(st), "watchyard@wy-local") {
		t.Fatal("setup after recovery did not apply")
	}
}

// `wy tick` skips its share sync (and succeeds) while setup.lock is held.
func TestTickSkipsShareWhileLocked(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	// a synced copy (Windows copy mode) whose primary changed: the next tick copies it over
	p, c := h.Abs("prim.json"), h.Abs("copy.json")
	for _, f := range []string{p, c} {
		if err := os.WriteFile(f, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte("old\n"))
	g := `{"v":1,"groups":[{"name":"prim.json","kind":"file","paths":["` + p + `","` + c + `"],"sha256":"` + hex.EncodeToString(sum[:]) + `"}]}`
	if err := os.MkdirAll(filepath.Dir(share.CopiesPath(s.state())), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(share.CopiesPath(s.state()), []byte(g), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Write(s.state(), model.Ledger{V: 1, Copies: []string{c}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	synced := func() bool { b, _ := os.ReadFile(c); return string(b) == "new\n" }
	unlock, err := apply.Lock(s.state())
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := s.wy(t, "", "tick"); code != 0 || strings.Contains(errOut, "share") {
		t.Fatalf("tick exit %d: %s", code, errOut)
	}
	if synced() {
		t.Fatal("tick synced while locked")
	}
	unlock()
	s.wy(t, "", "tick")
	if !synced() {
		t.Fatal("tick did not sync once the lock was free")
	}
}

// The timer and the hub service are in the uninstall plan, and nothing is
// removed without --yes or a terminal, even when the plan has no other action.
func TestUninstallAsksBeforeRemovingTimerAndService(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	led := model.Ledger{V: 1, Timer: true, HubService: &model.LedgerService{Name: "wy-hub", Unit: "x"}}
	if err := ledger.Write(s.state(), led); err != nil {
		t.Fatal(err)
	}
	out, _, code := s.wy(t, "", "uninstall", "--dry-run", "--json")
	var r struct {
		Uninstall model.UninstallSteps `json:"uninstall"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil || !r.Uninstall.Timer || r.Uninstall.HubService != "wy-hub" {
		t.Fatalf("exit %d: %s", code, out)
	}
	if out, _, _ := s.wy(t, "", "uninstall", "--dry-run"); !strings.Contains(out, "flush timer") || !strings.Contains(out, "hub service wy-hub") {
		t.Fatalf("plan text: %s", out)
	}
	if _, _, code := s.wy(t, "", "uninstall"); code != exitNeedsPerson {
		t.Fatalf("no terminal, no --yes: exit %d", code)
	}
	if got, _ := ledger.Read(s.state()); !got.Timer || got.HubService == nil {
		t.Fatalf("ledger changed without a confirmation: %+v", got)
	}
}

// Uninstall keeps the ledger record of what it left as is (a launcher changed
// by the person), so a later doctor or uninstall still knows it.
func TestUninstallKeepsRecordsOfWhatItLeft(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	l := h.Abs(".local/bin/claude-9")
	if err := os.WriteFile(l, []byte("#!/bin/sh\n# edited by me\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	led := model.Ledger{V: 1, Launchers: []model.LedgerLauncher{{Path: l, SHA256: strings.Repeat("0", 64)}}}
	if err := ledger.Write(s.state(), led); err != nil {
		t.Fatal(err)
	}
	s.wy(t, "", "uninstall", "--yes")
	got, err := ledger.Read(s.state())
	if err != nil || len(got.Launchers) != 1 || got.Launchers[0].Path != l {
		t.Fatalf("record of the launcher left as is dropped: %+v %v", got, err)
	}
	if _, err := os.Stat(l); err != nil {
		t.Fatal("changed launcher removed")
	}
}

// `wy account remove` does not overwrite a ledger it cannot read.
func TestAccountRemoveRefusesUnreadableLedger(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	s.mustWy(t, "setup", "--yes", "--hub", "later")
	bad := []byte("{not json")
	if err := os.WriteFile(ledger.Path(s.state()), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, code := s.wy(t, "", "account", "remove", "~/.claude-2", "--yes"); code == 0 {
		t.Fatal("account remove succeeded with an unreadable ledger")
	}
	if b, _ := os.ReadFile(ledger.Path(s.state())); string(b) != string(bad) {
		t.Fatalf("ledger overwritten: %s", b)
	}
}

// Findings 1/9: account remove stops syncing the folder's Windows copies (ledger and
// copies.json), so tick never writes into a folder wy no longer manages.
func TestAccountRemoveForgetsTheFolderCopies(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	s.mustWy(t, "setup", "--yes", "--hub", "later")
	c2, c3 := h.Abs(".claude-2/keybindings.json"), h.Abs(".claude-3/keybindings.json")
	led, err := ledger.Read(s.state())
	if err != nil {
		t.Fatal(err)
	}
	led.Copies = []string{c2, c3}
	if err := ledger.Write(s.state(), led); err != nil {
		t.Fatal(err)
	}
	cj := share.CopiesPath(s.state())
	if err := os.MkdirAll(filepath.Dir(cj), 0o700); err != nil {
		t.Fatal(err)
	}
	g := `{"v":1,"groups":[{"name":"keybindings.json","kind":"file","paths":["` + h.Abs(".claude/keybindings.json") + `","` + c2 + `","` + c3 + `"],"sha256":"x"}]}`
	if err := os.WriteFile(cj, []byte(g), 0o600); err != nil {
		t.Fatal(err)
	}
	s.mustWy(t, "account", "remove", "~/.claude-2", "--yes")
	if got, _ := ledger.Read(s.state()); len(got.Copies) != 1 || got.Copies[0] != c3 {
		t.Fatalf("ledger copies: %v", got.Copies)
	}
	groups, _ := share.LoadCopies(s.state())
	if len(groups) != 1 || strings.Contains(strings.Join(groups[0].Paths, ","), c2) {
		t.Fatalf("copies.json: %+v", groups)
	}
}

// Lower finding: an unknown flag is a usage error (exit 2), like every argument error.
func TestUnknownFlagExits2(t *testing.T) {
	s := newSetupHome(t, detecttest.Laptop(t))
	for _, args := range [][]string{{"setup", "--bogus"}, {"account", "list", "--bogus"}, {"uninstall", "--bogus"}, {"tick", "--bogus"}, {"paths", "--bogus"}} {
		if _, _, code := s.wy(t, "", args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// Lower finding: --purge refuses a state folder that is not wy's (WY_STATE_DIR anywhere).
func TestPurgeRefusesForeignStateDir(t *testing.T) {
	s := newSetupHome(t, detecttest.Laptop(t))
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "thesis.tex"), []byte("years of work"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.env = append(s.env, "WY_STATE_DIR="+foreign)
	if _, _, code := s.wy(t, "", "uninstall", "--yes", "--purge"); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(foreign, "thesis.tex")); err != nil {
		t.Fatal("foreign folder deleted")
	}
	s.env = append(s.env, "WY_STATE_DIR="+s.root)
	if _, _, code := s.wy(t, "", "uninstall", "--yes", "--purge"); code != exitUsage {
		t.Fatalf("home as state folder: exit %d", code)
	}
}
