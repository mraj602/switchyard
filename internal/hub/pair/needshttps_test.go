package pair

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The refusal says what a phone needs, the address in use, the two ways out, and names
// `tailscale serve` as one option only; a loopback address is called unreachable.
func TestNeedsHTTPSMessage(t *testing.T) {
	_, _, err := NormalizeURL("http://127.0.0.1:19200", "phone", false)
	if !errors.Is(err, ErrNeedsHTTPS) {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{"needs the hub's https address", "http://127.0.0.1:19200", "board was opened over",
		"can never be reached by a phone", "Open the board at the hub's https address", "sy hub serve --public-url https://<hub address>",
		"hub.public_url", "if your tailnet supports HTTPS certificates, `tailscale serve --bg 19200`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
	_, _, err = NormalizeURL("http://box.lan:19200", "phone", false)
	if err == nil || strings.Contains(err.Error(), "loopback") || !strings.Contains(err.Error(), "http://box.lan:19200") {
		t.Errorf("non-loopback: %v", err)
	}
}

// Create answers needs_https with its own code (api.yaml); other bad addresses stay bad_request.
func TestCreateNeedsHTTPSCode(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Create(context.Background(), CreateRequest{Kind: "phone", URL: "http://127.0.0.1:19200"})
	var ce *ClaimError
	if !errors.As(err, &ce) || ce.Status != 400 || ce.Code != "needs_https" {
		t.Fatalf("%v", err)
	}
	_, err = f.svc.Create(context.Background(), CreateRequest{Kind: "phone", URL: "ftp://x"})
	if !errors.As(err, &ce) || ce.Code != "bad_request" {
		t.Fatalf("%v", err)
	}
}
