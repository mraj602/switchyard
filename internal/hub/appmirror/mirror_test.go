package appmirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub serves a releases list and its assets. Tests change rels between passes.
type fakeGitHub struct {
	mu   sync.Mutex
	srv  *httptest.Server
	rels []fakeRel
	hits int
}

type fakeRel struct {
	tag               string
	draft, prerelease bool
	apk               []byte
	build             int
	sumsLie           bool // SHA256SUMS holds another digest
	corruptAPK        bool // the served APK differs from the one hashed
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits++
	if r.URL.Path == "/repos/me/proj/releases" {
		var out []map[string]any
		for _, rl := range f.rels {
			ver := strings.TrimPrefix(rl.tag, "phone-v")
			base := f.srv.URL + "/dl/" + rl.tag + "/"
			out = append(out, map[string]any{"tag_name": rl.tag, "draft": rl.draft, "prerelease": rl.prerelease,
				"assets": []map[string]any{
					{"name": "manifest.json", "browser_download_url": base + "manifest.json"},
					{"name": "SHA256SUMS", "browser_download_url": base + "SHA256SUMS"},
					{"name": "switchyard-" + ver + ".apk", "browser_download_url": base + "switchyard-" + ver + ".apk"},
				}})
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/dl/"), "/")
	for _, rl := range f.rels {
		if len(parts) != 2 || parts[0] != rl.tag {
			continue
		}
		ver := strings.TrimPrefix(rl.tag, "phone-v")
		apkName := "switchyard-" + ver + ".apk"
		digest := sum(rl.apk)
		switch parts[1] {
		case "manifest.json":
			fmt.Fprintf(w, `{"v":1,"version":%q,"build":%d,"min_sdk":31,"apk":%q,"size":%d,"sha256":%q,"cert_sha256":%q,"notes":"x"}`,
				ver, rl.build, apkName, len(rl.apk), digest, strings.Repeat("c", 64))
		case "SHA256SUMS":
			d := digest
			if rl.sumsLie {
				d = strings.Repeat("0", 64)
			}
			fmt.Fprintf(w, "%s  %s\n", d, apkName)
		case apkName:
			if rl.corruptAPK {
				_, _ = w.Write([]byte("tampered"))
				return
			}
			_, _ = w.Write(rl.apk)
		default:
			http.NotFound(w, r)
		}
		return
	}
	http.NotFound(w, r)
}

func newMirror(t *testing.T, f *fakeGitHub, enabled func(context.Context) bool) *Mirror {
	return New(Config{Dir: filepath.Join(t.TempDir(), "app", "android"), Repo: "me/proj", APIBase: f.srv.URL, Enabled: enabled, Client: f.srv.Client()})
}

func TestSyncPublishesNewestVerifiedRelease(t *testing.T) {
	f := newFake(t)
	f.rels = []fakeRel{
		{tag: "phone-v0.8.0", apk: []byte("old"), build: 8},
		{tag: "phone-v0.10.0", apk: []byte("new apk"), build: 10},
		{tag: "phone-v0.11.0", apk: []byte("draft"), build: 11, draft: true},
		{tag: "phone-v0.12.0", apk: []byte("pre"), build: 12, prerelease: true},
		{tag: "v1.0.0", apk: []byte("hub"), build: 99},
	}
	m := newMirror(t, f, nil)
	if _, ok := m.Current(); ok {
		t.Fatal("empty mirror has a release")
	}
	if err := m.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	cur, ok := m.Current()
	if !ok || cur.Manifest.Version != "0.10.0" || cur.Manifest.Build != 10 {
		t.Fatalf("current = %+v ok=%v (semver order, not list order; no draft, prerelease or hub tag)", cur.Manifest, ok)
	}
	b, _ := os.ReadFile(cur.APKPath)
	if string(b) != "new apk" {
		t.Fatalf("apk bytes %q", b)
	}
	// A second pass with nothing new downloads no asset: the releases list and the manifest.
	f.mu.Lock()
	f.hits = 0
	f.mu.Unlock()
	if err := m.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.hits != 2 {
		t.Fatalf("second pass made %d requests, want 2", f.hits)
	}
}

func TestSyncRefusesWhatDoesNotVerify(t *testing.T) {
	cases := map[string]fakeRel{
		"sums disagree":   {tag: "phone-v1.0.0", apk: []byte("a"), build: 1, sumsLie: true},
		"apk is replaced": {tag: "phone-v1.0.0", apk: []byte("a"), build: 1, corruptAPK: true},
	}
	for name, rl := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.rels = []fakeRel{rl}
			m := newMirror(t, f, nil)
			if err := m.Sync(context.Background()); err == nil {
				t.Fatal("published an unverified release")
			}
			if _, ok := m.Current(); ok {
				t.Fatal("an unverified release is served")
			}
			ents, _ := os.ReadDir(m.c.Dir)
			if len(ents) != 0 {
				t.Fatalf("leftovers: %v", ents)
			}
		})
	}
}

func TestOffSwitchAndKeepTwo(t *testing.T) {
	f := newFake(t)
	f.rels = []fakeRel{{tag: "phone-v0.1.0", apk: []byte("1"), build: 1}}
	on := false
	m := newMirror(t, f, func(context.Context) bool { return on })
	if err := m.Sync(context.Background()); err != nil || f.hits != 0 {
		t.Fatalf("off: err=%v hits=%d (must not touch the network)", err, f.hits)
	}
	on = true
	for i, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		f.mu.Lock()
		f.rels = []fakeRel{{tag: "phone-v" + v, apk: []byte(v), build: i + 1}}
		f.mu.Unlock()
		if err := m.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(m.c.Dir)
	if len(ents) != 2 {
		t.Fatalf("kept %d versions, want current and previous", len(ents))
	}
	if cur, _ := m.Current(); cur.Manifest.Version != "0.3.0" {
		t.Fatalf("current %s", cur.Manifest.Version)
	}
}

func TestParseManifestRejects(t *testing.T) {
	good := `{"v":1,"version":"1.0.0","build":1,"apk":"a.apk","sha256":"` + strings.Repeat("a", 64) + `","cert_sha256":"` + strings.Repeat("b", 64) + `"}`
	if _, err := ParseManifest([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"v":1`, `"v":2`, 1),
		strings.Replace(good, `a.apk`, `../a.apk`, 1),
		strings.Replace(good, strings.Repeat("a", 64), "AB", 1),
		`{`,
	} {
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
