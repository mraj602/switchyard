package doctor

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
)

// healthProbeTimeout bounds the one network call checkRemoteHub makes.
const healthProbeTimeout = 3 * time.Second

// probeHealth is the real Options.HealthProbe: GET <hub>/health through the hub.dial
// transport, 3 s, no token.
func probeHealth(ctx context.Context, hubURL, dial string) (client.Health, error) {
	tr, err := client.DialTransport(dial)
	if err != nil {
		return client.Health{}, err
	}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	c := client.New(hubURL, "")
	c.HTTP = &http.Client{Transport: tr, Timeout: healthProbeTimeout}
	return c.Health(ctx)
}

// hubIsLocal reports whether the hub that hub.url names runs on this machine, so the hub
// database in the data folder is the one the machine uses: no hub.url (the machine is its
// own hub), a loopback address or "localhost" (merged mode), the host of the local hub's
// listen address, or the address of one of this machine's interfaces. A hub on another
// host leaves any hub.db here a stale copy (a hub that moved away).
func hubIsLocal(cfg config.Config, listen string) bool {
	raw := strings.TrimSpace(cfg.Hub.URL)
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return true // not understood: keep the old behaviour
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	if lh, _, err := net.SplitHostPort(listen); err == nil && lh != "" && strings.EqualFold(strings.Trim(lh, "[]"), h) {
		return true
	}
	if ip != nil {
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
					return true
				}
			}
		}
	}
	return false
}

// checkRemoteHub is what doctor says about a hub on another machine: it answers /health
// (over https). The token is judged by the flush and board token lines. The hub-database
// checks (push url, push targets...) do not run: they would read the stale database a hub
// left behind in this machine's data folder.
func checkRemoteHub(ctx context.Context, rep *Report, o Options, cfg config.Config) {
	hub := strings.TrimRight(cfg.Hub.URL, "/")
	probe := o.HealthProbe
	if probe == nil {
		probe = probeHealth
	}
	h, err := probe(ctx, hub, cfg.Hub.Dial)
	if err != nil {
		rep.add(Check{Name: "hub health", Level: Warn, Detail: "the hub at " + hub + " did not answer /health: " + err.Error(),
			Fix: "check the hub is running and reachable from this machine (VPN, tunnel, hub.dial); records wait in the outbox meanwhile"})
		return
	}
	d := hub + " is up"
	if h.Version != "" {
		d += " (version " + h.Version + ")"
	}
	if h.Frozen != nil {
		d += ", frozen for a move"
	}
	if !strings.HasPrefix(strings.ToLower(hub), "https://") {
		rep.add(Check{Name: "hub health", Level: Warn, Detail: d + ", but hub.url is not https",
			Fix: "use the hub's https address: sy config set hub.url https://<hub address>"})
		return
	}
	rep.add(Check{Name: "hub health", Level: OK, Detail: d})
}
