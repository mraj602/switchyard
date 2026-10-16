package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/service"
)

// ErrExpired is returned by `sy hub pair --wait` when the code ended (expired or cancelled)
// before anyone claimed it. The message has already been printed; the command exits 3.
var ErrExpired = errors.New("the pairing code expired before it was claimed")

// pairPollEvery is how often --wait looks at the code (a variable so tests can speed it up).
var pairPollEvery = time.Second

// PairUsage is the help of `sy hub pair`.
const PairUsage = `Usage: sy hub pair --kind phone|browser|machine|desktop [flags]

Makes a one-time pairing code (10 minutes, single use) on this hub and shows it with a QR.

  --kind K      what is being paired (required)
  --name N      the device name (wins over the name the claimer sends)
  --machine ID  machine and desktop codes: the machine id (optional; sy machine prints it)
  --url U       the hub address the phone opens (default: the hub.public_url setting)
  --ttl D       lifetime, 1m to 1h (default 10m)
  --wait        wait until the code is claimed (exit 3 if it expires first)
  --json        print JSON instead of text
  --qr          draw the QR even when stdout is not a terminal
  --insecure    allow an http:// address for a phone (it pairs, but cannot install or get pushes)
  --db FILE     hub database (default <data>/hub.db)
`

// Pair is `sy hub pair`: it makes a pairing code directly in the hub's database (pairing.md
// 2.4), so it must run on the hub machine (in Docker: docker compose exec hub sy hub pair).
func Pair(args []string, stdout, stderr io.Writer) error {
	return pairCmd("hub pair", "", false, args, stdout, stderr)
}

// PhonePair is `sy phone pair`: `sy hub pair --kind phone --wait`.
func PhonePair(args []string, stdout, stderr io.Writer) error {
	return pairCmd("phone pair", "phone", true, args, stdout, stderr)
}

func pairCmd(name, kind string, wait bool, args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags(name, stderr)
	fs.Usage = func() { fmt.Fprint(stderr, PairUsage) }
	if kind == "" {
		fs.StringVar(&kind, "kind", "", "phone, browser, machine or desktop")
		fs.BoolVar(&wait, "wait", false, "wait until the code is claimed")
	}
	pname := fs.String("name", "", "the device name")
	machine := fs.String("machine", "", "machine id")
	url := fs.String("url", "", "hub address")
	ttl := fs.Duration("ttl", 0, "lifetime")
	asJSON := fs.Bool("json", false, "print JSON")
	forceQR := fs.Bool("qr", false, "draw the QR code even when stdout is not a terminal")
	insecure := fs.Bool("insecure", false, "allow http for a phone")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		if err == nil {
			fmt.Fprintf(stderr, "sy %s: unexpected argument %q\n", name, pos[0])
		}
		return ErrUsage
	}
	if kind == "" {
		fmt.Fprintf(stderr, "sy %s: --kind is required (phone, browser, machine or desktop)\n", name)
		return ErrUsage
	}
	if *ttl != 0 && (*ttl < pair.MinTTL || *ttl > pair.MaxTTL) {
		fmt.Fprintf(stderr, "sy %s: --ttl must be between 1m and 1h\n", name)
		return ErrUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := openHubStore(ctx, *db)
	if err != nil {
		return err
	}
	defer st.Close()
	setting, _, _ := st.PairSetting(ctx, "hub.public_url")
	hub, err := pair.ResolveURL(*url, setting, "")
	if err != nil {
		return err
	}
	svc := pair.NewService(st, "")
	// The QR carries the hub key's fingerprint and the seal key (keys.md 7); the first
	// `sy hub pair` makes the hub key if serve has not yet.
	if svc.Keys, err = keysService(st, nil); err != nil {
		return err
	}
	c, err := svc.Create(ctx, pair.CreateRequest{Kind: kind, Name: *pname, Machine: *machine, URL: hub, TTL: *ttl,
		CreatedBy: "cli", Insecure: *insecure})
	if err != nil {
		var ce *pair.ClaimError
		if errors.As(err, &ce) {
			return errors.New(ce.Message)
		}
		return err
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(pairJSON(c)); err != nil {
			return err
		}
	} else {
		printCode(stdout, c, *forceQR || wantQR(stdout))
	}
	if !wait {
		return nil
	}
	return waitClaimed(ctx, svc, c, *asJSON, stdout)
}

// pairJSON is api.yaml PairCode.
func pairJSON(c pair.Code) map[string]any {
	var name, command any
	if c.Name != "" {
		name = c.Name
	}
	if c.Command != "" {
		command = c.Command
	}
	return map[string]any{"id": c.ID, "kind": c.Kind, "name": name, "code": c.Code, "secret": c.Secret, "url": c.URL,
		"expires_at": store.Time(c.ExpiresAt.UnixMilli()), "command": command, "qr": c.QR}
}

// wantQR: draw the QR only on a terminal that shows colour (pairing.md 2.2).
func wantQR(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func printCode(w io.Writer, c pair.Code, qr bool) {
	until := c.ExpiresAt.Local().Format("15:04")
	switch c.Kind {
	case "machine", "desktop":
		fmt.Fprintf(w, "On the machine to connect, run:\n\n  %s\n\nThe code works once, for %s (until %s).\n", c.Command,
			time.Until(c.ExpiresAt).Round(time.Minute), until)
		return
	}
	if qr {
		fmt.Fprint(w, "Scan with your phone's camera:\n\n")
		for _, ln := range strings.SplitAfter(c.QR.Terminal(), "\n") {
			if ln != "" {
				fmt.Fprint(w, "  "+ln)
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "  or open  %s  and enter  %s\n", c.Hub, c.Code)
	fmt.Fprintf(w, "  The code works once, for %s (until %s).\n", time.Until(c.ExpiresAt).Round(time.Minute), until)
	if !qr {
		fmt.Fprintf(w, "  Link: %s\n", c.URL)
	}
	if c.PlainHTTP {
		fmt.Fprintf(w, "  Note: this address is plain http, so the app can pair but cannot be installed or receive pushes.\n  For the full app, %s.\n", service.HTTPSFixFor(c.Hub))
	}
	fmt.Fprintln(w)
}

func waitClaimed(ctx context.Context, svc *pair.Service, c pair.Code, asJSON bool, stdout io.Writer) error {
	if !asJSON {
		fmt.Fprintln(stdout, "Waiting for the phone… (Ctrl-C stops waiting; the code stays valid)")
	}
	tick := time.NewTicker(pairPollEvery)
	defer tick.Stop()
	for {
		st, err := svc.Status(context.Background(), c.ID)
		if err != nil {
			return err
		}
		switch st.State {
		case pair.StatusClaimed:
			return printClaimed(stdout, st, asJSON)
		case pair.StatusExpired, pair.StatusCancelled:
			if asJSON {
				_ = json.NewEncoder(stdout).Encode(map[string]any{"claimed": false, "status": st.State})
			} else {
				fmt.Fprintf(stdout, "The code %s without being claimed. Make a new one.\n", map[string]string{"expired": "expired", "cancelled": "was cancelled"}[st.State])
			}
			return ErrExpired
		}
		select {
		case <-ctx.Done():
			if !asJSON {
				fmt.Fprintf(stdout, "\nStopped waiting; the code stays valid until %s.\n", c.ExpiresAt.Local().Format("15:04"))
			}
			return nil
		case <-tick.C:
		}
	}
}

func printClaimed(w io.Writer, st pair.Status, asJSON bool) error {
	d := st.Device
	if asJSON {
		out := map[string]any{"claimed": true, "device": nil}
		if d != nil {
			out["device"] = map[string]any{"id": d.ID, "kind": d.Kind, "name": d.Name, "scopes": d.Scopes}
		}
		return json.NewEncoder(w).Encode(out)
	}
	if d == nil {
		fmt.Fprintln(w, "Paired.")
		return nil
	}
	fmt.Fprintf(w, "Paired: %s (%s). It can %s; revoke it with `sy hub token revoke %s` or from the board's Devices list.\n",
		d.Name, d.ID, abilities(d.Scopes), d.ID)
	return nil
}

func abilities(scopes []string) string {
	has := func(s string) bool {
		for _, v := range scopes {
			if v == s {
				return true
			}
		}
		return false
	}
	var out []string
	if has("write") {
		out = append(out, "upload records")
	}
	if has("read") {
		out = append(out, "read")
	}
	if has("push") {
		out = append(out, "receive pushes")
	}
	if len(out) == 0 {
		return "connect"
	}
	return strings.Join(out, " and ")
}

// OwnerLinkUsage is the help of `sy hub owner-link`.
const OwnerLinkUsage = `Usage: sy hub owner-link [--url U] [--ttl D] [--json] [--insecure]

Makes a one-time sign-in link for the board's owner settings (pairing phones, devices,
rules). Open it in a browser; it works once, for 10 minutes, and signs that browser in as
the owner for 30 days.
`

// OwnerLink is `sy hub owner-link` (pairing.md 4).
func OwnerLink(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("hub owner-link", stderr)
	fs.Usage = func() { fmt.Fprint(stderr, OwnerLinkUsage) }
	url := fs.String("url", "", "hub address")
	ttl := fs.Duration("ttl", 0, "lifetime")
	asJSON := fs.Bool("json", false, "print JSON")
	insecure := fs.Bool("insecure", false, "allow an http address that is not loopback")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		if err == nil {
			fmt.Fprintf(stderr, "sy hub owner-link: unexpected argument %q\n", pos[0])
		}
		return ErrUsage
	}
	if *ttl != 0 && (*ttl < pair.MinTTL || *ttl > pair.MaxTTL) {
		fmt.Fprintln(stderr, "sy hub owner-link: --ttl must be between 1m and 1h")
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openHubStore(ctx, *db)
	if err != nil {
		return err
	}
	defer st.Close()
	setting, _, _ := st.PairSetting(ctx, "hub.public_url")
	hub, err := pair.ResolveURL(*url, setting, "")
	if err != nil {
		return err
	}
	l, err := pair.NewService(st, "").CreateOwnerLink(ctx, pair.OwnerLinkRequest{URL: hub, TTL: *ttl, CreatedBy: "cli", Insecure: *insecure})
	if err != nil {
		var ce *pair.ClaimError
		if errors.As(err, &ce) {
			return errors.New(ce.Message)
		}
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{"id": l.ID, "url": l.URL, "secret": l.Secret,
			"expires_at": store.Time(l.ExpiresAt.UnixMilli())})
	}
	fmt.Fprintf(stdout, "Open this link in the browser to sign in as the owner (works once, until %s):\n\n  %s\n\n",
		l.ExpiresAt.Local().Format("15:04"), l.URL)
	return nil
}

// openHubStore opens the hub database for a pairing command: it must already exist, or
// this is not the hub machine.
func openHubStore(ctx context.Context, db string) (*store.Store, error) {
	path := db
	if path == "" {
		p, err := DefaultDB()
		if err != nil {
			return nil, err
		}
		path = p
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("there is no hub database at %s: run this on the hub machine (in Docker: docker compose exec hub sy hub pair ...), "+
				"or open the board, Settings, Add phone", path)
		}
		return nil, err
	}
	return openStore(ctx, path, nil)
}
