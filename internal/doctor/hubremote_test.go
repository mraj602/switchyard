package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
)

func remoteCfg(url string) config.Config {
	cfg := config.Config{}
	cfg.Hub.URL = url
	return cfg
}

func TestHubIsLocal(t *testing.T) {
	for _, c := range []struct {
		url, listen string
		want        bool
	}{
		{"", "127.0.0.1:8787", true},
		{"http://127.0.0.1:8787", "127.0.0.1:8787", true},
		{"http://localhost:8787", "", true},
		{"http://[::1]:8787", "", true},
		{"http://box.lan:8787", "box.lan:8787", true},
		{"https://sy.example.com", "127.0.0.1:8787", false},
		{"https://sy.mraj602.cyphersage.in", "", false},
		{"http://203.0.113.9:8787", "127.0.0.1:8787", false},
	} {
		if got := hubIsLocal(remoteCfg(c.url), c.listen); got != c.want {
			t.Errorf("hubIsLocal(%q, %q) = %v, want %v", c.url, c.listen, got, c.want)
		}
	}
}

// A hub that moved away leaves a hub.db behind: with hub.url on another host doctor must not
// read it (no push url / push targets warnings) and checks the remote hub instead.
func TestPushRemoteHubSkipsStaleDB(t *testing.T) {
	e := newFakeHome(t, `{}`)
	hubDB(t, e) // an empty, stale local database
	o := e.opts(nil)
	o.HealthProbe = func(_ context.Context, hub, _ string) (client.Health, error) {
		if hub != "https://sy.example.com" {
			t.Errorf("probed %q", hub)
		}
		return client.Health{OK: true, Version: "1.2.3"}, nil
	}
	var rep Report
	checkPush(context.Background(), &rep, o, remoteCfg("https://sy.example.com/"))
	for _, c := range rep.Checks {
		if strings.HasPrefix(c.Name, "push") || c.Name == "pull" {
			t.Errorf("hub-database check on a remote hub: %+v", c)
		}
	}
	if c := pcheck(t, rep, "hub health"); c.Level != OK || !strings.Contains(c.Detail, "version 1.2.3") {
		t.Errorf("%+v", c)
	}
}

func TestRemoteHubUnreachableAndPlainHTTP(t *testing.T) {
	e := newFakeHome(t, `{}`)
	o := e.opts(nil)
	o.HealthProbe = func(context.Context, string, string) (client.Health, error) {
		return client.Health{}, errors.New("connection refused")
	}
	var rep Report
	checkPush(context.Background(), &rep, o, remoteCfg("https://sy.example.com"))
	if c := pcheck(t, rep, "hub health"); c.Level != Warn || !strings.Contains(c.Detail, "connection refused") || c.Fix == "" {
		t.Errorf("%+v", c)
	}

	o.HealthProbe = func(context.Context, string, string) (client.Health, error) { return client.Health{OK: true}, nil }
	rep = Report{}
	checkPush(context.Background(), &rep, o, remoteCfg("http://sy.example.com"))
	if c := pcheck(t, rep, "hub health"); c.Level != Warn || !strings.Contains(c.Detail, "not https") {
		t.Errorf("%+v", c)
	}
}

// Merged mode (hub on 127.0.0.1) keeps the hub-database checks and makes no network call.
func TestPushLoopbackHubKeepsDBChecks(t *testing.T) {
	e := newFakeHome(t, `{}`)
	hubDB(t, e)
	o := e.opts(nil)
	o.HealthProbe = func(context.Context, string, string) (client.Health, error) {
		t.Error("probed a local hub")
		return client.Health{}, nil
	}
	var rep Report
	checkPush(context.Background(), &rep, o, remoteCfg("http://127.0.0.1:8787"))
	for _, n := range []string{"push url", "push keys", "push targets"} {
		pcheck(t, rep, n)
	}
}

// The push url fix no longer claims tailscale serve is the only way.
func TestPushURLFixWording(t *testing.T) {
	e := newFakeHome(t, `{}`)
	hubDB(t, e)
	c := pcheck(t, runPush(t, e), "push url")
	for _, want := range []string{"sy hub serve --public-url https://<hub address>", "if your tailnet supports HTTPS certificates, `tailscale serve --bg 8787`"} {
		if !strings.Contains(c.Fix, want) {
			t.Errorf("fix lacks %q: %s", want, c.Fix)
		}
	}
}
