package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
)

// TLS stays end to end through a forward: the connection goes to hub.dial, but the server
// name checked against the certificate and the Host header are the URL's.
func TestDialKeepsTLSNameOfTheURL(t *testing.T) {
	var gotHost, gotSNI string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		if r.TLS != nil {
			gotSNI = r.TLS.ServerName
		}
		io.WriteString(w, `{"ok":true,"version":"0.5.0","schema":4}`)
	}))
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	tr, err := DialTransport(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	c := New("https://example.com", "")
	c.HTTP = &http.Client{Transport: tr, Timeout: 5 * time.Second}
	h, err := c.Health(context.Background())
	if err != nil || h.Version != "0.5.0" {
		t.Fatalf("health through the dial: %v %+v", err, h)
	}
	if gotHost != "example.com" || gotSNI != "example.com" {
		t.Fatalf("Host %q SNI %q, want example.com", gotHost, gotSNI)
	}
	// A name the certificate does not cover fails, although the bytes reach the same server.
	c.BaseURL = "https://not-the-hub.example"
	if _, err := c.Health(context.Background()); err == nil {
		t.Fatal("certificate check skipped for a name the hub's certificate does not cover")
	}
}

func TestDialRefusesNonLoopback(t *testing.T) {
	for _, d := range []string{"10.0.0.1:443", "hub.example:443", "0.0.0.0:1", "unix:"} {
		if _, err := DialTransport(d); err == nil {
			t.Errorf("dial %q accepted", d)
		}
	}
	hub := config.Defaults().Hub
	hub.URL = "http://127.0.0.1:8787"
	if _, err := ForConfig(config.Config{Hub: hub}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDialUnixSocketAndStaleRemoval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets: forward sockets are a Unix-remote feature")
	}
	dir, err := os.MkdirTemp("", "wydial") // short path: sun_path is ~104 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "hub.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"version":"0.5.1","schema":4}`)
	})}
	go srv.Serve(ln)
	hc, err := HTTPClient("unix:"+sock, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c := New("http://127.0.0.1:47830", "")
	c.HTTP = hc
	if h, err := c.Health(context.Background()); err != nil || h.Version != "0.5.1" {
		t.Fatalf("health over the socket: %v %+v", err, h)
	}
	srv.Close()

	// A socket nobody listens on (a dropped forward) refuses: it is removed so the next
	// forward can bind it.
	stale, err := net.Listen("unix", sock+"2")
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if _, err := os.Lstat(sock + "2"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	hc2, _ := HTTPClient("unix:"+sock+"2", 2*time.Second)
	c.HTTP = hc2
	if _, err := c.Health(context.Background()); err == nil {
		t.Fatal("health over a dead socket succeeded")
	}
	if _, err := os.Lstat(sock + "2"); !os.IsNotExist(err) {
		t.Fatalf("stale socket kept: %v", err)
	}
	// A regular file at the path is never removed.
	if err := os.WriteFile(sock+"3", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	hc3, _ := HTTPClient("unix:"+sock+"3", 2*time.Second)
	c.HTTP = hc3
	_, _ = c.Health(context.Background())
	if _, err := os.Lstat(sock + "3"); err != nil {
		t.Fatal("a non-socket file was removed")
	}
}

func TestForConfigChecksTheURL(t *testing.T) {
	var cfg config.Config
	cfg.Hub.URL = "http://hub.example.com:8787"
	if _, err := ForConfig(cfg, "wy_m_x"); err == nil {
		t.Fatal("plain http to a public host was accepted")
	}
	cfg.Hub.URL = "http://home-box:8787"
	if _, err := ForConfig(cfg, "wy_m_x"); err == nil {
		t.Fatal("plain http to a single-label host was accepted")
	}
	cfg.Hub.InsecureLAN = true
	if _, err := ForConfig(cfg, "wy_m_x"); err != nil {
		t.Fatalf("insecure_lan: %v", err)
	}
	cfg.Hub.InsecureLAN = false
	cfg.Hub.URL = "https://hub.example.com"
	if _, err := ForConfig(cfg, "wy_m_x"); err != nil {
		t.Fatalf("https: %v", err)
	}
}
