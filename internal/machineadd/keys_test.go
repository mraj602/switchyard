package machineadd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/keys"
)

const testDevice = "dev_01JA4TESTDEVICE"

// The fake hub host's signing key and an owner key (seeds as in link.md 10's vectors).
var (
	testHubPriv   = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	testHubPub    = testHubPriv.Public().(ed25519.PublicKey)
	testOwnerPriv = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))
	testOwnerPub  = testOwnerPriv.Public().(ed25519.PublicKey)
)

// deviceReport writes what the fake remote's `sy keys device --json` prints: fresh keys.
func (w *world) deviceReport(device string) keys.DeviceKeysNew {
	w.t.Helper()
	dk, err := keys.NewDeviceKeys(time.Now(), nil)
	if err != nil {
		w.t.Fatal(err)
	}
	pub, _ := dk.Public()
	sp, _ := keys.UnB64(pub.Sign.Pub)
	bp, _ := keys.UnB64(pub.Box.Pub)
	b, _ := json.Marshal(DeviceReport{Device: device, Machine: testMID, Keys: pub, SignFP: keys.Fingerprint(sp), BoxFP: keys.Fingerprint(bp)})
	if err := os.WriteFile(filepath.Join(w.remote, "keysdevice.json"), append(b, '\n'), 0o644); err != nil {
		w.t.Fatal(err)
	}
	return pub
}

// fakeRegister is the hub host's `sy hub keys register`: a real hub-signed certificate (reg
// ssh) of the keys on stdin, and the confirmed owner key.
func fakeRegister(args []string, stdin []byte) ([]byte, error) {
	var k keys.DeviceKeysNew
	if err := json.Unmarshal(stdin, &k); err != nil {
		return nil, &AdminError{Args: args, Exit: 1, Stderr: "bad keys"}
	}
	machine := ""
	for i, a := range args {
		if a == "--machine" && i+1 < len(args) {
			machine = args[i+1]
		}
	}
	hubFP := keys.Fingerprint(testHubPub)
	raw, _ := json.Marshal(keys.NewCertBody(args[2], "machine", "gpu-box", machine, k, keys.RegSSH, hubFP, time.Now()))
	cert := keys.NewDoc(keys.TypeDeviceCert, raw)
	if err := cert.Sign(keys.ByHub, testHubPriv); err != nil {
		return nil, err
	}
	od, _ := keys.NewOwnerKeyDoc(testOwnerPriv, hubFP, time.Now())
	_ = od.Sign(keys.ByHub, testHubPriv)
	b, _ := json.Marshal(Registered{Device: args[2], Reg: keys.RegSSH, Cert: cert,
		HubKey: PubKey{Alg: keys.AlgEd25519, Pub: keys.B64(testHubPub), FP: hubFP}, OwnerKey: &od})
	return append(b, '\n'), nil
}

func TestAddRegistersKeysOverSSH(t *testing.T) {
	w := newWorld(t)
	pub := w.deviceReport(testDevice)
	e := w.env()
	ownerPin := keys.NewPin(testOwnerPub, keys.PinLocal, time.Now())
	e.OwnerPin = func() *keys.Pin { return &ownerPin }
	var signed keys.Cert
	e.Certify = func(ctx context.Context, name string, c keys.Cert) (string, string, error) {
		signed = c
		return ownerPin.FP, keys.SignDoc(keys.TypeDeviceCert, c.Raw, keys.ByOwner, testOwnerPriv).Sig, nil
	}
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, e)
	if err != nil {
		t.Fatalf("Add: %v\nevents: %+v", err, w.events)
	}
	reg := w.admin.called("keys register")
	if strings.Join(reg, " ") != "keys register "+testDevice+" --machine "+testMID+" --keys-stdin --json" {
		t.Fatalf("register call %q", reg)
	}
	var sent keys.DeviceKeysNew
	if err := json.Unmarshal(w.admin.stdins[indexOf(w.admin, "keys register")], &sent); err != nil || sent.Sign != pub.Sign || sent.Box != pub.Box {
		t.Fatalf("keys sent to the hub %+v %v", sent, err)
	}
	// The handoff the remote pins: the hub's key and certificate, and this machine's owner pin.
	var h Handoff
	if err := json.Unmarshal([]byte(w.read("pin.stdin")), &h); err != nil {
		t.Fatal(err)
	}
	if h.HubKey.FP != keys.Fingerprint(testHubPub) || h.Owner == nil || h.Owner.FP != ownerPin.FP || h.Cert.Type != keys.TypeDeviceCert {
		t.Fatalf("handoff %+v", h)
	}
	if signed.Body.Reg != keys.RegSSH || signed.Body.Device != testDevice {
		t.Fatalf("certify got %+v", signed.Body)
	}
	cc := w.admin.called("keys certify")
	if len(cc) != 6 || cc[2] != testDevice || cc[4] != ownerPin.FP || cc[5] != "--sig-stdin" {
		t.Fatalf("certify call %q", cc)
	}
	if sum.Keys == nil || sum.Keys.Reg != keys.RegSSH || !sum.Keys.OwnerSigned || sum.Keys.Device != testDevice || sum.Keys.HubPinned != keys.PinSSH {
		t.Fatalf("keys summary %+v", sum.Keys)
	}
}

func indexOf(f *fakeAdmin, prefix string) int {
	for i, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return i
		}
	}
	return -1
}

func TestAddKeysNoCertifyLeavesNextStep(t *testing.T) {
	w := newWorld(t)
	e := w.env()
	e.Certify = func(context.Context, string, keys.Cert) (string, string, error) {
		return "", "", errors.New("no terminal for the passphrase")
	}
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, e)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if sum.Keys == nil || sum.Keys.OwnerSigned || w.admin.called("keys certify") != nil {
		t.Fatalf("keys %+v", sum.Keys)
	}
	if !strings.Contains(strings.Join(sum.Warnings, "\n"), "sy keys certify "+testDevice) {
		t.Fatalf("no next step in %q", sum.Warnings)
	}
}

func TestAddKeysRefusesAnotherHub(t *testing.T) {
	w := newWorld(t)
	e := w.env()
	other := keys.NewPin(testOwnerPub, keys.PinTOFU, time.Now()) // any key that is not the hub's
	e.HubPin = func() *keys.Pin { return &other }
	_, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, e)
	if err == nil || !strings.Contains(err.Error(), "this machine pins") {
		t.Fatalf("another hub's key accepted: %v", err)
	}
	if w.read("pin.stdin") != "" {
		t.Fatal("pinned on the remote anyway")
	}
}

func TestAddKeysRejectsCertForOtherKeys(t *testing.T) {
	w := newWorld(t)
	w.admin.answer = func(args []string, stdin []byte) ([]byte, error) {
		if strings.Join(args[:2], " ") == "keys register" {
			other, _ := keys.NewDeviceKeys(time.Now(), nil)
			op, _ := other.Public()
			b, _ := json.Marshal(op)
			return fakeRegister(args, b)
		}
		return hubAnswers(w.now)(args, stdin)
	}
	_, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env())
	if err == nil || !strings.Contains(err.Error(), "does not hold this machine's keys") {
		t.Fatalf("swapped keys accepted: %v", err)
	}
}

func TestAddKeysRenewsRevokedKeys(t *testing.T) {
	w := newWorld(t)
	n := 0
	w.admin.answer = func(args []string, stdin []byte) ([]byte, error) {
		if strings.Join(args[:2], " ") == "keys register" {
			if n++; n == 1 {
				return nil, &AdminError{Args: args, Exit: 1, Stderr: "these keys were revoked; make new ones on the machine and register those\n"}
			}
		}
		return hubAnswers(w.now)(args, stdin)
	}
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env())
	if err != nil || sum.Keys == nil {
		t.Fatalf("Add: %v %+v", err, w.events)
	}
	if !strings.Contains(w.read("keysdevice.args"), "--renew") {
		t.Fatalf("no renew: %q", w.read("keysdevice.args"))
	}
}

func TestAddPullSkipsKeys(t *testing.T) {
	w := newWorld(t)
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachPull, Binary: w.bin, Yes: true}, w.env())
	if err != nil {
		t.Fatalf("Add: %v %+v", err, w.events)
	}
	if sum.Keys != nil || w.admin.called("keys register") != nil || w.read("keysdevice.args") != "" {
		t.Fatal("keys step ran for a pull machine")
	}
}
