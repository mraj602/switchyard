package appmirror

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	certHash          string // cert_sha256 in the manifest; default the pin
	sumsLie           bool   // SHA256SUMS holds another digest
	corruptAPK        bool   // the served APK differs from the one hashed
}

// fakeAPK is a file shaped like a signed APK: some bytes, an APK signing block holding a v3 (or
// v2) value whose signers carry the given certificates, a stub central directory and the end
// record. Only what APKSignerCerts reads is real.
func fakeAPK(id uint32, certs ...string) []byte {
	lp := func(b []byte) []byte { return append(binary.LittleEndian.AppendUint32(nil, uint32(len(b))), b...) }
	var signers []byte
	for _, c := range certs {
		signed := append(lp(lp([]byte("digest"))), lp(lp([]byte(c)))...)
		signers = append(signers, lp(append(lp(signed), lp([]byte("sig"))...))...)
	}
	val := lp(signers)
	pair := binary.LittleEndian.AppendUint64(nil, uint64(4+len(val)))
	pair = binary.LittleEndian.AppendUint32(pair, id)
	pair = append(pair, val...)
	size := uint64(len(pair) + 24)
	block := binary.LittleEndian.AppendUint64(nil, size)
	block = append(block, pair...)
	block = binary.LittleEndian.AppendUint64(block, size)
	block = append(block, "APK Sig Block 42"...)
	out := append([]byte("PK\x03\x04 local entries "), block...)
	cd := len(out)
	out = append(out, "PK\x01\x02 central"...)
	eocd := []byte{'P', 'K', 5, 6, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0}
	eocd = binary.LittleEndian.AppendUint32(eocd, uint32(cd))
	return append(append(out, eocd...), 0, 0)
}

// certDER stands for a certificate; its SHA-256 is what the pin names.
const certDER = "release certificate bytes"

func pin() string { return sum([]byte(certDER)) }

// signed is an APK signed by the pinned certificate; tag makes the bytes differ per release.
func signed(tag string) []byte { return append(fakeAPK(idV3, certDER), tag...) }

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
					{"name": "watchyard-" + ver + ".apk", "browser_download_url": base + "watchyard-" + ver + ".apk"},
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
		apkName := "watchyard-" + ver + ".apk"
		digest := sum(rl.apk)
		switch parts[1] {
		case "manifest.json":
			fmt.Fprintf(w, `{"v":1,"version":%q,"build":%d,"min_sdk":31,"apk":%q,"size":%d,"sha256":%q,"cert_sha256":%q,"notes":"x"}`,
				ver, rl.build, apkName, len(rl.apk), digest, cmp.Or(rl.certHash, pin()))
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
	return New(Config{Dir: filepath.Join(t.TempDir(), "app", "android"), Repo: "me/proj", APIBase: f.srv.URL, Enabled: enabled, Client: f.srv.Client(), PinnedCert: pin()})
}

func TestSyncPublishesNewestVerifiedRelease(t *testing.T) {
	f := newFake(t)
	f.rels = []fakeRel{
		{tag: "phone-v0.8.0", apk: signed("old"), build: 8},
		{tag: "phone-v0.10.0", apk: signed("new apk"), build: 10},
		{tag: "phone-v0.11.0", apk: signed("draft"), build: 11, draft: true},
		{tag: "phone-v0.12.0", apk: signed("pre"), build: 12, prerelease: true},
		{tag: "v1.0.0", apk: signed("hub"), build: 99},
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
	if !bytes.Equal(b, signed("new apk")) {
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
		"sums disagree":   {tag: "phone-v1.0.0", apk: signed("a"), build: 1, sumsLie: true},
		"apk is replaced": {tag: "phone-v1.0.0", apk: signed("a"), build: 1, corruptAPK: true},
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

func TestSyncPinnedCertificate(t *testing.T) {
	other := sum([]byte("someone else's key"))
	cases := map[string]fakeRel{
		"manifest names another certificate": {tag: "phone-v1.0.0", apk: signed("a"), build: 1, certHash: other},
		"apk is signed by another key":       {tag: "phone-v1.0.0", apk: append(fakeAPK(idV3, "someone else's key"), "a"...), build: 1},
		"one of two signers is another key":  {tag: "phone-v1.0.0", apk: append(fakeAPK(idV2, certDER, "someone else's key"), "a"...), build: 1},
		"apk has no signing block":           {tag: "phone-v1.0.0", apk: []byte("PK plain zip PK\x05\x06 0000000000000000"), build: 1},
	}
	for name, rl := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.rels = []fakeRel{rl}
			m := newMirror(t, f, nil)
			err := m.Sync(context.Background())
			if err == nil || !strings.Contains(err.Error(), "not") {
				t.Fatalf("err = %v", err)
			}
			if _, ok := m.Current(); ok {
				t.Fatal("a release with the wrong signer is served")
			}
		})
	}
	// v2-only signing is read too.
	f := newFake(t)
	f.rels = []fakeRel{{tag: "phone-v1.0.0", apk: append(fakeAPK(idV2, certDER), "v2"...), build: 1}}
	if err := newMirror(t, f, nil).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSyncRefusesWithoutAPin(t *testing.T) {
	f := newFake(t)
	f.rels = []fakeRel{{tag: "phone-v1.0.0", apk: signed("a"), build: 1}}
	for _, bad := range []string{"", " ", "not hex"} {
		m := New(Config{Dir: filepath.Join(t.TempDir(), "android"), Repo: "me/proj", APIBase: f.srv.URL, Client: f.srv.Client(), PinnedCert: bad})
		err := m.Sync(context.Background())
		if err == nil || (bad == "" || bad == " ") && !errors.Is(err, ErrNoPin) {
			t.Fatalf("pin %q: err = %v", bad, err)
		}
		if _, ok := m.Current(); ok {
			t.Fatal("mirrored without a pin")
		}
	}
	if f.hits != 0 {
		t.Fatalf("%d requests were made without a pin", f.hits)
	}
	if ReleaseCertSHA256 != "" && !hexRE.MatchString(ReleaseCertSHA256) {
		t.Fatalf("ReleaseCertSHA256 %q is not 64 lower-case hex digits", ReleaseCertSHA256)
	}
}

func TestOffSwitchAndKeepTwo(t *testing.T) {
	f := newFake(t)
	f.rels = []fakeRel{{tag: "phone-v0.1.0", apk: signed("1"), build: 1}}
	on := false
	m := newMirror(t, f, func(context.Context) bool { return on })
	if err := m.Sync(context.Background()); err != nil || f.hits != 0 {
		t.Fatalf("off: err=%v hits=%d (must not touch the network)", err, f.hits)
	}
	on = true
	for i, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		f.mu.Lock()
		f.rels = []fakeRel{{tag: "phone-v" + v, apk: signed(v), build: i + 1}}
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
