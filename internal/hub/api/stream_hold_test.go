package api_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// /v1/stream must stay open through the http.Server that `wy hub serve`
// runs, for as long as the client wants, with a ping every PingEvery. A ReadTimeout or
// WriteTimeout on that server would cut it at that age. WY_SSE_HOLD=70s holds it for the
// real duration with the real ping interval (25 s); by default it holds 40 pings of 50 ms.
func TestStreamStaysOpenThroughServeServer(t *testing.T) {
	ping, hold := 50*time.Millisecond, 2*time.Second
	if v := os.Getenv("WY_SSE_HOLD"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		ping, hold = 0, d // 0 = the default 25 s
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := api.New(api.Config{Store: st, WebUI: fstest.MapFS{"index.html": {Data: []byte("board")}}, PingEvery: ping})
	defer srv.Close()
	hs := srv.HTTPServer("127.0.0.1:0")
	if hs.ReadTimeout != 0 || hs.WriteTimeout != 0 {
		t.Errorf("serve's http.Server has ReadTimeout %v / WriteTimeout %v: they cut the stream", hs.ReadTimeout, hs.WriteTimeout)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = hs.Serve(ln) }()
	defer hs.Close()
	c, err := auth.Create(context.Background(), st, auth.CreateRequest{Kind: "browser", Name: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hold+30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+ln.Addr().String()+"/v1/stream", nil)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	start := time.Now()
	pings := 0
	sc := bufio.NewScanner(resp.Body)
	for time.Since(start) < hold {
		if !sc.Scan() {
			t.Fatalf("stream ended after %v and %d pings: %v", time.Since(start).Round(time.Millisecond), pings, sc.Err())
		}
		if strings.HasPrefix(sc.Text(), "event: ping") {
			pings++
		}
	}
	every := ping
	if every == 0 {
		every = 25 * time.Second
	}
	if want := int(hold / every / 2); pings < max(want, 1) {
		t.Fatalf("%d pings in %v", pings, hold)
	}
}
