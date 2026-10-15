package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/machineadd"
)

// linkRig is a machine paired by token only (`sy connect --token`: no link keys yet), with
// its own temp state folder, against a real hub API with a signing key.
type linkRig struct {
	t      *testing.T
	env    keysEnv
	tty    *fakeTTY
	out    *bytes.Buffer
	ks     *hubkeys.Service
	device string
	mid    string
	url    string
}

func newLinkRig(t *testing.T) *linkRig {
	t.Helper()
	hs, _, ks := keyHub(t)
	state := t.TempDir()
	m, err := identity.LoadOrCreateMachine(state, identity.MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.Create(context.Background(), ks.Store, auth.CreateRequest{Kind: "machine", Name: "worker", Machine: m.ID})
	if err != nil {
		t.Fatal(err)
	}
	r := &linkRig{t: t, tty: &fakeTTY{t: t}, out: &bytes.Buffer{}, ks: ks, device: c.Device.ID, mid: m.ID, url: hs.URL}
	r.env = keysEnv{
		state: state, stdout: r.out, stderr: r.out, stdin: strings.NewReader(""),
		guard: func() error { return nil },
		tty:   func() (keysTTY, error) { return r.tty, nil },
		hub:   func() (*client.Client, error) { return client.New(hs.URL, c.Token), nil },
		now:   time.Now, rand: rand.Reader, iter: 1000,
	}
	return r
}

func (r *linkRig) enable(repin bool, answers ...string) error {
	r.t.Helper()
	r.out.Reset()
	r.tty.answers = answers
	err := agentKeys(context.Background(), r.env, agentKeysOpts{repin: repin, hubURL: r.url, reach: config.ReachDirect})
	if len(r.tty.answers) != 0 {
		r.t.Errorf("unused answers %q", r.tty.answers)
	}
	return err
}

func (r *linkRig) pin(name string) keys.Pin {
	r.t.Helper()
	p, err := keys.ReadPin(keys.Path(r.env.state, name))
	if err != nil {
		r.t.Fatalf("%s: %v", name, err)
	}
	return p
}

func TestAgentKeysFreshThenRerun(t *testing.T) {
	r := newLinkRig(t)
	owner := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))
	od, _ := keys.NewOwnerKeyDoc(owner, r.ks.Key.FP, time.Now())
	if _, err := r.ks.ProposeAndConfirm(context.Background(), od); err != nil {
		t.Fatal(err)
	}
	if err := r.enable(false); err != nil {
		t.Fatal(err, r.out.String())
	}
	out := r.out.String()
	dk, err := keys.ReadDeviceKeys(r.env.state)
	if err != nil || dk.Device != r.device {
		t.Fatalf("device.json %q %v", dk.Device, err)
	}
	if p := r.pin(keys.HubPinFile); p.FP != r.ks.Key.FP || p.How != keys.PinTOFU {
		t.Fatalf("hub pin %+v", p)
	}
	if p := r.pin(keys.OwnerPinFile); p.FP != keys.Fingerprint(owner.Public().(ed25519.PublicKey)) || p.How != keys.PinTOFU {
		t.Fatalf("owner pin %+v", p)
	}
	c, err := r.ks.Cert(context.Background(), r.device)
	if err != nil || c.Body.Reg != keys.RegTOFU {
		t.Fatalf("hub cert %+v %v", c.Body, err)
	}
	for _, want := range []string{"Made this machine's link keys", "Pinned hub key", "reg tofu", "sy keys certify " + r.device, keys.GroupFP(c.Body.SignFP())} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// Again: the same keys, the same certificate, nothing revoked, nothing new pinned.
	if err := r.enable(false); err != nil {
		t.Fatal(err, r.out.String())
	}
	if strings.Contains(r.out.String(), "Made") || strings.Contains(r.out.String(), "Pinned") {
		t.Errorf("rerun made or pinned again:\n%s", r.out)
	}
	c2, _ := r.ks.Cert(context.Background(), r.device)
	if string(c2.Raw) != string(c.Raw) {
		t.Fatal("rerun reissued the certificate")
	}
	if rl, _ := r.ks.Revocations(context.Background()); rl != nil {
		t.Fatalf("rerun revoked keys: %+v", rl.Body)
	}

	// Owner-certified: the next step is gone.
	if err := r.ks.Certify(context.Background(), r.device, od.Sigs[0].FP, keys.SignDoc(keys.TypeDeviceCert, c.Raw, keys.ByOwner, owner).Sig); err != nil {
		t.Fatal(err)
	}
	if err := r.enable(false); err != nil || !strings.Contains(r.out.String(), "Owner-certified") || strings.Contains(r.out.String(), "Next:") {
		t.Fatalf("after certify: %v\n%s", err, r.out)
	}
}

func TestAgentKeysWrongPinnedHubAndRepin(t *testing.T) {
	r := newLinkRig(t)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey)
	if err := keys.SetPin(keys.Path(r.env.state, keys.HubPinFile), keys.NewPin(other, keys.PinTOFU, time.Now())); err != nil {
		t.Fatal(err)
	}
	err := r.enable(false)
	if err == nil || !strings.Contains(err.Error(), "sy agent enable --repin-hub") || !strings.Contains(err.Error(), keys.GroupFP(r.ks.Key.FP)) {
		t.Fatalf("wrong pinned hub: %v", err)
	}
	if p := r.pin(keys.HubPinFile); p.FP != keys.Fingerprint(other) {
		t.Fatal("pin changed without --repin-hub")
	}
	if _, err := r.ks.Cert(context.Background(), r.device); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("keys registered with a hub this machine does not trust: %v", err)
	}

	// --repin-hub runs the signing guard first.
	r.env.guard = func() error { return &keys.GuardError{PID: 4242, Name: "claude"} }
	var ee *exitError
	if err := r.enable(true); !errors.As(err, &ee) || ee.code != 4 {
		t.Fatalf("repin under the guard: %v", err)
	}
	r.env.guard = func() error { return nil }
	// Declined: nothing changes.
	if err := r.enable(true, "n"); err == nil || !strings.Contains(err.Error(), "not changed") {
		t.Fatalf("declined repin: %v", err)
	}
	if p := r.pin(keys.HubPinFile); p.FP != keys.Fingerprint(other) {
		t.Fatal("declined repin changed the pin")
	}
	// Confirmed: the old and the new fingerprint were shown, the pin is by hand, and the
	// keys are registered with the new hub.
	if err := r.enable(true, "y"); err != nil {
		t.Fatal(err, r.out.String())
	}
	if out := r.out.String(); !strings.Contains(out, "old "+keys.GroupFP(keys.Fingerprint(other))) || !strings.Contains(out, "new "+keys.GroupFP(r.ks.Key.FP)) {
		t.Fatalf("repin output:\n%s", out)
	}
	if p := r.pin(keys.HubPinFile); p.FP != r.ks.Key.FP || p.How != keys.PinHand {
		t.Fatalf("pin after repin %+v", p)
	}
	if _, err := r.ks.Cert(context.Background(), r.device); err != nil {
		t.Fatalf("no keys after repin: %v", err)
	}
	// Repin when the pin already matches: says so, asks nothing.
	if err := r.enable(true); err != nil || !strings.Contains(r.out.String(), "already pins") {
		t.Fatalf("repin with a matching pin: %v %s", err, r.out)
	}
}

func TestAgentKeysNoHubAndPull(t *testing.T) {
	r := newLinkRig(t)
	r.env.hub = func() (*client.Client, error) { t.Fatal("asked the hub"); return nil, nil }
	if err := agentKeys(context.Background(), r.env, agentKeysOpts{}); err != nil || !strings.Contains(r.out.String(), "No hub configured") {
		t.Fatalf("no hub: %v %s", err, r.out)
	}
	if _, err := keys.ReadDeviceKeys(r.env.state); err != nil {
		t.Fatalf("no keys made: %v", err)
	}
	r.out.Reset()
	if err := agentKeys(context.Background(), r.env, agentKeysOpts{hubURL: r.url, reach: config.ReachPull}); err != nil || !strings.Contains(r.out.String(), "no route") {
		t.Fatalf("pull: %v %s", err, r.out)
	}
	if err := agentKeys(context.Background(), r.env, agentKeysOpts{repin: true}); err == nil {
		t.Fatal("repin without a hub accepted")
	}
}

// The remote halves of sy machine add: `sy keys device` reports the keys and the device id
// (from the token), the hub host registers them (reg ssh), `sy keys pin-ssh` pins what the
// add hands over; sy agent enable afterwards keeps the ssh certificate.
func TestKeysDevicePinSSHThenAgentEnable(t *testing.T) {
	r := newLinkRig(t)
	ctx := context.Background()
	r.out.Reset()
	if err := keysMain(ctx, r.env, []string{"device", "--json"}); err != nil {
		t.Fatal(err)
	}
	var dev machineadd.DeviceReport
	if err := json.Unmarshal(r.out.Bytes(), &dev); err != nil || dev.Device != r.device || dev.Machine != r.mid || !dev.Created || dev.SignFP == "" {
		t.Fatalf("device report %s %v", r.out, err)
	}
	if _, err := r.ks.RegisterSSH(ctx, dev.Device, dev.Machine, dev.Keys); err != nil {
		t.Fatal(err)
	}
	c, _ := r.ks.Cert(ctx, dev.Device)
	owner := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32)).Public().(ed25519.PublicKey)
	h := machineadd.Handoff{HubKey: machineadd.PubKey{Alg: keys.AlgEd25519, Pub: keys.B64(r.ks.Key.Pub), FP: r.ks.Key.FP},
		Owner: &machineadd.PubKey{Alg: keys.AlgEd25519, Pub: keys.B64(owner), FP: keys.Fingerprint(owner)}, Cert: c.Doc}
	pinSSH := func(h machineadd.Handoff) error {
		b, _ := json.Marshal(h)
		r.env.stdin = bytes.NewReader(b)
		r.out.Reset()
		return keysMain(ctx, r.env, []string{"pin-ssh", "--json"})
	}
	// A certificate for other keys is refused before anything is pinned.
	bad := h
	other, _ := keys.NewDeviceKeys(time.Now(), rand.Reader)
	op, _ := other.Public()
	raw, _ := json.Marshal(keys.NewCertBody(dev.Device, "machine", "worker", r.mid, op, keys.RegSSH, r.ks.Key.FP, time.Now()))
	bad.Cert = keys.NewDoc(keys.TypeDeviceCert, raw)
	if err := r.ks.Key.Sign(&bad.Cert); err != nil {
		t.Fatal(err)
	}
	if err := pinSSH(bad); err == nil {
		t.Fatal("a certificate for other keys was accepted")
	}
	if _, err := os.Stat(keys.Path(r.env.state, keys.HubPinFile)); !os.IsNotExist(err) {
		t.Fatal("pinned after a bad handoff")
	}
	if err := pinSSH(h); err != nil {
		t.Fatal(err, r.out.String())
	}
	var pr machineadd.PinReport
	if err := json.Unmarshal(r.out.Bytes(), &pr); err != nil || pr.Hub != keys.PinSSH || pr.Owner != keys.PinSSH || pr.OwnerKept != "" {
		t.Fatalf("pin report %s %v", r.out, err)
	}
	if p := r.pin(keys.OwnerPinFile); p.FP != keys.Fingerprint(owner) || p.How != keys.PinSSH {
		t.Fatalf("owner pin %+v", p)
	}
	// Again: fine. Another hub key: refused, pointing at --repin-hub.
	if err := pinSSH(h); err != nil {
		t.Fatal(err)
	}
	evil := h
	ek, _ := hubkeys.FromSeed(bytes.Repeat([]byte{9}, 32))
	evil.HubKey = machineadd.PubKey{Pub: keys.B64(ek.Pub), FP: ek.FP}
	raw, _ = json.Marshal(keys.NewCertBody(dev.Device, "machine", "worker", r.mid, dev.Keys, keys.RegSSH, ek.FP, time.Now()))
	evil.Cert = keys.NewDoc(keys.TypeDeviceCert, raw)
	if err := ek.Sign(&evil.Cert); err != nil {
		t.Fatal(err)
	}
	if err := pinSSH(evil); err == nil || !strings.Contains(err.Error(), "--repin-hub") {
		t.Fatalf("another hub key: %v", err)
	}

	// sy agent enable on the machine: the ssh certificate is kept (no PUT, no tofu).
	if err := r.enable(false); err != nil {
		t.Fatal(err, r.out.String())
	}
	if !strings.Contains(r.out.String(), "reg ssh") || strings.Contains(r.out.String(), "Pinned") || strings.Contains(r.out.String(), "check that it shows") {
		t.Fatalf("agent enable after machine add:\n%s", r.out)
	}
	if c2, _ := r.ks.Cert(ctx, dev.Device); c2.Body.Reg != keys.RegSSH || string(c2.Raw) != string(c.Raw) {
		t.Fatal("agent enable replaced the ssh certificate")
	}

	// --renew makes new keys.
	r.out.Reset()
	if err := keysMain(ctx, r.env, []string{"device", "--json", "--renew"}); err != nil {
		t.Fatal(err)
	}
	var dev2 machineadd.DeviceReport
	if err := json.Unmarshal(r.out.Bytes(), &dev2); err != nil || dev2.SignFP == dev.SignFP || dev2.Device != r.device {
		t.Fatalf("renew %s %v", r.out, err)
	}
}
