package service

import (
	"strings"
	"testing"
)

// tailscale serve is one option among others: the advice leads with an https address of the
// hub's own and names the tailnet certificate condition.
func TestHTTPSFix(t *testing.T) {
	for _, got := range []string{HTTPSFix("127.0.0.1:19100"), HTTPSFixFor("http://box.lan:19100")} {
		for _, want := range []string{"wy hub serve --public-url https://<hub address>", "hub.public_url",
			"if your tailnet supports HTTPS certificates, `tailscale serve --bg 19100`"} {
			if !strings.Contains(got, want) {
				t.Errorf("%q lacks %q", got, want)
			}
		}
	}
	if got := HTTPSFixFor(""); !strings.Contains(got, "tailscale serve --bg 8787") {
		t.Errorf("no port: %q", got)
	}
}
