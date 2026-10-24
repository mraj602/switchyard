// Package smoke runs the board in a headless browser against the mock hub.
package smoke

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/mraj602/watchyard/webui"
	"github.com/mraj602/watchyard/webui/devserver"
)

// TestBrowserSmoke needs Chrome and puppeteer-core, so it only runs when asked:
//
//	mkdir -p $TMP/pp && (cd $TMP/pp && npm init -y && npm i puppeteer-core)
//	WY_WEBUI_BROWSER=1 WY_PUPPETEER_MODULES=$TMP/pp/node_modules WY_WEBUI_SHOTS=$TMP/shots \
//	    go test ./webui/smoke
//
// CHROME overrides the browser path (default /usr/bin/google-chrome).
func TestBrowserSmoke(t *testing.T) {
	if os.Getenv("WY_WEBUI_BROWSER") != "1" {
		t.Skip("set WY_WEBUI_BROWSER=1 to run the headless browser smoke test")
	}
	modules := os.Getenv("WY_PUPPETEER_MODULES")
	if modules == "" {
		t.Fatal("WY_PUPPETEER_MODULES must point at a node_modules directory containing puppeteer-core")
	}
	out := os.Getenv("WY_WEBUI_SHOTS")
	if out == "" {
		out = t.TempDir()
	}

	hub := devserver.New(devserver.Options{Static: webui.FS(), Dev: true})
	// Serve with the hub's Content-Security-Policy (webui.CSP), so a violation fails the page.
	csp := webui.CSP()
	inner := hub.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Headless Chrome still talks to the desktop's session bus, so the fixture alerts would
	// pop up as real notifications: run it on a private bus (dbus-run-session), or, without
	// that tool, with a bus address that points nowhere.
	cmd := exec.Command("node", "smoke.cjs")
	env := os.Environ()
	if dbus, err := exec.LookPath("dbus-run-session"); err == nil {
		cmd = exec.Command(dbus, "--", "node", "smoke.cjs")
	} else {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent/wy-no-bus")
	}
	cmd.Env = append(env, "NODE_PATH="+modules, "BASE_URL="+ts.URL, "TOKEN="+devserver.DefaultToken,
		"PAIR_SECRET="+devserver.PairSecret, "PAIR_CODE="+devserver.PairCode, "OWNER_SECRET="+devserver.OwnerSecret, "EXPIRED_OWNER_SECRET="+devserver.ExpiredOwnerSecret, "OUT_DIR="+out)
	b, err := cmd.CombinedOutput()
	t.Logf("%s", b)
	if err != nil {
		t.Fatalf("smoke test failed: %v", err)
	}
	t.Logf("screenshots in %s", out)
}
