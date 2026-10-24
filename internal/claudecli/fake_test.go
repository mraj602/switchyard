package claudecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFolderEnviron(t *testing.T) {
	base := []string{"A=1", "CLAUDE_CONFIG_DIR=/old", "CLAUDE_CONFIG_DIR_X=keep"}
	got := Folder{Dir: "/h/.claude-2"}.Environ(base)
	want := []string{"A=1", "CLAUDE_CONFIG_DIR_X=keep", "CLAUDE_CONFIG_DIR=/h/.claude-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	got = Folder{Dir: "/h/.claude", Unset: true}.Environ(base)
	if !reflect.DeepEqual(got, []string{"A=1", "CLAUDE_CONFIG_DIR_X=keep"}) {
		t.Fatalf("unset: %v", got)
	}
	if base[1] != "CLAUDE_CONFIG_DIR=/old" {
		t.Fatal("base modified")
	}
}

func TestFakeAuth(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	a := Folder{Dir: "/h/.claude-2"}
	if st, _ := f.AuthStatus(ctx, a); st.LoggedIn {
		t.Fatal("fresh folder logged in")
	}
	f.SetLogin("/h/.claude-3", "x@y.z", "Org", "team")
	if st, _ := f.AuthStatus(ctx, Folder{Dir: "/h/.claude-3/"}); !st.LoggedIn || st.Email != "x@y.z" || st.Plan != "team" {
		t.Fatalf("%+v", st)
	}
	var out strings.Builder
	if err := f.AuthLogin(ctx, a, Stdio{In: strings.NewReader("b@example.com:Acme\n"), Out: &out}); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.AuthStatus(ctx, a); !st.LoggedIn || st.Email != "b@example.com" || st.Org != "Acme" {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(out.String(), "Paste code here") {
		t.Fatalf("no prompt: %q", out.String())
	}
	b := Folder{Dir: "/h/.claude-4"}
	_ = f.AuthLogin(ctx, b, Stdio{})
	if st, _ := f.AuthStatus(ctx, b); st.Email != "fake@example.com" {
		t.Fatalf("%+v", st)
	}
	if v, _ := f.Version(ctx); v.Version != "2.1.296" || v.Raw != "2.1.296 (Claude Code)" {
		t.Fatalf("%+v", v)
	}
}

// The plugin behaviours setup.md 5.2 relies on, as verified against Claude Code 2.1.296.
func TestFakePluginSemantics(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	owner := Folder{Dir: "/h/.claude", Unset: true}
	linked := Folder{Dir: "/h/.claude-2"} // plugins/ linked to the owner's, own settings
	shared := Folder{Dir: "/h/.claude-3"} // plugins/ and settings.json linked to the owner's
	f.LinkRoot(linked.Dir, owner.Dir)
	f.LinkRoot(shared.Dir, owner.Dir)
	f.LinkSettings(shared.Dir, owner.Dir)

	mkt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mkt, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mkt, ".claude-plugin", "marketplace.json"), []byte(`{"name":"wy-local"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// install before the marketplace exists fails
	if _, err := f.PluginInstall(ctx, owner, "watchyard@wy-local"); err == nil {
		t.Fatal("install without marketplace succeeded")
	}
	if r, err := f.PluginMarketplaceAdd(ctx, owner, mkt); err != nil || !r.OK() {
		t.Fatal(r, err)
	}
	if got := f.Marketplaces(linked.Dir); !reflect.DeepEqual(got, []string{"wy-local"}) {
		t.Fatalf("marketplaces via shared root = %v", got)
	}
	if _, err := f.PluginInstall(ctx, owner, "watchyard@wy-local"); err != nil {
		t.Fatal(err)
	}
	if !f.Enabled(owner.Dir, "watchyard@wy-local") || !f.Enabled(shared.Dir, "watchyard@wy-local") {
		t.Fatal("not enabled in the owner's settings target (and the folder sharing it)")
	}
	if f.Enabled(linked.Dir, "watchyard@wy-local") {
		t.Fatal("a folder with its own settings must not be enabled by someone else's install")
	}
	if got := f.Installed(linked.Dir); len(got) != 1 {
		t.Fatalf("installed via shared root = %v", got)
	}
	// Enable in the folder with its own settings.
	if _, err := f.PluginEnable(ctx, linked, "watchyard@wy-local"); err != nil || !f.Enabled(linked.Dir, "watchyard@wy-local") {
		t.Fatal(err)
	}
	list, _ := f.PluginList(ctx, linked)
	if len(list) != 1 || !list[0].Enabled {
		t.Fatalf("%+v", list)
	}
	// Uninstall from one folder removes the record for all; enabledPlugins stay.
	if _, err := f.PluginUninstall(ctx, linked, "watchyard@wy-local"); err != nil {
		t.Fatal(err)
	}
	if len(f.Installed(owner.Dir)) != 0 || len(f.Installed(shared.Dir)) != 0 {
		t.Fatal("record not removed for every folder sharing the root")
	}
	if !f.Enabled(owner.Dir, "watchyard@wy-local") {
		t.Fatal("enabledPlugins must survive an uninstall")
	}
	if _, err := f.PluginEnable(ctx, linked, "watchyard@wy-local"); err == nil {
		t.Fatal("enable of an uninstalled plugin succeeded")
	}
	if _, err := f.PluginMarketplaceRemove(ctx, owner, "wy-local"); err != nil {
		t.Fatal(err)
	}
	if len(f.Marketplaces(owner.Dir)) != 0 {
		t.Fatal("marketplace not removed")
	}
	// removing it again is "already done", as with the real claude
	if res, err := f.PluginMarketplaceRemove(ctx, owner, "wy-local"); err != nil || !res.AlreadyInGoalState || !res.OK() {
		t.Fatalf("second remove: %+v %v", res, err)
	}
}

func TestFakeCallsFailuresAndContext(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	fo := Folder{Dir: "/h/.claude-2"}
	_, _ = f.PluginMarketplaceAdd(ctx, fo, "/x/market")
	_, _ = f.PluginInstall(ctx, fo, "p@market")
	calls := f.Calls()
	if len(calls) != 2 || calls[0].Method != "PluginMarketplaceAdd" || calls[0].Args[0] != "/x/market" ||
		calls[1].Folder != fo {
		t.Fatalf("%+v", calls)
	}
	if n := len(f.CallsTo("PluginInstall")); n != 1 {
		t.Fatal(n)
	}
	boom := errors.New("boom")
	f.FailOn("PluginInstall", boom)
	if _, err := f.PluginInstall(ctx, fo, "p@market"); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	f.FailOn("PluginInstall", nil)
	if _, err := f.PluginInstall(ctx, fo, "p@market"); errors.Is(err, boom) {
		t.Fatal("failure not cleared")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.Agents(cctx, fo); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.SetAgents(fo.Dir, []Agent{{ID: "s1", Status: "idle"}})
	if ags, _ := f.Agents(ctx, fo); len(ags) != 1 || ags[0].ID != "s1" {
		t.Fatalf("%+v", ags)
	}
}
