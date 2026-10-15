package machineadd

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mraj602/switchyard/internal/keys"
)

// Link keys over SSH (keys.md 3.5, 4.2 reg ssh): `sy machine add` reads the machine's
// public keys over the person's SSH session, the hub host registers them through the admin
// channel, and the machine pins the hub key (and the operator's owner key) the add got from
// the hub host and from this machine, never from the network the machine sits on. Then the
// operator, the owner machine, may sign the certificate in the same run.

// DeviceReport is `sy keys device --json` on the remote: its device id (as its token says),
// machine id and public link keys, made at first use.
type DeviceReport struct {
	Device  string             `json:"device"`
	Machine string             `json:"machine"`
	Keys    keys.DeviceKeysNew `json:"keys"`
	SignFP  string             `json:"sign_fp"`
	BoxFP   string             `json:"box_fp"`
	Created bool               `json:"created"`
}

// Registered is `sy hub keys register --json` on the hub host.
type Registered struct {
	Device      string    `json:"device"`
	Reg         string    `json:"reg"`
	Cert        keys.Doc  `json:"cert"`
	OwnerSigned bool      `json:"owner_signed"`
	HubKey      PubKey    `json:"hub_key"`
	OwnerKey    *keys.Doc `json:"owner_key"`
}

// PubKey is a public key with its fingerprint ({"alg","pub","fp"}).
type PubKey struct {
	Alg string `json:"alg,omitempty"`
	Pub string `json:"pub"`
	FP  string `json:"fp"`
}

// Key decodes an Ed25519 key and checks it against its fingerprint.
func (p PubKey) Key() (ed25519.PublicKey, error) {
	b, err := keys.UnB64(p.Pub)
	if err != nil || len(b) != ed25519.PublicKeySize || keys.Fingerprint(b) != p.FP {
		return nil, errors.New("malformed key")
	}
	return b, nil
}

// Handoff is what `sy keys pin-ssh` reads on the remote's stdin: the hub key and the
// hub-signed certificate from the hub host, and the owner key this operator pins.
type Handoff struct {
	HubKey PubKey   `json:"hub_key"`
	Owner  *PubKey  `json:"owner,omitempty"`
	Cert   keys.Doc `json:"cert"`
}

// PinReport is `sy keys pin-ssh`'s answer.
type PinReport struct {
	Hub   string `json:"hub"`   // how the hub pin was set (ssh, or what an earlier pin says)
	Owner string `json:"owner"` // how the owner pin was set; "" when none
	// OwnerKept is the fingerprint of a different owner pin the remote kept.
	OwnerKept string `json:"owner_kept,omitempty"`
}

// KeysSummary is the keys part of the add summary.
type KeysSummary struct {
	Device      string `json:"device"`
	SignFP      string `json:"sign_fp"`
	BoxFP       string `json:"box_fp"`
	Reg         string `json:"reg"`
	OwnerSigned bool   `json:"owner_signed"`
	HubPinned   string `json:"hub_pin,omitempty"`
	OwnerPinned string `json:"owner_pin,omitempty"`
}

// RegisterKeysSSH is `sy hub keys register <device> --machine <id> --keys-stdin --json`.
func RegisterKeysSSH(ctx context.Context, a Admin, device, machine string, k keys.DeviceKeysNew) (Registered, error) {
	in, err := json.Marshal(k)
	if err != nil {
		return Registered{}, err
	}
	out, err := a.Hub(ctx, []string{"keys", "register", device, "--machine", machine, "--keys-stdin", "--json"}, in)
	if err != nil {
		return Registered{}, err
	}
	var r Registered
	if err := json.Unmarshal(firstJSON(out), &r); err != nil || r.Device == "" {
		return Registered{}, errors.New("unreadable answer from sy hub keys register")
	}
	return r, nil
}

// checkRegistered verifies the hub host's answer against what the remote reported: a hub key
// that matches its fingerprint, a certificate it signed for this device, machine and keys,
// reg ssh, and the owner signature only when it verifies.
func checkRegistered(r Registered, dev DeviceReport, machine string) (ed25519.PublicKey, keys.Cert, error) {
	hub, err := r.HubKey.Key()
	if err != nil {
		return nil, keys.Cert{}, fmt.Errorf("the hub's key: %v", err)
	}
	// The hub's part: an owner signature is the owner key's business (sy keys certify).
	c, err := keys.VerifyCert(r.Cert.Only(keys.ByHub), keys.Trust{Hub: hub}, false)
	if err != nil {
		return nil, keys.Cert{}, fmt.Errorf("the certificate does not verify with the hub's key: %v", err)
	}
	b := c.Body
	if b.Device != dev.Device || b.Machine == nil || *b.Machine != machine || b.Sign != dev.Keys.Sign || b.Box != dev.Keys.Box || b.Reg != keys.RegSSH {
		return nil, keys.Cert{}, errors.New("the certificate does not hold this machine's keys (reg ssh)")
	}
	return hub, c, nil
}

// linkKeys is the keys step (direct and forward only). Its failures do not undo the join:
// the add can be run again.
func (a *adder) linkKeys(ctx context.Context) error {
	if a.sum.Reach == ReachPull {
		a.e.emit("keys", "skip", fmt.Sprintf("%s is pulled by the hub and has no route to it, so it holds no link: no link keys (peers there reach only its own sessions)", a.sum.Name), nil)
		return nil
	}
	if a.e.Admin == nil {
		a.warn("keys", fmt.Sprintf("no admin channel, so the link keys were not registered over SSH: run `sy agent enable` on %s (registers them, reg tofu), then `sy keys certify` here", a.o.Host))
		return nil
	}
	dev, err := a.remoteDevice(ctx, false)
	if err != nil {
		return err
	}
	reg, err := RegisterKeysSSH(ctx, a.e.Admin, dev.Device, a.sum.Machine, dev.Keys)
	if err != nil && strings.Contains(err.Error(), "were revoked") {
		// Keys from a former pairing that was revoked: new ones, then again.
		if dev, err = a.remoteDevice(ctx, true); err != nil {
			return err
		}
		reg, err = RegisterKeysSSH(ctx, a.e.Admin, dev.Device, a.sum.Machine, dev.Keys)
	}
	if err != nil {
		return exitf(ExitFailed, "registering the link keys of %s: %v", a.o.Host, err)
	}
	hub, cert, err := checkRegistered(reg, dev, a.sum.Machine)
	if err != nil {
		return exitf(ExitFailed, "registering the link keys of %s: %v", a.o.Host, err)
	}
	if a.e.HubPin != nil {
		if p := a.e.HubPin(); p != nil && p.FP != reg.HubKey.FP {
			return exitf(ExitFailed, "the hub behind %s has key %s, but this machine pins %s: is the admin channel the right hub?",
				a.adminName(), keys.GroupFP(reg.HubKey.FP), keys.GroupFP(p.FP))
		}
	}
	h := Handoff{HubKey: PubKey{Alg: keys.AlgEd25519, Pub: reg.HubKey.Pub, FP: reg.HubKey.FP}, Cert: reg.Cert}
	if a.e.OwnerPin != nil {
		if p := a.e.OwnerPin(); p != nil {
			h.Owner = &PubKey{Alg: keys.AlgEd25519, Pub: p.Pub, FP: p.FP}
			if reg.OwnerKey != nil {
				if o, err := keys.VerifyOwnerKey(*reg.OwnerKey, hub); err == nil && o.Confirmed && o.FP != p.FP {
					a.warn("keys", fmt.Sprintf("the hub's active owner key is %s, but this machine pins %s; %s pins this machine's", keys.GroupFP(o.FP), keys.GroupFP(p.FP), a.sum.Name))
				}
			}
		}
	}
	pins, err := a.pinRemote(ctx, h)
	if err != nil {
		return err
	}
	ks := &KeysSummary{Device: dev.Device, SignFP: dev.SignFP, BoxFP: dev.BoxFP, Reg: reg.Reg, OwnerSigned: reg.OwnerSigned,
		HubPinned: pins.Hub, OwnerPinned: pins.Owner}
	a.sum.Keys = ks
	if pins.OwnerKept != "" {
		a.warn("keys", fmt.Sprintf("%s keeps its owner pin %s (not this machine's); check it with `sy keys status` there", a.sum.Name, keys.GroupFP(pins.OwnerKept)))
	}
	a.e.emit("keys", "ok", fmt.Sprintf("registered the link keys of %s over SSH (reg ssh; sign %s); it pins hub key %s", a.sum.Name,
		keys.GroupFP(dev.SignFP), keys.GroupFP(reg.HubKey.FP)), map[string]any{"device": dev.Device, "sign_fp": dev.SignFP, "box_fp": dev.BoxFP})
	if reg.OwnerSigned {
		return nil
	}
	next := fmt.Sprintf("run `sy keys certify %s` on the owner machine: peers across machines need the owner signature", dev.Device)
	if a.e.Certify == nil {
		a.warn("keys", next)
		return nil
	}
	fp, sig, err := a.e.Certify(ctx, a.sum.Name, cert)
	if err == nil {
		_, err = a.e.Admin.Hub(ctx, []string{"keys", "certify", dev.Device, "--fp", fp, "--sig-stdin"}, []byte(sig+"\n"))
	}
	if err != nil {
		a.warn("keys", fmt.Sprintf("not owner-certified (%v): %s", err, next))
		return nil
	}
	ks.OwnerSigned = true
	a.e.emit("keys", "ok", fmt.Sprintf("owner-certified %s with owner key %s", a.sum.Name, keys.GroupFP(fp)), nil)
	return nil
}

// remoteDevice runs `sy keys device --json [--renew]` on the remote (through the forward, so
// it can ask the hub for its device id).
func (a *adder) remoteDevice(ctx context.Context, renew bool) (DeviceReport, error) {
	words := []string{RemoteSy, "keys", "device", "--json"}
	if renew {
		words = append(words, "--renew")
	}
	res, err := a.runFwd(ctx, words, nil)
	if err != nil {
		return DeviceReport{}, exitf(ExitFailed, "%v", err)
	}
	var d DeviceReport
	if res.Exit != 0 || json.Unmarshal(firstJSON(res.Stdout), &d) != nil {
		return DeviceReport{}, exitf(ExitFailed, "`sy keys device` failed on %s (exit %d): %s", a.o.Host, res.Exit, firstLine(res.Stderr))
	}
	if d.Machine != a.sum.Machine {
		return DeviceReport{}, exitf(ExitFailed, "%s reports machine %s, not %s", a.o.Host, d.Machine, a.sum.Machine)
	}
	if d.Device == "" {
		return DeviceReport{}, exitf(ExitFailed, "%s does not know its device id (no hub token?): run `sy doctor` there", a.o.Host)
	}
	return d, nil
}

// pinRemote runs `sy keys pin-ssh` on the remote with the handoff on stdin.
func (a *adder) pinRemote(ctx context.Context, h Handoff) (PinReport, error) {
	in, err := json.Marshal(h)
	if err != nil {
		return PinReport{}, err
	}
	res, err := a.r.run(ctx, []string{RemoteSy, "keys", "pin-ssh", "--json"}, in)
	if err != nil {
		return PinReport{}, exitf(ExitFailed, "%v", err)
	}
	var p PinReport
	if res.Exit != 0 || json.Unmarshal(firstJSON(res.Stdout), &p) != nil {
		return PinReport{}, exitf(ExitFailed, "pinning the hub key on %s failed (exit %d): %s", a.o.Host, res.Exit, firstLine(res.Stderr))
	}
	return p, nil
}
