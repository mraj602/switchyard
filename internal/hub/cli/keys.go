package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"

	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
)

// hubKeyPath is <state>/hub_sign.key (keys.md 3.4).
func hubKeyPath() (string, error) { return hubkeys.DefaultPath() }

// loadOrCreateHubKey reads the hub signing key, making it at first use (`sy hub serve`, the
// first `sy hub pair`).
func loadOrCreateHubKey() (*hubkeys.HubKey, error) {
	p, err := hubKeyPath()
	if err != nil {
		return nil, err
	}
	k, err := hubkeys.LoadOrCreate(p)
	if err != nil {
		return nil, fmt.Errorf("hub signing key: %w", err)
	}
	return k, nil
}

// existingHubKey reads the hub signing key without making one; nil when there is none yet.
func existingHubKey() (*hubkeys.HubKey, error) {
	p, err := hubKeyPath()
	if err != nil {
		return nil, err
	}
	k, err := hubkeys.Load(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hub signing key: %w", err)
	}
	return k, nil
}

// keysService is the key service over st with the hub key, made at first use.
func keysService(st *store.Store, logger *log.Logger) (*hubkeys.Service, error) {
	k, err := loadOrCreateHubKey()
	if err != nil {
		return nil, err
	}
	s := hubkeys.New(st, k)
	s.Log = logger
	return s, nil
}

const keysUsage = `Usage: sy hub keys <command> [flags]

Commands:
  show [--json]          The hub key's fingerprint, the active owner key and pending proposals
  confirm-owner <fp>     Activate a pending owner key proposal (compare the fingerprint
                         with the one sy keys init printed on your terminal)
  register <device> --machine <id> --keys-stdin [--json]
                         Register a machine's link keys read over SSH (reg ssh; used by
                         sy machine add through the admin channel)
  certify <device> --fp <owner fp> --sig-stdin
                         Store an owner signature over a device's certificate (sy machine add)

Every command takes --db <file> (default <data>/hub.db).
`

// Keys is `sy hub keys …` (keys.md 3.3-3.4, 11).
func Keys(args []string, stdout, stderr io.Writer) error {
	return KeysIn(os.Stdin, args, stdout, stderr)
}

// KeysIn is Keys with an explicit stdin (register's keys, certify's signature).
func KeysIn(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, keysUsage)
		if len(args) == 0 {
			return ErrUsage
		}
		return nil
	}
	switch args[0] {
	case "show":
		return keysShow(args[1:], stdout, stderr)
	case "confirm-owner":
		return keysConfirmOwner(args[1:], stdout, stderr)
	case "register":
		return keysRegister(stdin, args[1:], stdout, stderr)
	case "certify":
		return keysCertify(stdin, args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "sy hub keys: unknown command %q\n\n%s", args[0], keysUsage)
	return ErrUsage
}

func keysShow(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("keys show", stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := openHubStore(ctx, *db)
	if err != nil {
		return err
	}
	defer st.Close()
	ks, err := keysService(st, nil)
	if err != nil {
		return err
	}
	owner, err := ks.ActiveOwner(ctx)
	if err != nil {
		return err
	}
	pending, err := ks.PendingOwners(ctx)
	if err != nil {
		return err
	}
	rv := 0
	if rl, err := ks.Revocations(ctx); err != nil {
		return err
	} else if rl != nil {
		rv = rl.Body.Version
	}
	if *asJSON {
		type ok struct {
			FP         string `json:"fp"`
			State      string `json:"state"`
			ProposedBy string `json:"proposed_by,omitempty"`
		}
		out := map[string]any{"hub_key": ks.Key.Public(), "owner_key": nil, "pending": []ok{}, "revocations_version": rv}
		if owner != nil {
			out["owner_key"] = ok{FP: owner.FP, State: owner.State, ProposedBy: owner.ProposedBy}
		}
		var p []ok
		for _, x := range pending {
			p = append(p, ok{FP: x.FP, State: x.State, ProposedBy: x.ProposedBy})
		}
		if p != nil {
			out["pending"] = p
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Fprintf(stdout, "hub key    %s\n", keys.GroupFP(ks.Key.FP))
	if owner != nil {
		fmt.Fprintf(stdout, "owner key  %s (active, proposed by %s)\n", keys.GroupFP(owner.FP), orDash(owner.ProposedBy))
	} else {
		fmt.Fprintln(stdout, "owner key  none (run sy keys init on your main machine)")
	}
	for _, x := range pending {
		fmt.Fprintf(stdout, "pending    %s proposed by %s at %s: sy hub keys confirm-owner %s\n", keys.GroupFP(x.FP), orDash(x.ProposedBy), ms(x.CreatedAt), x.FP)
	}
	fmt.Fprintf(stdout, "revocations version %d\n", rv)
	return nil
}

func keysConfirmOwner(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("keys confirm-owner", stderr)
	if pos, err := parse(fs, args); err != nil || len(pos) != 1 {
		if err == nil {
			fmt.Fprintln(stderr, "usage: sy hub keys confirm-owner <fingerprint>")
		}
		return ErrUsage
	} else {
		args = pos
	}
	fp, ok := keys.NormalizeFP(args[0])
	if !ok {
		return fmt.Errorf("%q is not a key fingerprint (24 characters, as sy keys init printed it)", args[0])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := openHubStore(ctx, *db)
	if err != nil {
		return err
	}
	defer st.Close()
	ks, err := keysService(st, nil)
	if err != nil {
		return err
	}
	o, err := ks.Confirm(ctx, fp, "cli")
	if err != nil {
		var ke *hubkeys.Error
		if errors.As(err, &ke) {
			return errors.New(ke.Msg)
		}
		return err
	}
	fmt.Fprintf(stdout, "owner key %s is active; the hub signed it. Machines pin it at their next pairing or link login; run sy keys certify --all on your main machine.\n", keys.GroupFP(o.FP))
	return nil
}

// keysRegister is `sy hub keys register <device> --machine <id> --keys-stdin [--json]`: the
// hub host's side of `sy machine add` (keys.md 4.2, reg ssh). The keys (api.yaml
// DeviceKeysNew) arrive on stdin from the operator, who read them over its SSH session to
// the machine. The reply carries what the machine then pins: the hub key, the hub-signed
// certificate and the active owner key document.
func keysRegister(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("keys register", stderr)
	machine := fs.String("machine", "", "the machine id the device must belong to")
	fromStdin := fs.Bool("keys-stdin", false, "read the device's public keys (JSON) from stdin")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 || *machine == "" || !*fromStdin {
		if err == nil {
			fmt.Fprintln(stderr, "usage: sy hub keys register <device> --machine <id> --keys-stdin [--json]")
		}
		return ErrUsage
	}
	var k keys.DeviceKeysNew
	if err := json.NewDecoder(io.LimitReader(stdin, 16<<10)).Decode(&k); err != nil {
		return fmt.Errorf("the keys on stdin are not JSON: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := openHubStore(ctx, *db)
	if err != nil {
		return err
	}
	defer st.Close()
	ks, err := keysService(st, nil)
	if err != nil {
		return err
	}
	if _, err := ks.RegisterSSH(ctx, pos[0], *machine, k); err != nil {
		return keysErr(err)
	}
	c, err := ks.Cert(ctx, pos[0])
	if err != nil {
		return keysErr(err)
	}
	owner, err := ks.ActiveOwner(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		out := map[string]any{"device": pos[0], "reg": c.Body.Reg, "cert": c.Doc, "owner_signed": c.OwnerSigned,
			"hub_key": ks.Key.Public(), "owner_key": nil}
		if owner != nil {
			out["owner_key"] = owner.Doc
		}
		return json.NewEncoder(stdout).Encode(out)
	}
	fmt.Fprintf(stdout, "registered link keys of %s (reg %s): sign %s, box %s\n", pos[0], c.Body.Reg, keys.GroupFP(c.Body.SignFP()), keys.GroupFP(c.Body.BoxFP()))
	return nil
}

// keysCertify is `sy hub keys certify <device> --fp <owner fp> --sig-stdin`: an owner
// signature made on the owner machine (sy machine add), checked like POST
// /v1/devices/{id}/cert.
func keysCertify(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("keys certify", stderr)
	fp := fs.String("fp", "", "the owner key's fingerprint")
	fromStdin := fs.Bool("sig-stdin", false, "read the signature (base64url) from stdin")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 || *fp == "" || !*fromStdin {
		if err == nil {
			fmt.Fprintln(stderr, "usage: sy hub keys certify <device> --fp <owner fp> --sig-stdin")
		}
		return ErrUsage
	}
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<10))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := openHubStore(ctx, *db)
	if err != nil {
		return err
	}
	defer st.Close()
	ks, err := keysService(st, nil)
	if err != nil {
		return err
	}
	if err := ks.Certify(ctx, pos[0], *fp, strings.TrimSpace(string(b))); err != nil {
		return keysErr(err)
	}
	fmt.Fprintf(stdout, "certified %s with owner key %s\n", pos[0], keys.GroupFP(*fp))
	return nil
}

func keysErr(err error) error {
	var ke *hubkeys.Error
	if errors.As(err, &ke) {
		return errors.New(ke.Msg)
	}
	return err
}
