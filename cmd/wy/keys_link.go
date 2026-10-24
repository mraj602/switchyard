package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/machineadd"
	"github.com/mraj602/watchyard/internal/paths"
)

// This machine's link keys (keys.md 3.5, 4.2, peers.md 2.1): the keys step of `wy agent
// enable` (tofu over the machine's token and route) and the two remote halves of `wy machine
// add` (reg ssh): `wy keys device` reports the keys, `wy keys pin-ssh` pins what the add
// got from the hub host.

func init() {
	agentKeysStep = func(ctx context.Context, stdout io.Writer, repin bool) error {
		env, err := realKeysEnv(stdout, os.Stderr)
		if err != nil {
			return err
		}
		o := agentKeysOpts{repin: repin}
		if d, err := paths.Resolve(paths.FromOS()); err == nil {
			if f, err := config.Load(d.Config.Path); err == nil {
				if cfg, err := f.Typed(); err == nil {
					o.hubURL, o.reach = cfg.Hub.URL, cfg.Hub.Reach
				}
			}
		}
		return agentKeys(ctx, env, o)
	}
}

type agentKeysOpts struct {
	repin         bool   // --repin-hub
	hubURL, reach string // config hub.url and hub.reach
}

// ownCert checks a certificate the hub sent this device: the hub's signature with the
// pinned key, and that it holds this device's keys. Owner signatures are not checked here
// (the hub and the peers check them).
func ownCert(doc keys.Doc, hub ed25519.PublicKey, pub keys.DeviceKeysNew) (keys.Cert, error) {
	c, err := keys.VerifyCert(doc.Only(keys.ByHub), keys.Trust{Hub: hub}, false)
	if err != nil {
		return keys.Cert{}, err
	}
	if c.Body.Sign != pub.Sign || c.Body.Box != pub.Box {
		return keys.Cert{}, errors.New("the certificate holds other keys than this machine's")
	}
	return c, nil
}

// agentKeys makes or loads the device keys, registers them with the hub over the machine's
// token (PUT /v1/devices/self/keys, reg tofu) unless the hub already holds them (registered
// by wy machine add, reg ssh, or an earlier run), pins the hub key on first use (or, with
// --repin-hub at a terminal, replaces it) and the hub's confirmed owner key, and prints the
// fingerprints and the next step.
func agentKeys(ctx context.Context, env keysEnv, o agentKeysOpts) error {
	if o.repin {
		// Replacing a pin is a signing-guard command (keys.md 8).
		if err := env.guard(); err != nil {
			return exitWith(4, err)
		}
	}
	w := env.stdout
	dk, created, err := keys.LoadOrCreateDeviceKeys(env.state, env.now())
	if err != nil {
		return fmt.Errorf("this machine's link keys: %w", err)
	}
	pub, err := dk.Public()
	if err != nil {
		return err
	}
	signFP, boxFP := fpOf(pub.Sign.Pub), fpOf(pub.Box.Pub)
	if created {
		fmt.Fprintf(w, "Made this machine's link keys: sign %s, box %s.\n", keys.GroupFP(signFP), keys.GroupFP(boxFP))
	}
	switch {
	case o.hubURL == "":
		if o.repin {
			return errors.New("no hub is configured here, so there is no hub key to pin (wy connect first)")
		}
		fmt.Fprintln(w, "No hub configured: the link keys are not registered (peers on this machine work without a hub).")
		return nil
	case o.reach == config.ReachPull:
		fmt.Fprintln(w, "This machine is pulled by the hub and has no route to it, so it holds no link: peers here reach only this machine's sessions.")
		return nil
	}
	hub, err := env.hub()
	if err != nil {
		return fmt.Errorf("registering the link keys: %w", err)
	}
	info, err := hub.Keys(ctx)
	if err != nil {
		return fmt.Errorf("reading the hub's key: %w", err)
	}
	b, err := keys.UnB64(info.HubKey.Pub)
	if err != nil || len(b) != ed25519.PublicKeySize || keys.Fingerprint(b) != info.HubKey.FP {
		return errors.New("the hub sent a malformed key")
	}
	hubPub := ed25519.PublicKey(b)
	if err := agentHubPin(env, hubPub, o.repin); err != nil {
		return err
	}

	// Registered already (wy machine add, reg ssh; an earlier run)? Else register, tofu.
	doc, signed, err := hub.DeviceCert(ctx, "self")
	c, cerr := ownCert(doc, hubPub, pub)
	if err != nil || cerr != nil {
		var se *client.StatusError
		if err != nil && !(errors.As(err, &se) && se.Status == http.StatusNotFound) {
			return fmt.Errorf("asking the hub for this machine's certificate: %w", err)
		}
		if doc, err = hub.PutKeys(ctx, pub); err != nil {
			if errors.As(err, &se) && se.Status == http.StatusConflict {
				return fmt.Errorf("the hub refused this machine's link keys: %s. Add the machine again with `wy machine add` from the owner machine, or pair it again", se.Message)
			}
			return fmt.Errorf("registering the link keys: %w", err)
		}
		if c, err = ownCert(doc, hubPub, pub); err != nil {
			return fmt.Errorf("%w (the hub's answer to the key registration: %v)", client.ErrTampered, err)
		}
		signed = doc.Has(keys.ByOwner)
	}
	if dk.Device != c.Body.Device {
		dk.Device = c.Body.Device
		if err := keys.WriteDeviceKeys(env.state, dk); err != nil {
			return err
		}
	}

	// The owner key: pinned on first use from the hub's confirmed document (keys.md 3.5).
	if info.OwnerKey != nil {
		if ok, err := keys.VerifyOwnerKey(info.OwnerKey.Doc, hubPub); err == nil && ok.Confirmed {
			cur, perr := keys.ReadPin(env.ownerPinPath())
			switch {
			case errors.Is(perr, os.ErrNotExist):
				if err := keys.SetPin(env.ownerPinPath(), keys.NewPin(ok.Pub, keys.PinTOFU, env.now())); err != nil {
					return err
				}
				fmt.Fprintf(w, "Pinned owner key %s (first use; compare with `wy keys status` on the owner machine).\n", keys.GroupFP(ok.FP))
			case perr == nil && cur.FP != ok.FP:
				fmt.Fprintf(w, "The hub's owner key is %s, but this machine pins %s (kept; wy keys trust-owner changes it).\n", keys.GroupFP(ok.FP), keys.GroupFP(cur.FP))
			}
		}
	}
	fmt.Fprintf(w, "Link keys of %s: sign %s, box %s (reg %s).\n", c.Body.Device, keys.GroupFP(signFP), keys.GroupFP(boxFP), c.Body.Reg)
	if signed {
		fmt.Fprintln(w, "Owner-certified: peers on other machines accept this machine.")
		return nil
	}
	cmp := ""
	if c.Body.Reg == keys.RegTOFU {
		cmp = fmt.Sprintf(" and check that it shows sign %s", keys.GroupFP(signFP))
	}
	fmt.Fprintf(w, "Next: on the owner machine run `wy keys certify %s`%s; peers across machines need the owner signature.\n", c.Body.Device, cmp)
	return nil
}

// agentHubPin pins the hub key on first use, accepts the pinned one, and replaces a
// different pin only with --repin-hub at a terminal after showing both (keys.md 3.4, 3.5).
func agentHubPin(env keysEnv, hub ed25519.PublicKey, repin bool) error {
	fp := keys.Fingerprint(hub)
	cur, err := keys.ReadPin(env.hubPinPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := keys.SetPin(env.hubPinPath(), keys.NewPin(hub, keys.PinTOFU, env.now())); err != nil {
			return err
		}
		fmt.Fprintf(env.stdout, "Pinned hub key %s (first use; compare with `wy hub keys show` on the hub).\n", keys.GroupFP(fp))
		return nil
	case err != nil:
		return err
	case cur.FP == fp:
		if repin {
			fmt.Fprintf(env.stdout, "This machine already pins hub key %s.\n", keys.GroupFP(fp))
		}
		return nil
	case !repin:
		return fmt.Errorf("this hub's key %s is not the one this machine pinned (%s). If you moved or rebuilt the hub, check the new fingerprint with `wy hub keys show` on the hub and run `wy agent enable --repin-hub`",
			keys.GroupFP(fp), keys.GroupFP(cur.FP))
	}
	t, err := env.tty()
	if err != nil {
		return err
	}
	defer t.Close()
	fmt.Fprintf(env.stdout, "hub key pin: old %s, new %s (compare the new one with `wy hub keys show` on the hub)\n", keys.GroupFP(cur.FP), keys.GroupFP(fp))
	if ok, err := confirmed(t, false, "Replace the pin?"); err != nil || !ok {
		return errors.New("pin not changed")
	}
	if err := keys.ReplacePin(env.hubPinPath(), keys.NewPin(hub, keys.PinHand, env.now())); err != nil {
		return err
	}
	fmt.Fprintf(env.stdout, "This machine now pins hub key %s.\n", keys.GroupFP(fp))
	return nil
}

// keysDevice is `wy keys device --json [--renew]`, run by wy machine add over SSH: this
// machine's device id (asked from the hub with its token when it can, else device.json),
// machine id and public link keys, made at first use. --renew makes new keys (the old ones
// were revoked).
func keysDevice(ctx context.Context, env keysEnv, args []string) error {
	fs := keysFlags("device", env)
	asJSON := fs.Bool("json", false, "print JSON")
	renew := fs.Bool("renew", false, "make new link keys in place of the current ones")
	if pos, err := parseKeysFlags(fs, args); err != nil || len(pos) > 0 || !*asJSON {
		return errUsage
	}
	dk, created, err := keys.LoadOrCreateDeviceKeys(env.state, env.now())
	if err != nil {
		return err
	}
	if *renew && !created {
		nk, err := keys.NewDeviceKeys(env.now(), env.rand)
		if err != nil {
			return err
		}
		nk.Device = dk.Device
		dk, created = nk, true
		if err := keys.WriteDeviceKeys(env.state, dk); err != nil {
			return err
		}
	}
	if hub, err := env.hub(); err == nil {
		qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		id, err := hub.MyDevice(qctx)
		cancel()
		if err == nil && id != dk.Device {
			dk.Device = id
			if err := keys.WriteDeviceKeys(env.state, dk); err != nil {
				return err
			}
		}
	}
	m, err := identity.LoadOrCreateMachine(env.state, identity.MachineOptions{})
	if err != nil {
		return err
	}
	pub, err := dk.Public()
	if err != nil {
		return err
	}
	return json.NewEncoder(env.stdout).Encode(machineadd.DeviceReport{Device: dk.Device, Machine: m.ID, Keys: pub,
		SignFP: fpOf(pub.Sign.Pub), BoxFP: fpOf(pub.Box.Pub), Created: created})
}

// keysPinSSH is `wy keys pin-ssh --json`, run by wy machine add over SSH with a
// machineadd.Handoff on stdin: the hub key and this device's certificate from the hub host,
// and the operator's owner key. It pins them (how ssh) where nothing is pinned yet; a
// different hub pin is refused (wy agent enable --repin-hub), a different owner pin kept.
func keysPinSSH(env keysEnv, args []string) error {
	fs := keysFlags("pin-ssh", env)
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parseKeysFlags(fs, args); err != nil || len(pos) > 0 || !*asJSON {
		return errUsage
	}
	var h machineadd.Handoff
	if err := json.NewDecoder(io.LimitReader(env.stdin, 64<<10)).Decode(&h); err != nil {
		return fmt.Errorf("the keys on stdin are not JSON: %w", err)
	}
	hub, err := h.HubKey.Key()
	if err != nil {
		return fmt.Errorf("the hub key on stdin: %w", err)
	}
	dk, err := keys.ReadDeviceKeys(env.state)
	if err != nil {
		return fmt.Errorf("this machine's link keys: %w", err)
	}
	pub, err := dk.Public()
	if err != nil {
		return err
	}
	c, err := ownCert(h.Cert, hub, pub)
	if err != nil {
		return fmt.Errorf("the certificate on stdin: %w", err)
	}
	m, err := identity.LoadOrCreateMachine(env.state, identity.MachineOptions{})
	if err != nil {
		return err
	}
	if c.Body.Machine == nil || *c.Body.Machine != m.ID {
		return errors.New("the certificate on stdin is for another machine")
	}
	now := env.now()
	if err := keys.SetPin(env.hubPinPath(), keys.NewPin(hub, keys.PinSSH, now)); err != nil {
		if errors.Is(err, keys.ErrPinned) {
			cur, _ := keys.ReadPin(env.hubPinPath())
			return fmt.Errorf("this machine pins hub key %s, but the hub's key is %s. If you moved or rebuilt the hub, check the new fingerprint with `wy hub keys show` on the hub and run `wy agent enable --repin-hub` here",
				keys.GroupFP(cur.FP), keys.GroupFP(h.HubKey.FP))
		}
		return err
	}
	var rep machineadd.PinReport
	if p, err := keys.ReadPin(env.hubPinPath()); err == nil {
		rep.Hub = p.How
	}
	if h.Owner != nil {
		ok, err := h.Owner.Key()
		if err != nil {
			return fmt.Errorf("the owner key on stdin: %w", err)
		}
		if err := keys.SetPin(env.ownerPinPath(), keys.NewPin(ok, keys.PinSSH, now)); err != nil && !errors.Is(err, keys.ErrPinned) {
			return err
		}
		if p, err := keys.ReadPin(env.ownerPinPath()); err == nil {
			rep.Owner = p.How
			if p.FP != h.Owner.FP {
				rep.OwnerKept = p.FP
			}
		}
	}
	if dk.Device != c.Body.Device {
		dk.Device = c.Body.Device
		if err := keys.WriteDeviceKeys(env.state, dk); err != nil {
			return err
		}
	}
	return json.NewEncoder(env.stdout).Encode(rep)
}

// machineAddKeys are the owner-machine parts of the keys step of wy machine add: this
// machine's pins, and the owner signature in the same run when a person is at a terminal.
func machineAddKeys(env *machineadd.Env, state string, interactive bool, stderr io.Writer) {
	pin := func(name string) func() *keys.Pin {
		return func() *keys.Pin {
			p, err := keys.ReadPin(keys.Path(state, name))
			if err != nil {
				return nil
			}
			return &p
		}
	}
	env.HubPin, env.OwnerPin = pin(keys.HubPinFile), pin(keys.OwnerPinFile)
	env.Certify = func(ctx context.Context, name string, c keys.Cert) (string, string, error) {
		kenv, err := realKeysEnv(stderr, stderr)
		if err != nil {
			return "", "", err
		}
		if _, err := os.Stat(kenv.sealedPath()); err != nil {
			return "", "", errors.New("this machine has no owner key")
		}
		if !interactive {
			return "", "", errors.New("no terminal for the passphrase")
		}
		if err := kenv.guard(); err != nil {
			return "", "", err
		}
		t, err := kenv.tty()
		if err != nil {
			return "", "", err
		}
		defer t.Close()
		if ok, err := confirmed(t, false, fmt.Sprintf("Owner-sign the certificate of %s (sign %s, reg %s)?", name, keys.GroupFP(c.Body.SignFP()), c.Body.Reg)); err != nil || !ok {
			return "", "", errors.New("declined")
		}
		priv, err := unseal(kenv, t)
		if err != nil {
			return "", "", err
		}
		defer clear(priv)
		return keys.Fingerprint(priv.Public().(ed25519.PublicKey)), keys.SignDoc(keys.TypeDeviceCert, c.Raw, keys.ByOwner, priv).Sig, nil
	}
}
