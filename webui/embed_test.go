package webui

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

var (
	htmlRef   = regexp.MustCompile(`(?:src|href)="([^"]+)"`)
	jsImport  = regexp.MustCompile(`(?:from|import)\s*\(?\s*['"](\.{1,2}/[^'"]+)['"]`)
	cssImport = regexp.MustCompile(`@import\s+(?:url\()?['"]?([^'")\s;]+)`)
)

// local reports whether a reference points at a file inside the board.
func local(ref string) bool {
	return !strings.Contains(ref, "://") && !strings.HasPrefix(ref, "data:") &&
		!strings.HasPrefix(ref, "#") && !strings.HasPrefix(ref, "//")
}

func TestFSHasIndexAndEverythingItReferences(t *testing.T) {
	fsys := FS()
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		t.Fatalf("index.html missing: %v", err)
	}

	seen := map[string]bool{}
	var visit func(from, ref string)
	visit = func(from, ref string) {
		ref = strings.SplitN(strings.SplitN(ref, "#", 2)[0], "?", 2)[0]
		if ref == "" || !local(ref) {
			return
		}
		p := path.Join(path.Dir(from), ref)
		if seen[p] {
			return
		}
		seen[p] = true
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			t.Errorf("%s references %q but %s is not embedded", from, ref, p)
			return
		}
		switch path.Ext(p) {
		case ".js":
			for _, m := range jsImport.FindAllStringSubmatch(string(b), -1) {
				visit(p, m[1])
			}
		case ".css":
			for _, m := range cssImport.FindAllStringSubmatch(string(b), -1) {
				visit(p, m[1])
			}
		}
	}
	for _, m := range htmlRef.FindAllStringSubmatch(string(index), -1) {
		visit("index.html", m[1])
	}
	// Not referenced from index.html: the manifest (via <link>, already visited), the
	// service worker (registered by pwa.js) and its library (importScripts).
	visit("index.html", "sw.js")
	visit("index.html", "sw-lib.js")
	for _, want := range []string{"app.js", "api.js", "store.js", "styles/base.css", "manifest.webmanifest", "pwa.js", "push.js"} {
		if !seen[want] {
			t.Errorf("index.html does not (transitively) load %s", want)
		}
	}
	// Every view is imported by app.js, so they must all have been reached.
	views, _ := fs.Glob(fsys, "views/*.js")
	if len(views) == 0 {
		t.Fatal("no views embedded")
	}
	for _, v := range views {
		if !seen[v] {
			t.Errorf("%s is embedded but never imported", v)
		}
	}
}

func TestRequiredFilesEmbedded(t *testing.T) {
	fsys := FS()
	for _, f := range []string{"manifest.webmanifest", "sw.js", "sw-lib.js", "pwa.js", "push.js",
		"icons/icon.svg", "icons/icon-192.png", "icons/icon-512.png", "icons/maskable-512.png",
		"icons/apple-touch-icon-180.png", "icons/badge-96.png"} {
		if _, err := fs.Stat(fsys, f); err != nil {
			t.Errorf("%s is not embedded", f)
		}
	}
	index, _ := fs.ReadFile(fsys, "index.html")
	for _, want := range []string{`rel="manifest"`, "apple-touch-icon", "apple-mobile-web-app-title", "theme-color"} {
		if !strings.Contains(string(index), want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	for _, tag := range regexp.MustCompile(`(?i)<script[^>]*>`).FindAllString(string(index), -1) {
		if !strings.Contains(tag, " src=") {
			t.Errorf("index.html has an inline script %s (the CSP forbids it)", tag)
		}
	}
}

func TestManifestAndIcons(t *testing.T) {
	fsys := FS()
	b, err := fs.ReadFile(fsys, "manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		StartURL  string                                       `json:"start_url"`
		Scope     string                                       `json:"scope"`
		Display   string                                       `json:"display"`
		Icons     []struct{ Src, Sizes, Type, Purpose string } `json:"icons"`
		Shortcuts []struct {
			URL   string                 `json:"url"`
			Icons []struct{ Src string } `json:"icons"`
		} `json:"shortcuts"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest.webmanifest: %v", err)
	}
	if m.Display != "standalone" || m.Scope != "/" || m.StartURL == "" {
		t.Errorf("manifest display/scope/start_url = %q %q %q", m.Display, m.Scope, m.StartURL)
	}
	for _, ic := range m.Icons {
		if _, err := fs.Stat(fsys, ic.Src); err != nil {
			t.Errorf("manifest icon %s missing", ic.Src)
		}
	}
	for _, sc := range m.Shortcuts {
		for _, ic := range sc.Icons {
			if _, err := fs.Stat(fsys, ic.Src); err != nil {
				t.Errorf("shortcut icon %s missing", ic.Src)
			}
		}
	}
	for file, size := range map[string]int{"icons/icon-192.png": 192, "icons/icon-512.png": 512,
		"icons/maskable-512.png": 512, "icons/apple-touch-icon-180.png": 180, "icons/badge-96.png": 96} {
		f, err := fsys.Open(file)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		if cfg.Width != size || cfg.Height != size {
			t.Errorf("%s is %dx%d, want %d", file, cfg.Width, cfg.Height, size)
		}
	}
}

var assetLine = regexp.MustCompile(`(?m)^\s*'([^']+)',?\s*$`)

func TestServiceWorkerAssetsMatchEmbeddedFiles(t *testing.T) {
	fsys := FS()
	b, err := fs.ReadFile(fsys, "sw.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "const ASSETS = [")
	if i < 0 {
		t.Fatal("sw.js has no `const ASSETS = [` list")
	}
	list := src[i:]
	list = list[:strings.Index(list, "];")]
	got := map[string]bool{}
	for _, m := range assetLine.FindAllStringSubmatch(list, -1) {
		got[m[1]] = true
	}
	want := map[string]bool{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "sw.js" || strings.HasSuffix(p, ".test.cjs") || strings.HasSuffix(p, ".test.mjs") {
			return nil
		}
		want[p] = true
		return nil
	})
	var missing, extra []string
	for p := range want {
		if !got[p] {
			missing = append(missing, p)
		}
	}
	for p := range got {
		if !want[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Errorf("ASSETS differs from the embedded files: missing %v, not embedded %v", missing, extra)
	}
	if !strings.Contains(src, buildPlaceholder) {
		t.Error("sw.js lacks the __SY_BUILD__ placeholder")
	}
}

func get(h http.Handler, method, p string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, p, nil))
	return w
}

func TestHandlerServesSWWithBuildHash(t *testing.T) {
	fsys := FS()
	h := Handler(fsys)
	w := get(h, "GET", "/sw.js")
	if w.Code != 200 {
		t.Fatalf("/sw.js = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, buildPlaceholder) {
		t.Error("placeholder not replaced")
	}
	want := BuildHash(fsys)
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(want) || !strings.Contains(body, want) {
		t.Errorf("sw.js does not carry the build hash %q", want)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("sw.js content type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control %q", cc)
	}
	if UIBuild() != want {
		t.Error("UIBuild differs from BuildHash(FS())")
	}
}

func TestHandlerHeadersAndMethods(t *testing.T) {
	h := Handler(FS())
	for p, ct := range map[string]string{
		"/": "text/html", "/index.html": "text/html", "/manifest.webmanifest": "application/manifest+json",
		"/icons/icon.svg": "image/svg+xml", "/icons/icon-192.png": "image/png", "/styles/base.css": "text/css", "/app.js": "text/javascript",
	} {
		w := get(h, "GET", p)
		if w.Code != 200 {
			t.Errorf("GET %s = %d", p, w.Code)
			continue
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), ct) {
			t.Errorf("GET %s content type %q, want %s", p, w.Header().Get("Content-Type"), ct)
		}
		hd := w.Header()
		if hd.Get("Cache-Control") != "no-cache" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("GET %s: missing headers %v", p, hd)
		}
		policy := hd.Get("Content-Security-Policy")
		if policy == "" {
			t.Errorf("GET %s: no CSP", p)
		}
		for _, d := range strings.Split(policy, ";") {
			d = strings.TrimSpace(d)
			if (strings.HasPrefix(d, "script-src") || strings.HasPrefix(d, "default-src")) && strings.Contains(d, "unsafe-") {
				t.Errorf("CSP directive %q is not strict", d)
			}
		}
	}
	if w := get(h, "HEAD", "/sw.js"); w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD /sw.js = %d with %d body bytes", w.Code, w.Body.Len())
	}
	if w := get(h, "POST", "/"); w.Code != 405 {
		t.Errorf("POST / = %d", w.Code)
	}
	for _, p := range []string{"/nope.js", "/styles/", "/icons"} {
		if w := get(h, "GET", p); w.Code != 404 {
			t.Errorf("GET %s = %d, want 404 (no listings)", p, w.Code)
		}
	}
}

func TestBuildHashChangesWithAnyFile(t *testing.T) {
	mk := func(js string) fstest.MapFS {
		return fstest.MapFS{"index.html": {Data: []byte("<html>")}, "sw.js": {Data: []byte("v=__SY_BUILD__")}, "app.js": {Data: []byte(js)}}
	}
	a, b := BuildHash(mk("1")), BuildHash(mk("2"))
	if a == b || a != BuildHash(mk("1")) {
		t.Errorf("hash not stable/sensitive: %s %s", a, b)
	}
	renamed := fstest.MapFS{"index.html": {Data: []byte("<html>")}, "sw.js": {Data: []byte("v=__SY_BUILD__")}, "other.js": {Data: []byte("1")}}
	if BuildHash(renamed) == a {
		t.Error("renaming a file keeps the hash")
	}
	w := get(Handler(mk("1")), "GET", "/sw.js")
	if w.Body.String() != "v="+a {
		t.Errorf("sw.js body %q", w.Body.String())
	}
	n := "x"
	w = get(HandlerWithBuild(mk("1"), func() string { return n }), "GET", "/sw.js")
	if w.Body.String() != "v=x" {
		t.Errorf("sw.js body %q", w.Body.String())
	}
}

func TestCSPAllowsNoOtherOrigin(t *testing.T) {
	if strings.Contains(CSP(), "http") {
		t.Errorf("the policy names another origin, but the board loads everything from the hub: %s", CSP())
	}
	b, err := fs.ReadFile(FS(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "https://") {
		t.Error("index.html links another origin (fonts are served from the hub)")
	}
}

// The policy has no 'unsafe-inline' for styles, so the board must not produce an inline
// style anywhere: no style="" attribute or <style> block in a file or a string, no cssText,
// no setAttribute('style'), and no string value for the style option of h() (an object goes
// through el.style.setProperty, which the policy allows).
func TestNoInlineStyleAndStrictStyleSrc(t *testing.T) {
	for _, d := range strings.Split(CSP(), ";") {
		if strings.Contains(d, "unsafe-") {
			t.Errorf("CSP directive %q is not strict", d)
		}
	}
	if !strings.Contains(CSP(), "style-src 'self';") {
		t.Errorf("style-src is not 'self': %s", CSP())
	}
	bad := regexp.MustCompile("(?i)\\bstyle\\s*=|<style\\b|\\.cssText|setAttribute\\(\\s*['\"`]style|\\bstyle\\s*:\\s*['\"`]")
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch path.Ext(p) {
		case ".js", ".html", ".css":
		default:
			return nil
		}
		b, err := fs.ReadFile(FS(), p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if bad.MatchString(line) {
				t.Errorf("%s:%d writes an inline style (the CSP forbids it): %s", p, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every character above U+00FF that the board's code writes (arrows, shapes, dashes, quotes)
// must be inside a unicode-range of styles/fonts.css, or it falls back to whatever system
// font the platform has (the Plex subsets are latin only; fonts.css adds 'Switchyard Symbols').
// Comment-only lines are skipped.
func TestNonLatinGlyphsAreInAFontRange(t *testing.T) {
	css, err := fs.ReadFile(FS(), "styles/fonts.css")
	if err != nil {
		t.Fatal(err)
	}
	type span struct{ lo, hi rune }
	var spans []span
	for _, m := range regexp.MustCompile(`unicode-range:([^;]+);`).FindAllStringSubmatch(string(css), -1) {
		for _, part := range strings.Split(m[1], ",") {
			part = strings.TrimPrefix(strings.TrimSpace(part), "U+")
			lo, hi, _ := strings.Cut(part, "-")
			var a, b int64
			if _, err := fmt.Sscanf(lo, "%x", &a); err != nil {
				t.Fatalf("unicode-range %q: %v", part, err)
			}
			b = a
			if hi != "" {
				if _, err := fmt.Sscanf(hi, "%x", &b); err != nil {
					t.Fatalf("unicode-range %q: %v", part, err)
				}
			}
			spans = append(spans, span{rune(a), rune(b)})
		}
	}
	covered := func(r rune) bool {
		for _, s := range spans {
			if r >= s.lo && r <= s.hi {
				return true
			}
		}
		return false
	}
	missing := map[rune][]string{}
	_ = fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (path.Ext(p) != ".js" && path.Ext(p) != ".html") || strings.HasSuffix(p, "sw.js") {
			return nil
		}
		b, _ := fs.ReadFile(FS(), p)
		for _, line := range strings.Split(string(b), "\n") {
			if l := strings.TrimSpace(line); strings.HasPrefix(l, "//") || strings.HasPrefix(l, "*") || strings.HasPrefix(l, "/*") {
				continue
			}
			for _, r := range line {
				if r > 0xFF && !covered(r) {
					missing[r] = append(missing[r], p)
				}
			}
		}
		return nil
	})
	for r, files := range missing {
		t.Errorf("U+%04X %c (%s) is in no unicode-range of fonts.css: it would come from a system font", r, r, files[0])
	}
}
