package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/detach"
	"github.com/mraj602/switchyard/internal/directpush"
	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/paths"
)

// flushArgs are the flags of `sy flush`. The hook starts it as `sy flush --quiet`.
type flushArgs struct {
	quiet    bool
	wait     bool
	force    bool
	fromHook bool // started by `sy hook` (machines.md 5.4: may wait for a direct push)
	timeout  time.Duration
}

// runFlush implements `sy flush [--quiet] [--wait] [--force] [--timeout D]`.
//
// With no hub configured it sends nothing and exits 0, after one cheap, rate-limited run of
// the outbox size cap (without it the outbox would grow for ever; there is no cursor, so no
// pruning). --quiet is
// for the hook and the timer: it prints nothing, always exits 0, and honours a stored
// backoff and a stored "token rejected" state. Run by hand (no --quiet) it ignores both,
// prints what happened and exits 1 if records are still waiting because of an error.
// --wait keeps retrying failed batches with backoff for up to --timeout (default 30 s)
// instead of leaving the retry to the next trigger.
func runFlush(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("flush", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var a flushArgs
	fs.BoolVar(&a.quiet, "quiet", false, "print nothing and always exit 0 (for the hook and the timer)")
	fs.BoolVar(&a.wait, "wait", false, "retry failed batches with backoff until --timeout")
	fs.BoolVar(&a.force, "force", false, "ignore a stored backoff or rejected-token state (implied without --quiet)")
	fs.DurationVar(&a.timeout, "timeout", client.DefaultBudget, "how long one run may take")
	fs.BoolVar(&a.fromHook, "from-hook", false, "internal: started by sy hook; with direct push on, a failed flush waits for a push due within 90 s")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "sy flush: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}

	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return quietErr(a.quiet, err)
	}
	f, err := config.Load(d.Config.Path)
	if err != nil {
		return quietErr(a.quiet, err)
	}
	cfg, err := f.Typed()
	if err != nil {
		return quietErr(a.quiet, err)
	}
	if cfg.Hub.URL == "" {
		capWithoutHub(d, cfg)
		if a.fromHook && cfg.Hub.Reach == config.ReachPull {
			// A pulled machine has nothing to flush; the hub's next pull is its contact.
			hookDirectPush(context.Background(), d, cfg, nil, stderr)
		}
		if !a.quiet {
			fmt.Fprintln(stdout, "No hub configured; nothing to send. Connect one with `sy connect <url> --token <token>`.")
		}
		return nil
	}
	token, err := client.ReadToken(d.State.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("hub %s is configured but there is no token (%s); run `sy connect`", cfg.Hub.URL, filepath.Join(d.State.Path, client.TokenFile))
		}
		return quietErr(a.quiet, err)
	}
	cl, err := client.ForConfig(cfg, token) // hub.dial: a forward's loopback port or socket
	if err != nil {
		return quietErr(a.quiet, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res := flushOnce(ctx, d, cfg, cl, a, stderr)
	if a.fromHook && res.Err != nil {
		retry := func() client.FlushResult {
			b := a
			b.force = true // the failure just stored a backoff
			return flushOnce(ctx, d, cfg, cl, b, stderr)
		}
		hookDirectPush(ctx, d, cfg, retry, stderr)
	}
	if a.quiet {
		return nil
	}
	reportFlush(stdout, res)
	if res.Err != nil {
		return res.Err
	}
	return nil
}

// hookFlushDebounce is internal/hook's flushEvery: the age of flush.stamp below which a
// hook does not start a flush.
const hookFlushDebounce = 2 * time.Second

// directWaitMax is how far ahead a hook-started flush waits for a due direct push
// (machines.md 5.4); later ones are left to `sy tick`.
const directWaitMax = 90 * time.Second

// sleepFor waits d or until ctx ends (a test seam).
var sleepFor = func(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// hookDirectPush is machines.md 5.4 for the flush `sy hook` starts: the flush failed (or
// there is none, in pull mode) and direct push is on. When an episode is due within 90 s it
// waits for it, holding no lock, flushes once more (retry, nil in pull mode), then lets
// internal/directpush decide. Errors go to the log only: this runs detached.
func hookDirectPush(ctx context.Context, d paths.Dirs, cfg config.Config, retry func() client.FlushResult, stderr io.Writer) {
	if !cfg.Push.Direct.Enabled {
		return
	}
	o, err := directEnv{dirs: d, cfg: cfg}.options()
	if err != nil {
		return
	}
	o.Logf = func(format string, args ...any) { fmt.Fprintf(stderr, "sy flush: "+format+"\n", args...) }
	due, ok, err := directpush.NextDue(o)
	if err != nil || !ok {
		return
	}
	wait := time.Until(due)
	if wait > directWaitMax {
		return
	}
	if wait > 0 {
		sleepFor(ctx, wait)
		if ctx.Err() != nil {
			return
		}
	}
	if retry != nil {
		_ = retry()
	}
	if _, err := directpush.Check(ctx, o); err != nil {
		o.Logf("direct push: %v", err)
	}
}

// capEvery is the least time between two size-cap checks of a machine with no hub.
const capEvery = 10 * time.Minute

// capWithoutHub enforces outbox.max_mb when no hub is configured, at most once per
// capEvery (outbox/cap.stamp) and only if no other flusher holds the lock. Errors are
// ignored: this is housekeeping on the hook's background path.
func capWithoutHub(d paths.Dirs, cfg config.Config) {
	if cfg.Outbox.MaxMB <= 0 && cfg.Outbox.KeepDays <= 0 {
		return
	}
	ob := outbox.New(filepath.Join(d.State.Path, "outbox"))
	if _, err := os.Stat(ob.Dir); err != nil {
		return // nothing was ever written
	}
	if !detach.Debounce(filepath.Join(ob.Dir, "cap.stamp"), capEvery, time.Now()) {
		return
	}
	lock, ok, err := ob.TryFlushLock()
	if err != nil || !ok {
		return
	}
	defer lock.Release()
	if cfg.Outbox.MaxMB > 0 {
		_, _ = ob.EnforceCap(int64(cfg.Outbox.MaxMB) << 20)
	}
	if cfg.Outbox.KeepDays > 0 {
		// A pulled machine has no hub URL here and never reaches client.Flush's housekeeping.
		_, _ = ob.Prune(cfg.Outbox.KeepDays)
	}
}

// quietErr turns an error into nil for --quiet runs, which must never fail a caller.
func quietErr(quiet bool, err error) error {
	if quiet {
		return nil
	}
	return err
}

// flushOnce runs the flusher for the folders in d. cl may carry a test HTTP client.
func flushOnce(ctx context.Context, d paths.Dirs, cfg config.Config, cl *client.Client, a flushArgs, stderr io.Writer) client.FlushResult {
	ob := outbox.New(filepath.Join(d.State.Path, "outbox"))
	ob.NoFsync = !cfg.Outbox.Fsync
	o := client.FlushOptions{
		Outbox:   ob,
		Client:   cl,
		Wait:     a.wait,
		Force:    a.force || !a.quiet,
		Budget:   a.timeout,
		CapBytes: int64(cfg.Outbox.MaxMB) << 20,
		KeepDays: cfg.Outbox.KeepDays,
	}
	if a.fromHook {
		// sy hook starts no flush of its own while flush.stamp is younger than this (its
		// flushEvery); this run picks up what those hooks wrote.
		o.Linger = hookFlushDebounce
	}
	if !a.quiet {
		o.Logf = func(format string, args ...any) { fmt.Fprintf(stderr, "sy flush: "+format+"\n", args...) }
	}
	return client.Flush(ctx, o)
}

func reportFlush(w io.Writer, r client.FlushResult) {
	switch {
	case r.Skipped != "":
		fmt.Fprintf(w, "Nothing sent: %s.\n", r.Skipped)
	case r.Err != nil:
		fmt.Fprintf(w, "Sent %d record(s) before the error. Records not answered by the hub stay queued.\n", r.Sent)
	case r.Sent == 0:
		fmt.Fprintln(w, "Nothing to send; the hub has everything.")
	default:
		fmt.Fprintf(w, "Sent %d record(s) in %d batch(es): %d accepted (%d already known), %d rejected.\n",
			r.Sent, r.Batches, r.Accepted, r.Duplicates, r.Rejected)
	}
}
