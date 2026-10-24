package peers_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

// envFix mints device certificates the way the hub does (hub signature, owner signature)
// and envelopes between two machines, all from fixed seeds.
type envFix struct {
	t                *testing.T
	hub, owner       ed25519.PrivateKey
	trust            keys.Trust
	now              time.Time
	laptop, gpubox   keys.DeviceKeys
	laptopC, gpuboxC keys.Doc
}

func seed(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func newEnvFix(t *testing.T) *envFix {
	f := &envFix{t: t, hub: ed25519.NewKeyFromSeed(seed(1)), owner: ed25519.NewKeyFromSeed(seed(4)), now: time.Unix(1_800_000_000, 0)}
	f.trust = keys.Trust{Hub: f.hub.Public().(ed25519.PublicKey), Owner: f.owner.Public().(ed25519.PublicKey)}
	f.laptop, f.laptopC = f.device("dev_laptop", "m_laptop", 7, true)
	f.gpubox, f.gpuboxC = f.device("dev_gpubox", "m_gpubox", 8, true)
	return f
}

// device makes keys and a machine certificate; owner false leaves the owner signature off.
func (f *envFix) device(id, machine string, s byte, owner bool) (keys.DeviceKeys, keys.Doc) {
	f.t.Helper()
	dk, err := keys.NewDeviceKeys(f.now, bytes.NewReader(bytes.Repeat([]byte{s}, 64)))
	if err != nil {
		f.t.Fatal(err)
	}
	dk.Device = id
	pub, _ := dk.Public()
	body := keys.NewCertBody(id, "machine", id, machine, pub, keys.RegQR, keys.Fingerprint(f.trust.Hub), f.now)
	raw, _ := json.Marshal(body)
	d := keys.NewDoc(keys.TypeDeviceCert, raw)
	if err := d.Sign(keys.ByHub, f.hub); err != nil {
		f.t.Fatal(err)
	}
	if owner {
		if err := d.Sign(keys.ByOwner, f.owner); err != nil {
			f.t.Fatal(err)
		}
	}
	return dk, d
}

func (f *envFix) envelope() peers.Envelope {
	return peers.Envelope{Msg: "5f0c9a2e-0000-4000-8000-000000000001", Thread: "t_3a9e000000000000", Kind: peers.KindStart,
		From:      peers.EnvFrom{Session: "sess-a", Device: "dev_laptop", Machine: "m_laptop", Account: "acc-work", Name: "api", Handle: "api@work.laptop", Mode: peers.ModeBypass},
		To:        peers.EnvTo{Session: "sess-b", Device: "dev_gpubox", Machine: "m_gpubox", Account: "acc-personal"},
		CreatedAt: f.now.UTC().Format(time.RFC3339Nano), Exp: f.now.Add(time.Hour).Unix()}
}

// seal is the sender's side: check the target's certificate, seal, sign.
func (f *envFix) seal(e peers.Envelope, text string, sign keys.DeviceKeys) keys.Doc {
	f.t.Helper()
	target, err := peers.CheckMachineCert(f.gpuboxC, f.trust, nil, e.To.Device)
	if err != nil {
		f.t.Fatal(err)
	}
	sk, _ := sign.SignKey()
	d, err := peers.SealEnvelope(e, text, target, sk)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *envFix) open(d, cert keys.Doc, rev *keys.RevocationBody) (string, error) {
	e, _, err := peers.CheckIncoming(d, cert, f.trust, rev, "dev_gpubox", f.now)
	if err != nil {
		return "", err
	}
	box, _ := f.gpubox.BoxKey()
	return e.Open(box)
}

func TestEnvelopeRoundTrip(t *testing.T) {
	f := newEnvFix(t)
	d := f.seal(f.envelope(), "hello gpu-box", f.laptop)
	got, err := f.open(d, f.laptopC, nil)
	if err != nil || got != "hello gpu-box" {
		t.Fatalf("open: %q %v", got, err)
	}
	// The body the hub relays holds no text.
	if raw, _ := d.BodyBytes(); bytes.Contains(raw, []byte("hello gpu-box")) {
		t.Fatal("the envelope carries the text in the clear")
	}
}

func reasonIs(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted, want %s", reason)
	}
	if got := peers.ReasonOf(err); got != reason {
		t.Fatalf("reason %s (%v), want %s", got, err, reason)
	}
}

// Every field is covered by the signature: changing any one is bad_signature.
func TestEnvelopeTamper(t *testing.T) {
	f := newEnvFix(t)
	d := f.seal(f.envelope(), "hello", f.laptop)
	raw, _ := d.BodyBytes()
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	for _, k := range []string{"msg", "thread", "kind", "hop", "created_at", "exp", "size", "sealed", "from", "to"} {
		m := map[string]any{}
		for kk, v := range fields {
			m[kk] = v
		}
		switch v := m[k].(type) {
		case string:
			m[k] = v + "x"
		case float64:
			m[k] = v + 1
		case map[string]any:
			c := map[string]any{}
			for kk, vv := range v {
				c[kk] = vv
			}
			c["session"] = "sess-z"
			m[k] = c
		}
		nb, _ := json.Marshal(m)
		td := d
		td.Body = keys.B64(nb)
		_, err := f.open(td, f.laptopC, nil)
		reasonIs(t, err, peers.ReasonBadSignature)
	}
}

func TestEnvelopeChecks(t *testing.T) {
	f := newEnvFix(t)
	e := f.envelope()

	// Wrong to.device.
	d := f.seal(e, "hello", f.laptop)
	_, _, err := peers.CheckIncoming(d, f.laptopC, f.trust, nil, "dev_other", f.now)
	reasonIs(t, err, peers.ReasonWrongDevice)

	// Expired.
	_, _, err = peers.CheckIncoming(d, f.laptopC, f.trust, nil, "dev_gpubox", f.now.Add(2*time.Hour))
	reasonIs(t, err, peers.ReasonExpired)

	// exp more than 24 h after created_at (peers.md 8.2) is rejected, 24 h exactly is not.
	long := e
	long.Exp = f.now.Add(peers.MaxTTL + time.Second).Unix()
	_, _, err = peers.CheckIncoming(f.seal(long, "hello", f.laptop), f.laptopC, f.trust, nil, "dev_gpubox", f.now)
	reasonIs(t, err, peers.ReasonBadSignature)
	edge := e
	edge.Exp = f.now.Add(peers.MaxTTL).Unix()
	if _, _, err = peers.CheckIncoming(f.seal(edge, "hello", f.laptop), f.laptopC, f.trust, nil, "dev_gpubox", f.now); err != nil {
		t.Errorf("exp = created_at + 24h: %v", err)
	}

	// A created_at far ahead of the receiver's clock would let exp outlive the replay window.
	ahead := e
	ahead.CreatedAt = f.now.Add(48 * time.Hour).UTC().Format(time.RFC3339Nano)
	ahead.Exp = f.now.Add(49 * time.Hour).Unix()
	_, _, err = peers.CheckIncoming(f.seal(ahead, "hello", f.laptop), f.laptopC, f.trust, nil, "dev_gpubox", f.now)
	reasonIs(t, err, peers.ReasonBadSignature)

	// The thread and message ids end up in file names, audit lines and the hub's rows: only
	// their own forms are taken.
	for _, mut := range []func(*peers.Envelope){
		func(e *peers.Envelope) { e.Thread = "t_1\n[Watchyard] forged" },
		func(e *peers.Envelope) { e.Thread = "../../x" },
		func(e *peers.Envelope) { e.Msg = "../m" },
		func(e *peers.Envelope) { e.Msg = "" },
	} {
		bad := e
		mut(&bad)
		_, _, err = peers.CheckIncoming(f.seal(bad, "hello", f.laptop), f.laptopC, f.trust, nil, "dev_gpubox", f.now)
		reasonIs(t, err, peers.ReasonBadSignature)
	}

	// A sender certificate without the owner signature.
	_, bare := f.device("dev_laptop", "m_laptop", 7, false)
	_, err = f.open(d, bare, nil)
	reasonIs(t, err, peers.ReasonUncertified)

	// A revoked sender.
	rev := &keys.RevocationBody{Devices: []keys.RevokedDevice{{Device: "dev_laptop"}}}
	_, err = f.open(d, f.laptopC, rev)
	reasonIs(t, err, peers.ReasonRevoked)

	// A certificate whose machine differs from from.machine.
	other := e
	other.From.Machine = "m_elsewhere"
	_, err = f.open(f.seal(other, "hello", f.laptop), f.laptopC, nil)
	reasonIs(t, err, peers.ReasonBadSignature)

	// Signed by another machine's key, with the laptop's certificate.
	_, err = f.open(f.seal(e, "hello", f.gpubox), f.laptopC, nil)
	reasonIs(t, err, peers.ReasonBadSignature)
}

// HPKE's info binds the body to the two devices: sealed with another from.device, it does
// not open.
func TestEnvelopeSealInfo(t *testing.T) {
	f := newEnvFix(t)
	e := f.envelope()
	target, _ := peers.CheckMachineCert(f.gpuboxC, f.trust, nil, "dev_gpubox")
	pt, _ := json.Marshal(map[string]string{"text": "hi"})
	sealed, err := link.SealHPKE(target.Body.BoxPub(), peers.PeerSealInfo(e.Msg, "dev_mallory", e.To.Device), pt)
	if err != nil {
		t.Fatal(err)
	}
	e.Sealed = keys.B64(sealed)
	box, _ := f.gpubox.BoxKey()
	if _, err := e.Open(box); !errors.Is(err, link.ErrOpen) {
		t.Fatalf("open with the wrong info: %v", err)
	}
}

// The sender refuses to seal to a key the owner did not certify (a hub handing out a key of
// its own), and to a revoked one.
func TestSenderRefusesUncertifiedTarget(t *testing.T) {
	f := newEnvFix(t)
	_, bare := f.device("dev_gpubox", "m_gpubox", 9, false)
	_, err := peers.CheckMachineCert(bare, f.trust, nil, "dev_gpubox")
	reasonIs(t, err, peers.ReasonUncertified)
	_, err = peers.CheckMachineCert(f.gpuboxC, keys.Trust{Hub: f.trust.Hub}, nil, "dev_gpubox")
	reasonIs(t, err, peers.ReasonUncertified)
	rev := &keys.RevocationBody{Devices: []keys.RevokedDevice{{Device: "dev_gpubox"}}}
	_, err = peers.CheckMachineCert(f.gpuboxC, f.trust, rev, "dev_gpubox")
	reasonIs(t, err, peers.ReasonRevoked)
	_, err = peers.CheckMachineCert(f.gpuboxC, f.trust, nil, "dev_other")
	reasonIs(t, err, peers.ReasonBadSignature)
	// SealEnvelope itself refuses a certificate without the owner signature.
	c, _ := keys.VerifyCert(bare, keys.Trust{Hub: f.trust.Hub}, false)
	sk, _ := f.laptop.SignKey()
	if _, err := peers.SealEnvelope(f.envelope(), "x", c, sk); err == nil {
		t.Fatal("sealed to an uncertified key")
	}
}

// A tofu machine that replaced its keys is listed with the old ones ("replaced", keys.md 4.2
// step 3): its new certificate is not revoked. A listing of its current keys is.
func TestReplacedKeysAreNotARevocation(t *testing.T) {
	f := newEnvFix(t)
	d := f.seal(f.envelope(), "hello", f.laptop)
	sender, err := peers.CheckMachineCert(f.laptopC, f.trust, nil, "dev_laptop")
	if err != nil {
		t.Fatal(err)
	}
	replaced := &keys.RevocationBody{Devices: []keys.RevokedDevice{{Device: "dev_laptop", Keys: []string{"old-sign-fp", "old-box-fp"}, Reason: "replaced"}}}
	if got, err := f.open(d, f.laptopC, replaced); err != nil || got != "hello" {
		t.Fatalf("after a key replacement: %q, %v", got, err)
	}
	gpuboxReplaced := &keys.RevocationBody{Devices: []keys.RevokedDevice{{Device: "dev_gpubox", Keys: []string{"old-sign-fp"}, Reason: "replaced"}}}
	if _, err := peers.CheckMachineCert(f.gpuboxC, f.trust, gpuboxReplaced, "dev_gpubox"); err != nil {
		t.Fatalf("sealing to a machine that replaced its keys: %v", err)
	}
	lost := &keys.RevocationBody{Devices: []keys.RevokedDevice{replaced.Devices[0],
		{Device: "dev_laptop", Keys: []string{sender.Body.SignFP(), sender.Body.BoxFP()}, Reason: "lost"}}}
	_, err = f.open(d, f.laptopC, lost)
	reasonIs(t, err, peers.ReasonRevoked)
}
