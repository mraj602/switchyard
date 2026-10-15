//go:build fakeclock

// This file exists only in binaries built with `-tags fakeclock` (the end-to-end test in
// test/e2e/push builds one). A release build has no fake clock and no test push client.

package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/clock"
	"github.com/mraj602/switchyard/internal/hub/push/engine"
	"github.com/mraj602/switchyard/internal/hub/push/refresh"
)

// newServeHooks reads two variables:
//
//   - SY_FAKE_CLOCK=<path>: run on a clock.Fake that starts at the wall time and is moved
//     only through a control server on the unix socket <path>:
//     GET /now (RFC 3339 time), POST /advance?d=<duration> (advance, then one engine pass
//     and one widget refresh pass), POST /sync (the same without advancing).
//   - SY_TEST_WEBPUSH_DIAL=<host:port>: the Web Push sender connects to this address for
//     every endpoint and skips certificate checks, so endpoints on an allow-listed fake host
//     reach webpushtest's TLS server.
func newServeHooks(logf func(string, ...any)) (serveHooks, error) {
	h := serveHooks{Clock: clock.Real{}, Attach: func(*engine.Engine) {},
		AttachRefresh: func(*refresh.Refresher) {}, Close: func() {}}
	if addr := os.Getenv("SY_TEST_WEBPUSH_DIAL"); addr != "" {
		h.WebPushClient = dialClient(addr)
		logf("TEST BUILD: web push goes to %s", addr)
	}
	sock := os.Getenv("SY_FAKE_CLOCK")
	if sock == "" {
		return h, nil
	}
	fake := clock.NewFake(time.Now().UTC().Truncate(time.Millisecond))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return h, err
	}
	var (
		mu  sync.Mutex
		eng *engine.Engine
		rfr *refresh.Refresher
	)
	syncEngine := func() {
		mu.Lock()
		e, rf := eng, rfr
		mu.Unlock()
		if e != nil {
			e.Sync()
		}
		if rf != nil {
			rf.Sync()
		}
	}
	mux := http.NewServeMux()
	now := func(w http.ResponseWriter) { _, _ = w.Write([]byte(fake.Now().UTC().Format(time.RFC3339Nano))) }
	mux.HandleFunc("GET /now", func(w http.ResponseWriter, r *http.Request) { now(w) })
	mux.HandleFunc("POST /sync", func(w http.ResponseWriter, r *http.Request) { syncEngine(); now(w) })
	mux.HandleFunc("POST /advance", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("d"))
		if err != nil || d < 0 {
			http.Error(w, "d must be a duration", http.StatusBadRequest)
			return
		}
		fake.Advance(d)
		syncEngine()
		now(w)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("fake clock control: %v", err)
		}
	}()
	logf("TEST BUILD: fake clock at %s, control socket %s", fake.Now().Format(time.RFC3339), sock)
	h.Clock, h.Fake = fake, true
	h.Attach = func(e *engine.Engine) {
		mu.Lock()
		eng = e
		mu.Unlock()
	}
	h.AttachRefresh = func(rf *refresh.Refresher) {
		mu.Lock()
		rfr = rf
		mu.Unlock()
	}
	h.Close = func() {
		_ = srv.Close()
		_ = os.Remove(sock)
	}
	return h, nil
}

// dialClient is an HTTP client that connects to addr for every request and skips
// certificate checks (test build only), with the guarded client's timeout and no redirects.
func dialClient(addr string) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test build only
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
