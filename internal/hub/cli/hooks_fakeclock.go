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
	"github.com/mraj602/switchyard/internal/hub/push/expo"
)

// newServeHooks reads three variables:
//
//   - SY_FAKE_CLOCK=<path>: run on a clock.Fake that starts at the wall time and is moved
//     only through a control server on the unix socket <path>:
//     GET /now (RFC 3339 time), POST /advance?d=<duration> (advance, then one engine pass,
//     one widget refresh pass and one Expo receipts poll), POST /sync (one engine pass and
//     one widget refresh pass).
//   - SY_TEST_WEBPUSH_DIAL=<host:port>: the Web Push sender connects to this address for
//     every endpoint and skips certificate checks, so endpoints on an allow-listed fake host
//     reach webpushtest's TLS server.
//   - SY_TEST_EXPO_DIAL=<host:port>: the Expo sender (sends and receipts) connects to this
//     address whatever the base URL and skips certificate checks, so the default
//     https://exp.host reaches expotest's TLS server (push.md 12.6). exp.host is never dialled.
func newServeHooks(logf func(string, ...any)) (serveHooks, error) {
	h := serveHooks{Clock: clock.Real{}, Attach: func(*engine.Engine) {},
		AttachExpo: func(*expo.Receipts, *expo.Refresher) {}, Close: func() {}}
	if addr := os.Getenv("SY_TEST_WEBPUSH_DIAL"); addr != "" {
		h.WebPushClient = dialClient(addr)
		logf("TEST BUILD: web push goes to %s", addr)
	}
	if addr := os.Getenv("SY_TEST_EXPO_DIAL"); addr != "" {
		h.ExpoClient = dialClient(addr)
		logf("TEST BUILD: expo pushes go to %s", addr)
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
		mu       sync.Mutex
		eng      *engine.Engine
		receipts *expo.Receipts
		refresh  *expo.Refresher
	)
	syncEngine := func(poll bool) {
		mu.Lock()
		e, rc, rf := eng, receipts, refresh
		mu.Unlock()
		if e != nil {
			e.Sync()
		}
		if rf != nil {
			rf.Sync()
		}
		if rc != nil && poll {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := rc.Poll(ctx); err != nil {
				logf("fake clock: expo receipts: %v", err)
			}
			cancel()
		}
	}
	mux := http.NewServeMux()
	now := func(w http.ResponseWriter) { _, _ = w.Write([]byte(fake.Now().UTC().Format(time.RFC3339Nano))) }
	mux.HandleFunc("GET /now", func(w http.ResponseWriter, r *http.Request) { now(w) })
	mux.HandleFunc("POST /sync", func(w http.ResponseWriter, r *http.Request) { syncEngine(false); now(w) })
	mux.HandleFunc("POST /advance", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("d"))
		if err != nil || d < 0 {
			http.Error(w, "d must be a duration", http.StatusBadRequest)
			return
		}
		fake.Advance(d)
		syncEngine(true)
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
	h.AttachExpo = func(rc *expo.Receipts, rf *expo.Refresher) {
		mu.Lock()
		receipts, refresh = rc, rf
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
