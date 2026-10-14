// Package smoke runs the board in a headless browser against the mock hub.
package smoke

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/mraj602/switchyard/webui"
	"github.com/mraj602/switchyard/webui/devserver"
)

// TestBrowserSmoke needs Chrome and puppeteer-core, so it only runs when asked:
//
//	mkdir -p $TMP/pp && (cd $TMP/pp && npm init -y && npm i puppeteer-core)
//	SY_WEBUI_BROWSER=1 SY_PUPPETEER_MODULES=$TMP/pp/node_modules SY_WEBUI_SHOTS=$TMP/shots \
//	    go test ./webui/smoke
//
// CHROME overrides the browser path (default /usr/bin/google-chrome).
func TestBrowserSmoke(t *testing.T) {
	if os.Getenv("SY_WEBUI_BROWSER") != "1" {
		t.Skip("set SY_WEBUI_BROWSER=1 to run the headless browser smoke test")
	}
	modules := os.Getenv("SY_PUPPETEER_MODULES")
	if modules == "" {
		t.Fatal("SY_PUPPETEER_MODULES must point at a node_modules directory containing puppeteer-core")
	}
	out := os.Getenv("SY_WEBUI_SHOTS")
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

	cmd := exec.Command("node", "smoke.cjs")
	cmd.Env = append(os.Environ(), "NODE_PATH="+modules, "BASE_URL="+ts.URL, "TOKEN="+devserver.DefaultToken,
		"PAIR_SECRET="+devserver.PairSecret, "PAIR_CODE="+devserver.PairCode, "OWNER_SECRET="+devserver.OwnerSecret, "OUT_DIR="+out)
	b, err := cmd.CombinedOutput()
	t.Logf("%s", b)
	if err != nil {
		t.Fatalf("smoke test failed: %v", err)
	}
	t.Logf("screenshots in %s", out)
}
