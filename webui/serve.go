package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// buildPlaceholder is the literal in sw.js that the handler replaces with the build hash.
const buildPlaceholder = "__WY_BUILD__"

// BuildHash is the first 12 hex digits of SHA-256 over every file name and content in
// fsys (sorted by path). Any change to any file changes it, which is how a service worker
// update is noticed (pwa.md 4).
func BuildHash(fsys fs.FS) string {
	var names []string
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			continue
		}
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

var (
	uiOnce  sync.Once
	uiBuild string
)

// UIBuild is BuildHash(FS()), computed once. The hub reports it as `ui` in /health.
func UIBuild() string {
	uiOnce.Do(func() { uiBuild = BuildHash(FS()) })
	return uiBuild
}

// Handler serves the app's files from fsys at "/" (GET and HEAD only). The hub mounts it
// without authentication; the pages call the API with the session cookie. sw.js carries the
// build hash computed once from fsys.
func Handler(fsys fs.FS) http.Handler {
	hash := BuildHash(fsys)
	return HandlerWithBuild(fsys, func() string { return hash })
}

// HandlerWithBuild is Handler with a build string that is asked for on every sw.js request
// (the mock hub switches it to test the update flow).
func HandlerWithBuild(fsys fs.FS, build func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			// Missing, or a directory: no listings.
			http.NotFound(w, r)
			return
		}
		if name == "sw.js" {
			b = bytes.ReplaceAll(b, []byte(buildPlaceholder), []byte(build()))
		}
		h := w.Header()
		h.Set("Content-Type", contentType(name))
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// No Last-Modified/ETag: files are embedded and not fingerprinted, no-cache suffices.
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	})
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// csp is the Content-Security-Policy of the app. There is no inline script and no inline
// style: no <style> block, no style="" attribute in markup or in setAttribute (a policy
// without 'unsafe-inline' blocks both), so dynamic values (a seat colour, a meter width) are
// set through the CSSOM (el.style.setProperty, which the policy allows). The fonts are served from the hub
// itself, so the page makes no request to any other origin. default-src 'self' already
// covers connect (API, stream), worker (sw.js) and manifest requests; they are listed so
// the intent is explicit.
const csp = "default-src 'self'; script-src 'self'; connect-src 'self'; worker-src 'self'; manifest-src 'self'; " +
	"img-src 'self' data:; " +
	"style-src 'self'; font-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// CSP returns the policy the handler sends (for tests that serve the app through another
// handler).
func CSP() string { return csp }
