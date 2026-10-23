package doctor

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/paths"
)

func TestCheckLinkKeys(t *testing.T) {
	tmp := t.TempDir()
	var probe func(context.Context) (CertInfo, error)
	o := Options{Dirs: paths.Dirs{State: paths.Dir{Path: tmp}},
		CertProbe: func(ctx context.Context) (CertInfo, error) { return probe(ctx) }}
	cfg := config.Config{}
	cfg.Hub.URL, cfg.Hub.Reach = "https://hub.example", config.ReachDirect
	check := func() []Check {
		var rep Report
		checkLinkKeys(&rep, o, cfg)
		return rep.Checks
	}

	// No hub, or pulled: nothing to say.
	pull := cfg
	pull.Hub.Reach = config.ReachPull
	var rep Report
	checkLinkKeys(&rep, o, pull)
	if len(rep.Checks) != 0 {
		t.Fatalf("pull machine: %+v", rep.Checks)
	}
	// No device.json.
	if c := check(); len(c) != 1 || !strings.Contains(c[0].Detail, "no link keys") || c[0].Fix != "sy agent enable" {
		t.Fatalf("no keys: %+v", c)
	}
	dk, _, err := keys.LoadOrCreateDeviceKeys(tmp, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := dk.Public()
	sp, _ := keys.UnB64(pub.Sign.Pub)
	fp := keys.Fingerprint(sp)

	probe = func(context.Context) (CertInfo, error) { return CertInfo{}, ErrNoCert }
	if c := check(); len(c) != 1 || c[0].Level != Warn || c[0].Fix != "sy agent enable" {
		t.Fatalf("none at the hub: %+v", c)
	}
	probe = func(context.Context) (CertInfo, error) { return CertInfo{}, errors.New("connection refused") }
	if c := check(); len(c) != 0 {
		t.Fatalf("hub down: %+v", c)
	}
	probe = func(context.Context) (CertInfo, error) { return CertInfo{Device: "dev_1", Reg: "ssh", SignFP: fp}, nil }
	// No owner key pinned here (the owner never ran sy keys init): the fix starts there.
	if c := check(); len(c) != 1 || !strings.Contains(c[0].Detail, "no owner key pinned") || !strings.Contains(c[0].Fix, "sy keys init") ||
		!strings.Contains(c[0].Fix, "sy keys certify --all") || !strings.Contains(c[0].Fix, "sy agent enable") {
		t.Fatalf("uncertified, no owner pin: %+v", c)
	}
	probe = func(context.Context) (CertInfo, error) {
		return CertInfo{Device: "dev_1", Reg: "ssh", SignFP: fp, OwnerSigned: true}, nil
	}
	if c := check(); len(c) != 1 || c[0].Level != Warn || !strings.Contains(c[0].Detail, "no owner key pinned") || !strings.HasPrefix(c[0].Fix, "sy agent enable") {
		t.Fatalf("certified, no owner pin: %+v", c)
	}
	opub, _, _ := ed25519.GenerateKey(nil)
	if err := keys.SetPin(keys.Path(tmp, keys.OwnerPinFile), keys.NewPin(opub, keys.PinTOFU, time.Now())); err != nil {
		t.Fatal(err)
	}
	probe = func(context.Context) (CertInfo, error) { return CertInfo{Device: "dev_1", Reg: "ssh", SignFP: fp}, nil }
	if c := check(); len(c) != 1 || !strings.Contains(c[0].Detail, "not owner-certified") || c[0].Fix != "on the owner machine: sy keys certify dev_1" {
		t.Fatalf("uncertified: %+v", c)
	}
	probe = func(context.Context) (CertInfo, error) {
		return CertInfo{Device: "dev_1", Reg: "ssh", SignFP: "aaaabbbbccccddddeeeeffff"}, nil
	}
	if c := check(); len(c) != 1 || !strings.Contains(c[0].Detail, "other link keys") {
		t.Fatalf("other keys: %+v", c)
	}
	probe = func(context.Context) (CertInfo, error) {
		return CertInfo{Device: "dev_1", Reg: "ssh", SignFP: fp, OwnerSigned: true}, nil
	}
	if c := check(); len(c) != 1 || c[0].Level != OK {
		t.Fatalf("certified: %+v", c)
	}
	// keys.md 4.3: certified before machine accounts existed, so nothing is bound.
	probe = func(context.Context) (CertInfo, error) {
		return CertInfo{Device: "dev_1", Reg: "ssh", SignFP: fp, OwnerSigned: true, BoundKnown: true}, nil
	}
	if c := check(); len(c) != 1 || c[0].Level != Warn || !strings.Contains(c[0].Detail, "no accounts are bound") ||
		c[0].Fix != "on the owner machine, in a terminal: sy keys certify dev_1" {
		t.Fatalf("no accounts bound: %+v", c)
	}
	probe = func(context.Context) (CertInfo, error) {
		return CertInfo{Device: "dev_1", Reg: "ssh", SignFP: fp, OwnerSigned: true, BoundKnown: true, Bound: []string{"acc"}}, nil
	}
	if c := check(); len(c) != 1 || c[0].Level != OK || !strings.Contains(c[0].Detail, "1 account(s) bound") {
		t.Fatalf("bound: %+v", c)
	}
}
