package doctor

import (
	"runtime"
	"testing"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/paths"
)

func TestCheckAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no agent on Windows")
	}
	tmp := t.TempDir()
	o := Options{
		Dirs:   paths.Dirs{State: paths.Dir{Path: tmp}},
		Getenv: func(k string) string { return map[string]string{"XDG_RUNTIME_DIR": tmp}[k] },
	}
	var cfg config.Config
	var rep Report
	checkAgent(&rep, o, cfg)
	if len(rep.Checks) != 0 {
		t.Fatalf("peers off, yet a check: %+v", rep.Checks)
	}
	cfg.Peers.Enabled = true
	checkAgent(&rep, o, cfg)
	if len(rep.Checks) != 1 || rep.Checks[0].Level != Warn || rep.Checks[0].Fix != "sy agent enable" {
		t.Fatalf("%+v", rep.Checks)
	}
}
