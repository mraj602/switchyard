package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/appmirror"
	"github.com/mraj602/switchyard/internal/hub/store"
)

type fakeMirror struct {
	rel appmirror.Release
	ok  bool
}

func (f fakeMirror) Current() (appmirror.Release, bool) { return f.rel, f.ok }

func appHub(t *testing.T, m api.AppMirror) *hub {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := api.Config{Store: st, PingEvery: time.Hour}
	if m != nil {
		cfg.AppMirror = m
	}
	srv := api.New(cfg)
	h := &hub{t: t, st: st, srv: srv, http: httptest.NewServer(srv.Handler()), tokens: map[string]string{}}
	t.Cleanup(h.stop)
	return h
}

func getWith(t *testing.T, url, token string) (*http.Response, string) {
	t.Helper()
	rq, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		rq.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestAndroidMirrorRoutes(t *testing.T) {
	// Nothing mirrored: 404 for an authenticated caller, 401 without a token.
	h := appHub(t, nil)
	owner := ownerToken(t, h)
	if res, _ := getWith(t, h.http.URL+"/v1/app/android", ""); res.StatusCode != 401 {
		t.Fatalf("no token: %d", res.StatusCode)
	}
	for _, p := range []string{"/v1/app/android", "/v1/app/android/apk"} {
		if res, _ := getWith(t, h.http.URL+p, owner); res.StatusCode != 404 {
			t.Fatalf("%s empty mirror: %d", p, res.StatusCode)
		}
	}

	dir := t.TempDir()
	apk := filepath.Join(dir, "switchyard-0.9.0.apk")
	if err := os.WriteFile(apk, []byte("APKBYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := `{"v":1,"version":"0.9.0","build":14,"min_sdk":31,"apk":"switchyard-0.9.0.apk","size":8,` +
		`"sha256":"` + strings.Repeat("a", 64) + `","cert_sha256":"` + strings.Repeat("b", 64) + `","notes":"n"}`
	mf, err := appmirror.ParseManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	h2 := appHub(t, fakeMirror{appmirror.Release{Manifest: mf, Raw: []byte(raw), APKPath: apk}, true})
	owner = ownerToken(t, h2)
	res, body := getWith(t, h2.http.URL+"/v1/app/android", owner)
	if res.StatusCode != 200 || !strings.Contains(body, `"url":"/v1/app/android/apk"`) || !strings.Contains(body, `"build":14`) {
		t.Fatalf("manifest: %d %s", res.StatusCode, body)
	}
	res, body = getWith(t, h2.http.URL+"/v1/app/android/apk", owner)
	if res.StatusCode != 200 || body != "APKBYTES" || res.Header.Get("ETag") != `"`+strings.Repeat("a", 64)+`"` ||
		res.Header.Get("Content-Type") != "application/vnd.android.package-archive" {
		t.Fatalf("apk: %d %q %v", res.StatusCode, body, res.Header)
	}
}
