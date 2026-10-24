package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/directpush"
)

// Machine checks: how records reach the hub, held records, a frozen hub,
// direct push, the forward port, and on the hub its pull targets and pull key.

// policyStale is how old the cached direct-push policy may get before doctor warns (wy tick
// fetches it hourly).
const policyStale = 24 * time.Hour

// Pull target states, copied from internal/hub/store (pull.go): doctor reads the raw rows.
const (
	pullStateNew      = "new"
	pullStateOK       = "ok"
	pullStateFailing  = "failing"
	pullStateHostKey  = "host_key"
	pullStateAuth     = "auth"
	pullStateRefused  = "refused"
	pullStateDisabled = "disabled"
	pullKeyFile       = "id_ed25519" // internal/hub/pull KeyFile, in <state>/pull
)

// checkMachine adds the client-side machine checks for this machine's configuration.
func checkMachine(ctx context.Context, rep *Report, o Options, cfg config.Config) {
	dir := filepath.Join(o.Dirs.State.Path, "outbox")
	reach := cfg.Hub.Reach
	if reach == "" {
		reach = config.ReachDirect
	}
	if cfg.Hub.URL == "" && reach != config.ReachPull {
		return
	}
	var st client.Status
	if _, err := os.Stat(filepath.Join(dir, client.StatusFile)); err == nil {
		st = client.ReadStatus(dir)
	}
	checkReach(rep, o, cfg, reach, dir, st)
	checkHeld(rep, st)
	checkFrozen(rep, o, cfg)
	checkDirectPush(rep, o, cfg)
	if reach == config.ReachForward {
		checkForwardPort(ctx, rep, cfg)
	}
}

// contactText is "last contact 14:02 (5m ago)" or "no contact yet".
func contactText(o Options, word string, t time.Time) string {
	if t.IsZero() {
		return "no " + word + " yet"
	}
	return fmt.Sprintf("last %s %s (%s ago)", word, t.Local().Format("15:04"), age(o.now().Sub(t)))
}

// checkReach is the reach line (machines.md 3.3): "direct", "forward via laptop SSH, last
// contact …" or "pulled by hub, last pull …".
func checkReach(rep *Report, o Options, cfg config.Config, reach, dir string, st client.Status) {
	last := directpush.LastContact(dir)
	switch reach {
	case config.ReachPull:
		c := Check{Name: "reach", Level: OK, Detail: "pulled by the hub over SSH; " + contactText(o, "pull", last)}
		if st.PullRefused != "" {
			c.Level = Warn
			c.Detail += "; the last pull was refused: " + st.PullRefused
			c.Fix = "update wy on the hub or on this machine so both speak the same drain protocol: wy update"
		}
		rep.add(c)
	case config.ReachForward:
		rep.add(Check{Name: "reach", Level: OK,
			Detail: "hub reached through an SSH forward from the laptop; " + contactText(o, "contact", last)})
	default:
		d := "direct to " + cfg.Hub.URL
		if cfg.Hub.Dial != "" {
			d += " (dialed through " + cfg.Hub.Dial + ")"
		}
		rep.add(Check{Name: "reach", Level: OK, Detail: d + "; " + contactText(o, "contact", last)})
	}
}

// checkHeld: records kept back for a hub that cannot read them yet (machines.md 7.2).
func checkHeld(rep *Report, st client.Status) {
	if st.Held <= 0 && st.HubTooOld == nil {
		return
	}
	d := fmt.Sprintf("%d record(s) held back", st.Held)
	if h := st.HubTooOld; h != nil {
		hub := "the hub"
		if h.Hub != "" {
			hub = "the hub (" + h.Hub + ")"
		}
		d = fmt.Sprintf("%s reads records up to v%d, this machine writes v%d: %d record(s) held back", hub, h.RecordVMax, h.Needs, st.Held)
	}
	rep.add(Check{Name: "held records", Level: Warn, Detail: d,
		Fix: "update the hub (wy update on it, or wy update --machines hub); nothing is lost meanwhile"})
}

// checkFrozen: the hub was frozen for a move (machines.md 8.3). The client never follows
// moved_to by itself; doctor prints the exact command.
func checkFrozen(rep *Report, o Options, cfg config.Config) {
	c, ok := client.ReadHubCache(o.Dirs.State.Path)
	if !ok || c.Frozen == nil || (cfg.Hub.URL != "" && c.URL != cfg.Hub.URL) {
		return
	}
	d := "the hub is frozen for a move"
	if c.Frozen.Since != "" {
		d += " since " + c.Frozen.Since
	}
	fix := "the hub moved without a new address; ask its owner where it went, then run: wy connect <new hub url> --keep-token"
	if m := c.Frozen.MovedTo; m != nil && *m != "" {
		d += "; it moved to " + *m
		fix = "wy connect " + *m + " --keep-token"
	}
	rep.add(Check{Name: "hub frozen", Level: Fail, Detail: d + "; records queue here until this machine points at the new hub", Fix: fix})
}

// checkDirectPush: direct ntfy from this machine (machines.md 5): the topic, and the age of
// the policy cache wy tick fetches hourly.
func checkDirectPush(rep *Report, o Options, cfg config.Config) {
	if !cfg.Push.Direct.Enabled {
		return
	}
	state := o.Dirs.State.Path
	if _, err := directpush.LoadSecret(state); err != nil {
		rep.add(Check{Name: "direct push", Level: Warn, Detail: "on, but without a topic: " + err.Error(),
			Fix: "set the topic: wy direct-push on --secret-stdin"})
		return
	}
	server := directpush.ServerHost(cfg.Push.Direct.Server)
	p, ok := directpush.LoadPolicy(state)
	at, err := time.Parse(time.RFC3339, p.FetchedAt)
	switch {
	case !ok:
		rep.add(Check{Name: "direct push", Level: Warn, Detail: "on (" + server + "), but no policy from the hub yet: nothing is sent",
			Fix: "wy direct-push policy --fetch (wy tick fetches it hourly once the hub is reachable)"})
	case err != nil:
		rep.add(Check{Name: "direct push", Level: OK, Detail: "on (" + server + "), policy installed by hand"})
	case o.now().Sub(at) > policyStale:
		rep.add(Check{Name: "direct push", Level: Warn,
			Detail: fmt.Sprintf("on (%s), policy %s old (fetched hourly while the hub is reachable)", server, age(o.now().Sub(at))),
			Fix:    "check the hub is reachable, then: wy direct-push policy --fetch"})
	default:
		rep.add(Check{Name: "direct push", Level: OK,
			Detail: fmt.Sprintf("on (%s), policy %s old", server, age(o.now().Sub(at)))})
	}
}

// forwardAddr is where the forward lands on this machine: hub.dial, or the loopback host
// and port of hub.url. network is "tcp" or "unix"; "" when there is none.
func forwardAddr(cfg config.Config) (network, addr string) {
	if p, ok := strings.CutPrefix(cfg.Hub.Dial, config.UnixDialPrefix); ok {
		return "unix", p
	}
	if cfg.Hub.Dial != "" {
		return "tcp", cfg.Hub.Dial
	}
	u, err := url.Parse(cfg.Hub.URL)
	if err != nil || u.Hostname() == "" {
		return "", ""
	}
	h := u.Hostname()
	if ip := net.ParseIP(h); h != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", ""
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443"}[u.Scheme]
		if port == "" {
			port = "80"
		}
	}
	return "tcp", net.JoinHostPort(h, port)
}

// checkForwardPort looks at the forward's local end (machines.md 3.3): nothing listening
// means no SSH connection from the laptop holds it now; a listener that is not the hub means
// another program took the port, and the forward cannot bind it.
func checkForwardPort(ctx context.Context, rep *Report, cfg config.Config) {
	network, addr := forwardAddr(cfg)
	if network == "" {
		rep.add(Check{Name: "forward port", Level: Warn, Detail: "forward mode, but neither hub.dial nor a loopback hub.url says where the forward lands",
			Fix: "run wy machine add <this host> --reach forward again from the laptop"})
		return
	}
	conn, err := net.DialTimeout(network, addr, 500*time.Millisecond)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
			rep.add(Check{Name: "forward port", Level: OK, Detail: addr + " is free: no SSH connection from the laptop holds the forward now (records queue until one does)"})
			return
		}
		rep.add(Check{Name: "forward port", Level: Warn, Detail: "cannot probe " + addr + ": " + err.Error(),
			Fix: "check nothing blocks loopback connections on this machine"})
		return
	}
	conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cl, err := client.ForConfig(cfg, "")
	if err == nil {
		_, err = cl.Health(ctx)
	}
	if err != nil {
		rep.add(Check{Name: "forward port", Level: Warn, Detail: addr + " is taken, but the hub does not answer through it: " + err.Error(),
			Fix: "if another program holds " + addr + ", stop it, or pick another port: wy machine add <this host> --reach forward --port <n> from the laptop"})
		return
	}
	rep.add(Check{Name: "forward port", Level: OK, Detail: "forward open on " + addr + ": the hub answers"})
}

// pullChecks lists the hub's pull targets and checks the pull key (schema v4 and later).
// Nothing when the hub pulls no machine.
func pullChecks(ctx context.Context, rep *Report, db *sql.DB, o Options) error {
	rows, err := db.QueryContext(ctx, `SELECT name, enabled, state, coalesce(last_ok_at, 0), coalesce(last_error, '')
		FROM pull_target ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			name, state, lastErr string
			enabled              int
			lastOK               int64
		)
		if err := rows.Scan(&name, &enabled, &state, &lastOK, &lastErr); err != nil {
			return err
		}
		n++
		rep.add(pullTargetCheck(o, name, enabled == 1, state, lastOK, lastErr))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	key := filepath.Join(o.Dirs.State.Path, "pull", pullKeyFile)
	if fi, err := os.Stat(key); err != nil || fi.IsDir() {
		rep.add(Check{Name: "pull key", Level: Fail, Detail: fmt.Sprintf("%d pull target(s) but no pull key at %s", n, key),
			Fix: "restore " + key + " from the state volume or a backup; or restart wy hub serve (it makes a new key) and run wy machine add <host> --reach pull again for each target"})
		return nil
	}
	rep.add(Check{Name: "pull key", Level: OK, Detail: key})
	return nil
}

func pullTargetCheck(o Options, name string, enabled bool, state string, lastOK int64, lastErr string) Check {
	c := Check{Name: "pull " + name, Level: OK}
	last := "never pulled"
	if lastOK > 0 {
		last = "last pull " + age(o.now().Sub(time.UnixMilli(lastOK))) + " ago"
	}
	if lastErr != "" {
		last += ": " + lastErr
	}
	switch {
	case !enabled || state == pullStateDisabled:
		c.Detail = "disabled (" + last + ")"
	case state == pullStateOK:
		c.Detail = "ok, " + last
	case state == pullStateNew:
		c.Detail = "waiting for the first pull"
	case state == pullStateHostKey:
		c.Level, c.Detail = Fail, "the host key changed; no more pulls until it is trusted again ("+last+")"
		c.Fix = "check the server really changed its key, then: wy hub pull trust " + name
	case state == pullStateAuth:
		c.Level, c.Detail = Warn, "the server refuses the pull key ("+last+")"
		c.Fix = "authorize the key again from the laptop: wy machine add <host> --reach pull --name " + name
	case state == pullStateRefused:
		c.Level, c.Detail = Warn, "the server's wy refused the pull ("+last+")"
		c.Fix = "update wy there: wy update --machines " + name
	case state == pullStateFailing:
		c.Level, c.Detail = Warn, "failing ("+last+")"
		c.Fix = "check the server is up and reachable from the hub, then: wy hub pull run " + name + " --wait"
	default:
		c.Level, c.Detail = Warn, state+" ("+last+")"
		c.Fix = "wy hub pull list"
	}
	return c
}
