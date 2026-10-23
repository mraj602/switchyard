package config

import (
	"strings"
	"testing"
)

func TestHubReachDirectPushUpdateKeys(t *testing.T) {
	f, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Typed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Hub.Reach != ReachDirect || c.Hub.Dial != "" || c.Push.Direct.Enabled ||
		c.Push.Direct.Server != "https://ntfy.sh" || !c.Push.Direct.Private || c.Push.Direct.AfterS != 120 ||
		c.Update.BaseURL != "" || !c.Update.VerifyAttestation || c.Machines.HubAdmin != "" {
		t.Fatalf("defaults: %+v", c)
	}
	for k, args := range map[string][]string{
		"hub.reach":                 {"forward"},
		"hub.dial":                  {"127.0.0.1:47830"},
		"push.direct.enabled":       {"true"},
		"push.direct.server":        {"https://ntfy.example.org"},
		"push.direct.private":       {"false"},
		"push.direct.after_s":       {"90"},
		"update.base_url":           {"http://127.0.0.1:9999/releases"},
		"update.verify_attestation": {"false"},
		"machines.hub_admin":        {"ssh hub sy"},
	} {
		if err := f.Set(k, args); err != nil {
			t.Errorf("Set %s: %v", k, err)
		}
	}
	c, err = f.Typed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Hub.Reach != "forward" || c.Hub.Dial != "127.0.0.1:47830" || !c.Push.Direct.Enabled || c.Push.Direct.Private ||
		c.Push.Direct.AfterS != 90 || c.Machines.HubAdmin != "ssh hub sy" || c.Update.VerifyAttestation {
		t.Fatalf("after set: %+v", c)
	}
	if u := f.Unknown(); len(u) != 0 {
		t.Fatalf("hub, push.direct and update keys reported unknown: %v", u)
	}
}

func TestHubReachDialUpdateValidation(t *testing.T) {
	f, _ := Load(t.TempDir())
	bad := map[string][]string{
		"hub.reach":           {"auto"},
		"hub.dial":            {"hub.example.com:443"},
		"push.direct.server":  {"ntfy.sh"},
		"update.base_url":     {"http://example.com/releases"},
		"push.direct.after_s": {"0"},
	}
	for k, args := range bad {
		if err := f.Set(k, args); err == nil {
			t.Errorf("Set %s %v accepted", k, args)
		}
	}
	for _, v := range []string{"", "127.0.0.1:47830", "[::1]:8787", "localhost:1", "unix:/home/me/.local/state/switchyard/hub.sock"} {
		if err := CheckDial(v); err != nil {
			t.Errorf("CheckDial(%q): %v", v, err)
		}
	}
	for _, v := range []string{"10.0.0.2:8787", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:99999", "unix:", "gpu-box:22", "0.0.0.0:80"} {
		if err := CheckDial(v); err == nil || !strings.Contains(err.Error(), "hub.dial") {
			t.Errorf("CheckDial(%q) = %v, want an error", v, err)
		}
	}
	for _, v := range []string{"", "https://mirror.example.com/rel", "http://localhost:8080/x", "http://127.0.0.1:1/x"} {
		if err := CheckUpdateBase(v); err != nil {
			t.Errorf("CheckUpdateBase(%q): %v", v, err)
		}
	}
	for _, v := range []string{"http://example.com", "ftp://x.y", "https://"} {
		if err := CheckUpdateBase(v); err == nil {
			t.Errorf("CheckUpdateBase(%q) accepted", v)
		}
	}
}
