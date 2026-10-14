package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/keys"
)

// fakeTTY answers prompts from a script and records what was printed to the terminal.
type fakeTTY struct {
	t       *testing.T
	answers []string
	printed strings.Builder
	prompts []string
}

func (f *fakeTTY) next(prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	if len(f.answers) == 0 {
		f.t.Errorf("unexpected prompt %q", prompt)
		return "", errors.New("no answer")
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	if a == "<code-groups>" { // the last two groups of the code printed so far
		m := regexp.MustCompile(`SYR1-[A-Z0-9-]+`).FindString(f.printed.String())
		g := strings.Split(m, "-")
		a = strings.Join(g[len(g)-2:], "-")
	}
	return a, nil
}
func (f *fakeTTY) Password(p string) (string, error) { return f.next(p) }
func (f *fakeTTY) Line(p string) (string, error)     { return f.next(p) }
func (f *fakeTTY) Print(s string)                    { f.printed.WriteString(s) }
func (f *fakeTTY) Close() error                      { return nil }

type keysRig struct {
	t      *testing.T
	env    keysEnv
	tty    *fakeTTY
	out    *bytes.Buffer
	ks     *hubkeys.Service
	ps     *pair.Service
	hubURL string
	token  string
	device string
}

const pass1 = "correct horse battery staple"

// newKeysRig is a machine connected with a typed code to a real hub with a signing key.
func newKeysRig(t *testing.T) *keysRig {
	t.Helper()
	deps, _, _ := sandbox(t)
	hs, ps, ks := keyHub(t)
	var out, errb bytes.Buffer
	if err := connect(context.Background(), deps, []string{hs.URL, "--code", machineCode(t, ps, hs.URL), "--no-timer"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	tok, err := client.ReadToken(deps.dirs.State.Path)
	if err != nil {
		t.Fatal(err)
	}
	dk, _ := keys.ReadDeviceKeys(deps.dirs.State.Path)
	r := &keysRig{t: t, tty: &fakeTTY{t: t}, out: &bytes.Buffer{}, ks: ks, ps: ps, hubURL: hs.URL, token: tok, device: dk.Device}
	r.env = keysEnv{
		state: deps.dirs.State.Path, stdout: r.out, stderr: r.out, stdoutTTY: true,
		guard:  func() error { return nil },
		tty:    func() (keysTTY, error) { return r.tty, nil },
		hub:    func() (*client.Client, error) { return client.New(hs.URL, tok), nil },
		merged: func(context.Context) (*hubkeys.Service, func(), error) { return nil, nil, nil },
		now:    time.Now, rand: rand.Reader, iter: 1000,
	}
	return r
}

func (r *keysRig) run(answers []string, args ...string) error {
	r.t.Helper()
	r.tty.answers = answers
	r.out.Reset()
	r.tty.printed.Reset()
	err := keysMain(context.Background(), r.env, args)
	if len(r.tty.answers) != 0 {
		r.t.Errorf("%v: unused answers %q", args, r.tty.answers)
	}
	return err
}

func (r *keysRig) initOwner() string {
	r.t.Helper()
	if err := r.run([]string{pass1, pass1, "<code-groups>"}, "init", "--yes"); err != nil {
		r.t.Fatal(err, r.out.String())
	}
	p, err := keys.ReadPin(keys.Path(r.env.state, keys.OwnerPinFile))
	if err != nil || p.How != keys.PinLocal {
		r.t.Fatalf("owner pin %+v %v", p, err)
	}
	return p.FP
}

func TestKeysGuardRefusesWithExit4(t *testing.T) {
	r := newKeysRig(t)
	r.env.guard = func() error { return &keys.GuardError{PID: 4242, Name: "claude"} }
	r.env.tty = func() (keysTTY, error) { t.Fatal("the terminal was opened"); return nil, nil }
	for _, c := range []string{"init", "certify", "revoke", "passwd", "recover", "trust-owner"} {
		err := keysMain(context.Background(), r.env, []string{c, "--all"})
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != 4 || !errors.Is(err, keys.ErrGuard) {
			t.Errorf("%s: %v", c, err)
		}
	}
	// status and show-owner do not sign: no guard.
	if err := keysMain(context.Background(), r.env, []string{"status"}); err != nil {
		t.Errorf("status: %v", err)
	}
}

func TestKeysInitCertifyPasswdRevoke(t *testing.T) {
	r := newKeysRig(t)
	ctx := context.Background()

	// Short passphrase three times: nothing is made.
	if err := r.run([]string{"short", "short", "short"}, "init", "--yes"); err == nil {
		t.Fatal("short passphrase accepted")
	}
	if _, err := os.Stat(keys.Path(r.env.state, keys.OwnerSealedFile)); !os.IsNotExist(err) {
		t.Fatal("owner.sealed written without a passphrase")
	}
	// A wrong typed-back code writes nothing either.
	if err := r.run([]string{pass1, pass1, "AAAA-BBBB", "AAAA-BBBB", "AAAA-BBBB"}, "init", "--yes"); err == nil {
		t.Fatal("wrong typed-back code accepted")
	}
	// init: the recovery code goes to the terminal only, the proposal is pending.
	fp := r.initOwner()
	if !strings.Contains(r.tty.printed.String(), "SYR1-") || strings.Contains(r.out.String(), "SYR1-") {
		t.Fatal("the recovery code must be printed on the terminal only")
	}
	if !strings.Contains(r.out.String(), "confirm-owner "+fp) {
		t.Fatalf("init output: %s", r.out.String())
	}
	// init needs a terminal as stdout.
	r.env.stdoutTTY = false
	if err := r.run(nil, "init"); err == nil {
		t.Fatal("init without a terminal")
	}
	r.env.stdoutTTY = true

	// certify refuses until the hub's owner key is this one.
	if err := r.run([]string{pass1}, "certify", "--all", "--yes"); err == nil || !strings.Contains(err.Error(), "active owner key") {
		t.Fatalf("certify before confirm: %v", err)
	}
	if _, err := r.ks.Confirm(ctx, fp, "cli"); err != nil {
		t.Fatal(err)
	}
	// Wrong passphrase.
	if err := r.run([]string{"wrong passphrase here"}, "certify", "--all", "--yes"); !errors.Is(err, keys.ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if err := r.run([]string{pass1}, "certify", "--all", "--yes"); err != nil {
		t.Fatal(err, r.out.String())
	}
	if c, err := r.ks.Cert(ctx, r.device); err != nil || !c.OwnerSigned {
		t.Fatalf("not certified: %v", err)
	}
	if err := r.run(nil, "certify", "--all", "--yes"); err != nil || !strings.Contains(r.out.String(), "Nothing to certify") {
		t.Fatalf("second certify: %v %s", err, r.out.String())
	}

	// passwd: the old passphrase no longer opens the file.
	pass2 := "a different long passphrase"
	if err := r.run([]string{pass1, pass2, pass2}, "passwd"); err != nil {
		t.Fatal(err)
	}
	so, _ := keys.ReadSealedOwner(keys.Path(r.env.state, keys.OwnerSealedFile))
	if _, err := so.Open(pass1); err == nil {
		t.Fatal("old passphrase still opens owner.sealed")
	}
	if _, err := so.Open(pass2); err != nil {
		t.Fatal(err)
	}

	// revoke a second machine with an owner-signed list.
	other := pairMachine(t, r)
	if err := r.run([]string{pass2}, "revoke", other, "--reason", "lost", "--yes"); err != nil {
		t.Fatal(err, r.out.String())
	}
	rl, err := r.ks.Revocations(ctx)
	if err != nil || rl == nil || rl.Signer != keys.ByOwner || !rl.Body.RevokedDevice(other) {
		t.Fatalf("revocations %+v %v", rl, err)
	}
	if err := r.run(nil, "revoke", other, "--yes"); err != nil || !strings.Contains(r.out.String(), "already revoked") {
		t.Fatalf("revoke twice: %v %s", err, r.out.String())
	}
}

// pairMachine pairs another machine over the API with a typed code and returns its id.
func pairMachine(t *testing.T, r *keysRig) string {
	t.Helper()
	code := machineCode(t, r.ps, r.hubURL)
	dk, _ := keys.NewDeviceKeys(time.Now(), rand.Reader)
	pub, _ := dk.Public()
	rep, err := client.Claim(context.Background(), r.hubURL, nil, client.ClaimRequest{Code: code, Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4Q", ExpectKind: "machine", Name: "other", Keys: &pub})
	if err != nil {
		t.Fatal(err)
	}
	return rep.Device
}

func TestKeysRecoverTrustOwnerAndMerged(t *testing.T) {
	r := newKeysRig(t)
	ctx := context.Background()
	fp := r.initOwner()
	code := regexp.MustCompile(`SYR1-[A-Z0-9-]+`).FindString(r.tty.printed.String())
	if code == "" {
		t.Fatal("no code printed")
	}
	// Lose owner.sealed; recover with the code (lower case, spaces) gives the same key.
	os.Remove(keys.Path(r.env.state, keys.OwnerSealedFile))
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	if err := r.run([]string{typed, pass1, pass1}, "recover"); err != nil {
		t.Fatal(err)
	}
	so, _ := keys.ReadSealedOwner(keys.Path(r.env.state, keys.OwnerSealedFile))
	if so.FP != fp {
		t.Fatalf("recovered %s, want %s", so.FP, fp)
	}
	// A code for another key does not overwrite: this machine pins the first.
	var other [32]byte
	other[0] = 7
	if err := r.run([]string{keys.RecoveryCode(other)}, "recover"); err == nil || !strings.Contains(err.Error(), "pins") {
		t.Fatalf("other code: %v", err)
	}
	if err := r.run([]string{"SYR1-NOPE"}, "recover"); err == nil {
		t.Fatal("garbage code accepted")
	}

	// trust-owner: replace a wrong pin with the hub's confirmed key, by hand.
	if _, err := r.ks.Confirm(ctx, fp, "cli"); err != nil {
		t.Fatal(err)
	}
	wrong := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, 32)).Public().(ed25519.PublicKey)
	keys.ReplacePin(keys.Path(r.env.state, keys.OwnerPinFile), keys.NewPin(wrong, keys.PinTOFU, time.Now()))
	if err := r.run([]string{"n"}, "trust-owner", fp); err == nil {
		t.Fatal("pin replaced without a yes")
	}
	if err := r.run(nil, "trust-owner", keys.Fingerprint(wrong)); err == nil {
		t.Fatal("trusted a key the hub does not have")
	}
	if err := r.run([]string{"y"}, "trust-owner", keys.GroupFP(fp)); err != nil {
		t.Fatal(err)
	}
	if p, _ := keys.ReadPin(keys.Path(r.env.state, keys.OwnerPinFile)); p.FP != fp || p.How != keys.PinHand {
		t.Fatalf("pin %+v", p)
	}
	if !strings.Contains(r.out.String(), "old "+keys.GroupFP(keys.Fingerprint(wrong))) {
		t.Fatalf("old fingerprint not shown: %s", r.out.String())
	}

	// show-owner and status.
	if err := r.run(nil, "show-owner", "--json"); err != nil || !strings.Contains(r.out.String(), fp) {
		t.Fatalf("show-owner: %v %s", err, r.out.String())
	}
	if err := r.run(nil, "status"); err != nil || !strings.Contains(r.out.String(), keys.GroupFP(fp)) {
		t.Fatalf("status: %v %s", err, r.out.String())
	}

	// Merged mode: a machine whose state folder holds the hub key activates at once.
	m := newKeysRig(t)
	m.env.merged = func(context.Context) (*hubkeys.Service, func(), error) { return m.ks, func() {}, nil }
	mfp := m.initOwner()
	if o, _ := m.ks.ActiveOwner(ctx); o == nil || o.FP != mfp {
		t.Fatal("merged init did not activate the key")
	}
}
