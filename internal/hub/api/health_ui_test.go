package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/mraj602/watchyard/webui"
)

// /health carries the build hash of the served web app (pwa.md 4), the same one
// webui.Handler puts in sw.js.
func TestHealthReportsUIBuild(t *testing.T) {
	h := startHub(t, "")
	code, body := h.do("GET", "/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("/health: %d %s", code, body)
	}
	var got struct {
		UI string `json:"ui"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := webui.BuildHash(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>board")}})
	if got.UI == "" || got.UI != want {
		t.Errorf("ui = %q, want %q", got.UI, want)
	}
}
