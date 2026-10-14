package clientenv

import (
	"os"
	"path/filepath"
	"testing"
)

func loadWith(t *testing.T, toml string) *Env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SY_CONFIG_DIR", "")
	t.Setenv("SY_STATE_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("SY_HOME", filepath.Join(home, "sy"))
	cfg := filepath.Join(home, "sy", "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "SY_HOME": filepath.Join(home, "sy")}
	e, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestInvalidUnrelatedKeyKeepsPrivacy(t *testing.T) {
	e := loadWith(t, "[privacy]\nlast_message = false\n[outbox]\nmax_mb = \"lots\"\n")
	if e.Config.Privacy.LastMessage {
		t.Error("an invalid unrelated key re-enabled privacy.last_message")
	}
	if e.ConfigErr == nil {
		t.Error("config error not reported")
	}
	if e.Config.Outbox.MaxMB != 50 {
		t.Errorf("bad key should fall back to its default, got %d", e.Config.Outbox.MaxMB)
	}
}

func TestInvalidPrivacyKeyIsMostPrivate(t *testing.T) {
	e := loadWith(t, "[privacy]\nlast_message = \"yes\"\n")
	if e.Config.Privacy.LastMessage {
		t.Error("an undecodable privacy key must read as private")
	}
	e = loadWith(t, "privacy = 3\n")
	if e.Config.Privacy.LastMessage {
		t.Error("a privacy table of the wrong type must read as private")
	}
}

func TestUnparseableConfigIsMostPrivate(t *testing.T) {
	e := loadWith(t, "this is = = not toml")
	if e.Config.Privacy.LastMessage || e.ConfigErr == nil {
		t.Errorf("last_message=%v err=%v", e.Config.Privacy.LastMessage, e.ConfigErr)
	}
}

func TestGoodConfigHasNoError(t *testing.T) {
	e := loadWith(t, "[outbox]\nmax_mb = 7\n")
	if e.ConfigErr != nil || e.Config.Outbox.MaxMB != 7 || !e.Config.Privacy.LastMessage {
		t.Errorf("%v %+v", e.ConfigErr, e.Config)
	}
}
