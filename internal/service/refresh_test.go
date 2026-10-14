package service

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/model"
)

func TestUnitArgsRoundTrip(t *testing.T) {
	args := []string{"hub", "serve", "--listen", "127.0.0.1:8787", "--public-url", "https://hub.example", "--db",
		`/home/al ice/"x"/50%/$HOME/a\b`, "tab\there"}
	sp := model.ServiceSpec{Name: "switchyard-hub", Args: args}
	e := envFor("linux", "/home/alice", nil)
	got, ok := systemdExecArgs(SystemdUnit(e, sp))
	if !ok || !reflect.DeepEqual(got, args) {
		t.Errorf("systemd: %q, %v; want %q", got, ok, args)
	}
	got, ok = launchdProgramArgs(LaunchdPlist(envFor("darwin", "/Users/alice", nil), sp))
	if !ok || !reflect.DeepEqual(got, args) {
		t.Errorf("launchd: %q, %v; want %q", got, ok, args)
	}
	if _, ok := splitSystemd(`"unterminated`); ok {
		t.Error("an unterminated quote parsed")
	}
}

func TestRefreshKeepsArgsRewritesOnlyOnChangeAndRestarts(t *testing.T) {
	home := t.TempDir()
	r := &rec{}
	e := envFor("linux", home, r)
	s := New(e)
	ctx := context.Background()

	// Not installed: nothing happens.
	res, err := s.Refresh(ctx, "switchyard-hub")
	if err != nil || res.Installed || len(r.calls) != 0 {
		t.Fatalf("no unit: %+v %v %v", res, err, r.lines())
	}

	sp := model.ServiceSpec{Name: "switchyard-hub", Args: []string{"hub", "serve", "--listen", "127.0.0.1:9999"}}
	if err := s.Install(ctx, sp); err != nil {
		t.Fatal(err)
	}
	p := SystemdUnitPath(e, "switchyard-hub")
	before, _ := os.ReadFile(p)
	r.calls = nil
	res, err = s.Refresh(ctx, "switchyard-hub")
	if err != nil || !res.Installed || res.Rewritten || !res.Restarted {
		t.Fatalf("same binary: %+v %v", res, err)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Error("unchanged unit rewritten")
	}
	if l := r.lines(); len(l) != 1 || l[0] != "systemctl --user try-restart switchyard-hub.service" {
		t.Errorf("calls: %v", l)
	}

	// Another binary path renders differently: rewritten with the same arguments.
	s.Env.SyPath = "/opt/sy/bin/sy"
	r.calls = nil
	res, err = s.Refresh(ctx, "switchyard-hub")
	if err != nil || !res.Rewritten || !res.Restarted {
		t.Fatalf("new path: %+v %v", res, err)
	}
	after, _ := os.ReadFile(p)
	if !strings.Contains(string(after), "ExecStart=/opt/sy/bin/sy hub serve --listen 127.0.0.1:9999\n") {
		t.Errorf("rewritten unit:\n%s", after)
	}
	if l := r.lines(); len(l) != 2 || l[0] != "systemctl --user daemon-reload" {
		t.Errorf("calls: %v", l)
	}
}
