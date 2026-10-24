package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/mraj602/watchyard/internal/hub/pair"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/version"
)

// ExitRefused is the exit status of an import into a hub that has data (machines.md 8.2).
// cmd/wy maps a *RefusedError to it.
const ExitRefused = 5

// RefusedError is an import refusal; cmd/wy exits 5 for it.
type RefusedError struct{ Err error }

func (e *RefusedError) Error() string { return e.Err.Error() }
func (e *RefusedError) Unwrap() error { return e.Err }

// stateDir is the state folder (paths.md): <state>/pair_pepper and <state>/pull. It does not
// depend on --db: serve and pair take the pepper from here whatever database they open (the
// service unit passes --db <data>/hub.db, which is not under <state>), so export and import
// must read and write the same files.
func stateDir() (string, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return "", err
	}
	return d.State.Path, nil
}

func dbPathOf(db string) (string, error) {
	if db != "" {
		return db, nil
	}
	return DefaultDB()
}

// readPassphrase takes the first line of file, or prompts on the terminal (twice when confirm).
// A passphrase is never an argument.
func readPassphrase(file string, confirm bool, stderr io.Writer) (string, error) {
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		line, err := bufio.NewReader(f).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return "", fmt.Errorf("%s: the first line is empty", file)
		}
		return line, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("no terminal to ask for a passphrase: use --passphrase-file")
	}
	fmt.Fprint(stderr, "Passphrase: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	if len(a) == 0 {
		return "", errors.New("the passphrase is empty")
	}
	if confirm {
		fmt.Fprint(stderr, "Again: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		if string(a) != string(b) {
			return "", errors.New("the passphrases differ")
		}
	}
	return string(a), nil
}

// Export is `wy hub export [--out f] [--moved-to url] [--no-freeze] [--encrypt]
// [--passphrase-file f] [--json]` (machines.md 8.1).
func Export(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("export", stderr)
	out := fs.String("out", "", "archive file (default <data>/exports/hub-<UTC time>.syx)")
	movedTo := fs.String("moved-to", "", "the new hub's address, recorded in the freeze")
	noFreeze := fs.Bool("no-freeze", false, "copy only: do not freeze the hub (not a move)")
	encrypt := fs.Bool("encrypt", false, "encrypt the archive with a passphrase")
	passFile := fs.String("passphrase-file", "", "read the passphrase from the first line of this file (implies --encrypt)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	pullDirFlag := fs.String("pull-dir", "", "the pull key folder (default <state>/pull; the one wy hub serve --pull-dir uses)")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	if *movedTo != "" {
		origin, _, err := pair.NormalizeURL(*movedTo, "machine", true)
		if err != nil {
			return fmt.Errorf("--moved-to: %w", err)
		}
		*movedTo = origin
	}
	pass := ""
	if *encrypt || *passFile != "" {
		p, err := readPassphrase(*passFile, true, stderr)
		if err != nil {
			return err
		}
		pass = p
	}
	// Ctrl-C or SIGTERM cancels the export; Store.Export then unfreezes the hub it froze and
	// removes the plaintext snapshot before it returns.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dbp, err := dbPathOf(*db)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dbp); err != nil {
		return fmt.Errorf("no hub database at %s", dbp)
	}
	st, err := store.Open(ctx, dbp, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	target := *out
	if target == "" {
		target = filepath.Join(filepath.Dir(dbp), "exports", "hub-"+time.Now().UTC().Format("20060102T150405Z")+".syx")
	}
	sd, err := stateDir()
	if err != nil {
		return err
	}
	pd := *pullDirFlag
	if pd == "" {
		pd = filepath.Join(sd, "pull")
	}
	res, err := st.Export(ctx, store.ExportOptions{Out: target, StateDir: sd, PullDir: pd, MovedTo: *movedTo, NoFreeze: *noFreeze,
		Passphrase: pass, SyVersion: version.Version})
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "export interrupted: the snapshot was removed and a hub this export froze was unfrozen.")
		}
		if !*noFreeze {
			// Whatever went wrong, say how to get out if the hub is still frozen from this run.
			if fr, ferr := st.Frozen(context.WithoutCancel(ctx)); ferr != nil || fr != nil {
				fmt.Fprintln(stderr, "if the hub is still frozen and should not be, run: wy hub unfreeze")
			}
		}
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{"path": res.Path, "encrypted": res.Encrypted,
			"frozen": res.Frozen, "size": res.Size, "manifest": res.Manifest})
	}
	c := res.Manifest.Counts
	fmt.Fprintf(stdout, "wrote %s (%d bytes%s)\n", res.Path, res.Size, map[bool]string{true: ", encrypted"}[res.Encrypted])
	fmt.Fprintf(stdout, "holds %d devices, %d machines, %d records, %d sessions, %d push targets, %d pull targets (schema v%d)\n",
		c.Devices, c.Machines, c.Records, c.Sessions, c.PushTargets, c.PullTargets, res.Manifest.Schema)
	if res.Frozen {
		fmt.Fprintln(stdout, "this hub is now frozen: it answers 503 to machines, which keep their records and retry.")
		fmt.Fprintln(stdout, "call the move off with: wy hub unfreeze")
	}
	fmt.Fprintln(stdout, "This file holds every device's token hash, the VAPID private key, ntfy access tokens, the pairing pepper and the pull key.")
	fmt.Fprintln(stdout, "Anyone with it can impersonate this hub to your phones. Keep it like a password; delete it after the import.")
	return nil
}

// Import is `wy hub import <file> [--public-url url] [--passphrase-file f] [--yes] [--json]`
// (machines.md 8.2).
func Import(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("import", stderr)
	publicURL := fs.String("public-url", "", "this hub's public address (default: keep the exported one)")
	passFile := fs.String("passphrase-file", "", "read the passphrase from the first line of this file")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	listen := fs.String("listen", "127.0.0.1:8787", "address a running hub would answer on (refuse when it does)")
	pullDirFlag := fs.String("pull-dir", "", "the pull key folder (default <state>/pull; the one wy hub serve --pull-dir uses)")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return ErrUsage
	}
	file := pos[0]
	if _, err := os.Stat(file); err != nil {
		return err
	}
	pub := ""
	if *publicURL != "" {
		origin, _, err := pair.NormalizeURL(*publicURL, "machine", true)
		if err != nil {
			return fmt.Errorf("--public-url: %w", err)
		}
		pub = origin
	}
	dbp, err := dbPathOf(*db)
	if err != nil {
		return err
	}
	sd, err := stateDir()
	if err != nil {
		return err
	}
	// 8.2 step 1: a hub answering on its listen address means "stop the hub first".
	if hubAnswers(*listen) {
		return fmt.Errorf("a hub answers on %s: stop the hub first", *listen)
	}
	pass := ""
	if enc, _ := store.IsEncryptedExport(file); enc {
		if pass, err = readPassphrase(*passFile, false, stderr); err != nil {
			return err
		}
	}
	if !*yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("import into %s needs confirmation: pass --yes", dbp)
		}
		fmt.Fprintf(stderr, "Import %s into %s? [y/N] ", file, dbp)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not imported")
		}
	}
	logger := func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) }
	ictx, istop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer istop()
	res, err := store.Import(ictx, store.ImportOptions{File: file, Passphrase: pass, DBPath: dbp, StateDir: sd, PullDir: *pullDirFlag,
		PublicURL: pub, SyVersion: version.Version, Log: logger})
	var ne *store.NonEmptyError
	if errors.As(err, &ne) || errors.Is(err, store.ErrHubRunning) {
		return &RefusedError{Err: err}
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{"db": dbp, "public_url": res.PublicURL,
			"url_changed": res.URLChanged, "backups": res.Backups, "manifest": res.Manifest})
	}
	c := res.Manifest.Counts
	fmt.Fprintf(stdout, "imported %d devices, %d machines, %d records, %d push targets, %d pull targets into %s\n",
		c.Devices, c.Machines, c.Records, c.PushTargets, c.PullTargets, dbp)
	for _, b := range res.Backups {
		fmt.Fprintf(stdout, "kept the previous file as %s\n", b)
	}
	fmt.Fprintf(stdout, "public address: %s\n", orNone(res.PublicURL))
	if !res.URLChanged {
		fmt.Fprintln(stdout, "  (kept from the export; pass --public-url if this hub lives at another address)")
	}
	fmt.Fprint(stdout, `Next:
  1. Start this hub: wy hub serve
  2. Machine and desktop tokens keep working. On each machine run
       wy connect <new url> --keep-token
     (or, from the laptop, wy machine repoint <new url>). Until then their records wait,
     because the old hub is frozen.
  3. Pull targets keep working if this host can reach the remotes over SSH.
  4. Phones and browsers: re-pair, unless this hub has the same public address as the old one.
     Sign in to the board again with wy hub owner-link.
  5. Delete the export file when you are done: it holds secrets.
`)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none set)"
	}
	return s
}

// hubAnswers reports whether something answers /health on addr.
func hubAnswers(addr string) bool {
	c := &http.Client{Timeout: time.Second}
	resp, err := c.Get("http://" + addr + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Unfreeze is `wy hub unfreeze` (machines.md 8.1): clears hub.frozen.
func Unfreeze(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("unfreeze", stderr)
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openStore(ctx, *db, nil)
	if err != nil {
		return err
	}
	defer st.Close()
	was, err := st.Unfreeze(ctx)
	if err != nil {
		return err
	}
	if !was {
		fmt.Fprintln(stdout, "the hub was not frozen")
		return nil
	}
	fmt.Fprintln(stdout, "unfrozen: the hub accepts records again")
	return nil
}
