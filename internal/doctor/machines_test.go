package doctor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/directpush"
	"github.com/mraj602/watchyard/internal/hub/store"
)

func runMachine(t *testing.T, e fakeEnv, cfg config.Config) Report {
	t.Helper()
	var rep Report
	checkMachine(context.Background(), &rep, e.opts(nil), cfg)
	return rep
}

func writeFlushStatus(t *testing.T, e fakeEnv, body string) {
	t.Helper()
	write(t, filepath.Join(e.dirs.State.Path, "outbox", client.StatusFile), body)
}

func TestMachineChecksSilentWithoutHub(t *testing.T) {
	e := newFakeHome(t, `{}`)
	if rep := runMachine(t, e, config.Defaults()); len(rep.Checks) != 0 {
		t.Errorf("checks without a hub: %+v", rep.Checks)
	}
}

func TestReachLines(t *testing.T) {
	e := newFakeHome(t, `{}`)
	cfg := config.Defaults()
	cfg.Hub.URL = "https://hub.example"
	if c := pcheck(t, runMachine(t, e, cfg), "reach"); c.Level != OK || !strings.HasPrefix(c.Detail, "direct to https://hub.example") ||
		!strings.Contains(c.Detail, "no contact yet") {
		t.Errorf("direct: %+v", c)
	}

	writeFlushStatus(t, e, `{"time":"2026-10-12T11:58:00Z","ok":true,"last_contact":"2026-10-12T11:55:00Z","via":"pull"}`)
	pull := config.Defaults()
	pull.Hub.Reach = config.ReachPull
	c := pcheck(t, runMachine(t, e, pull), "reach")
	if c.Level != OK || !strings.HasPrefix(c.Detail, "pulled by the hub") || !strings.Contains(c.Detail, "last pull") ||
		!strings.Contains(c.Detail, "5m ago") {
		t.Errorf("pull: %+v", c)
	}
	writeFlushStatus(t, e, `{"time":"2026-10-12T11:58:00Z","ok":false,"last_contact":"2026-10-12T11:55:00Z","via":"pull","pull_refused":"frozen"}`)
	if c := pcheck(t, runMachine(t, e, pull), "reach"); c.Level != Warn || !strings.Contains(c.Detail, "refused: frozen") || c.Fix == "" {
		t.Errorf("pull refused: %+v", c)
	}

	fwd := config.Defaults()
	fwd.Hub.URL, fwd.Hub.Reach = "http://127.0.0.1:1", config.ReachForward
	if c := pcheck(t, runMachine(t, e, fwd), "reach"); !strings.HasPrefix(c.Detail, "hub reached through an SSH forward from the laptop; last contact") {
		t.Errorf("forward: %+v", c)
	}
}

func TestHeldRecordsSayUpdateTheHub(t *testing.T) {
	e := newFakeHome(t, `{}`)
	cfg := config.Defaults()
	cfg.Hub.URL = "https://hub.example"
	if rep := runMachine(t, e, cfg); hasCheck(rep, "held records") {
		t.Error("held records shown with nothing held")
	}
	writeFlushStatus(t, e, `{"time":"2026-10-12T11:58:00Z","ok":true,"held":3,"hub_too_old":{"hub":"0.4.2","record_v_max":1,"needs":2}}`)
	c := pcheck(t, runMachine(t, e, cfg), "held records")
	if c.Level != Warn || c.Detail != "the hub (0.4.2) reads records up to v1, this machine writes v2: 3 record(s) held back" ||
		!strings.Contains(c.Fix, "update the hub") {
		t.Errorf("held: %+v", c)
	}
}

func TestFrozenHubPrintsTheKeepTokenLine(t *testing.T) {
	e := newFakeHome(t, `{}`)
	cfg := config.Defaults()
	cfg.Hub.URL = "https://old.example"
	moved := "https://new.example"
	if err := os.MkdirAll(e.dirs.State.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := client.WriteHubCache(e.dirs.State.Path, client.HubCache{URL: cfg.Hub.URL, RecordVMax: 1, At: "2026-10-12T11:00:00Z",
		Frozen: &client.HealthFrozen{Since: "2026-10-12T10:00:00.000Z", MovedTo: &moved}}); err != nil {
		t.Fatal(err)
	}
	c := pcheck(t, runMachine(t, e, cfg), "hub frozen")
	if c.Level != Fail || c.Fix != "wy connect https://new.example --keep-token" {
		t.Errorf("frozen: %+v", c)
	}
	// Already pointed elsewhere: the old hub's cache does not count.
	cfg.Hub.URL = moved
	if rep := runMachine(t, e, cfg); hasCheck(rep, "hub frozen") {
		t.Error("frozen shown for another hub's cache")
	}
}

func TestDirectPushCheck(t *testing.T) {
	e := newFakeHome(t, `{}`)
	cfg := config.Defaults()
	cfg.Hub.URL = "https://hub.example"
	if rep := runMachine(t, e, cfg); hasCheck(rep, "direct push") {
		t.Error("direct push shown while off")
	}
	cfg.Push.Direct.Enabled = true
	if c := pcheck(t, runMachine(t, e, cfg), "direct push"); c.Level != Warn || !strings.Contains(c.Fix, "--secret-stdin") {
		t.Errorf("no topic: %+v", c)
	}
	st := e.dirs.State.Path
	if err := os.MkdirAll(st, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := directpush.SaveSecret(st, directpush.Secret{Topic: "sy-0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	if c := pcheck(t, runMachine(t, e, cfg), "direct push"); c.Level != Warn || !strings.Contains(c.Detail, "no policy") {
		t.Errorf("no policy: %+v", c)
	}
	now := e.opts(nil).Now()
	if err := directpush.SavePolicy(st, directpush.DefaultPolicy(), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c := pcheck(t, runMachine(t, e, cfg), "direct push")
	if c.Level != OK || !strings.Contains(c.Detail, "ntfy.sh") || !strings.Contains(c.Detail, "policy 2h") {
		t.Errorf("fresh: %+v", c)
	}
	if strings.Contains(c.Detail, "sy-0123") {
		t.Error("doctor printed the topic")
	}
	if err := directpush.SavePolicy(st, directpush.DefaultPolicy(), now.Add(-3*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c := pcheck(t, runMachine(t, e, cfg), "direct push"); c.Level != Warn || !strings.Contains(c.Fix, "--fetch") {
		t.Errorf("stale: %+v", c)
	}
}

func TestForwardPort(t *testing.T) {
	e := newFakeHome(t, `{}`)
	fwd := func(url, dial string) config.Config {
		c := config.Defaults()
		c.Hub.URL, c.Hub.Dial, c.Hub.Reach, c.Hub.InsecureLAN = url, dial, config.ReachForward, true
		return c
	}

	// Nothing listening: the port is free, no forward is open.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free := l.Addr().String()
	l.Close()
	if c := pcheck(t, runMachine(t, e, fwd("http://"+free, "")), "forward port"); c.Level != OK || !strings.Contains(c.Detail, "is free") {
		t.Errorf("free: %+v", c)
	}

	// The hub answers through the forward (hub.dial with an HTTPS-looking URL).
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"version":"0.5.0","schema":4,"record_v_max":1}`)
	}))
	defer hub.Close()
	addr := strings.TrimPrefix(hub.URL, "http://")
	if c := pcheck(t, runMachine(t, e, fwd("http://hub.example:8787", addr)), "forward port"); c.Level != OK || !strings.Contains(c.Detail, "the hub answers") {
		t.Errorf("open: %+v", c)
	}

	// Another program holds the port.
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	go func() {
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("SSH-2.0-not-a-hub\r\n"))
			c.Close()
		}
	}()
	if c := pcheck(t, runMachine(t, e, fwd("http://"+other.Addr().String(), "")), "forward port"); c.Level != Warn || !strings.Contains(c.Detail, "is taken") {
		t.Errorf("taken: %+v", c)
	}

	// A forward to a non-loopback URL without dial says where to look.
	if c := pcheck(t, runMachine(t, e, fwd("https://hub.example", "")), "forward port"); c.Level != Warn {
		t.Errorf("no landing: %+v", c)
	}
}

func TestHubPullTargetsAndKey(t *testing.T) {
	e := newFakeHome(t, `{}`)
	st, raw := hubDB(t, e)
	if rep := runPush(t, e); hasCheck(rep, "pull key") {
		t.Error("pull key checked with no pull target")
	}
	ctx := context.Background()
	for _, n := range []string{"srv1", "srv2", "srv3"} {
		if _, err := st.AddPullTarget(ctx, store.PullTarget{Name: n, Host: n + ".example", User: "dev", HostKeys: []string{"ssh-ed25519 AAAA"}}); err != nil {
			t.Fatal(err)
		}
	}
	lastOK := e.opts(nil).Now().Add(-90 * time.Second).UnixMilli()
	mustExec(t, raw, `UPDATE pull_target SET state = 'ok', last_ok_at = ? WHERE name = 'srv1'`, lastOK)
	mustExec(t, raw, `UPDATE pull_target SET state = 'host_key', last_error = 'host key mismatch' WHERE name = 'srv2'`)

	rep := runPush(t, e)
	if c := pcheck(t, rep, "pull srv1"); c.Level != OK || c.Detail != "ok, last pull 1m ago" {
		t.Errorf("srv1: %+v", c)
	}
	if c := pcheck(t, rep, "pull srv2"); c.Level != Fail || c.Fix != "check the server really changed its key, then: wy hub pull trust srv2" {
		t.Errorf("srv2: %+v", c)
	}
	if c := pcheck(t, rep, "pull srv3"); c.Level != OK || !strings.Contains(c.Detail, "first pull") {
		t.Errorf("srv3: %+v", c)
	}
	if c := pcheck(t, rep, "pull key"); c.Level != Fail {
		t.Errorf("missing key: %+v", c)
	}
	write(t, filepath.Join(e.dirs.State.Path, "pull", pullKeyFile), "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	if c := pcheck(t, runPush(t, e), "pull key"); c.Level != OK {
		t.Errorf("key present: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(e.dirs.State.Path, "pull")); err != nil {
		t.Fatal(err)
	}
}
