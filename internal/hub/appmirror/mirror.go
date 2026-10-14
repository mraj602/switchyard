package appmirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Release asset names (phone.md P16.1).
const (
	ManifestName = "manifest.json"
	SumsName     = "SHA256SUMS"
	maxAPK       = 300 << 20
	keep         = 2
)

var (
	tagRE  = regexp.MustCompile(`^phone-v(\d+)\.(\d+)\.(\d+)$`)
	hexRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fileRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Manifest is manifest.json (phone.md P16.1).
type Manifest struct {
	V           int    `json:"v"`
	Version     string `json:"version"`
	Build       int    `json:"build"`
	MinSDK      int    `json:"min_sdk"`
	APK         string `json:"apk"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	CertSHA256  string `json:"cert_sha256"`
	Notes       string `json:"notes"`
	PublishedAt string `json:"published_at"`
}

// ParseManifest checks the shape a mirror (and the phone) relies on.
func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	switch {
	case m.V != 1:
		return m, fmt.Errorf("manifest: unsupported v %d", m.V)
	case m.Version == "" || m.Build <= 0:
		return m, errors.New("manifest: version and build are required")
	case !fileRE.MatchString(m.APK):
		return m, errors.New("manifest: apk must be a plain file name")
	case !hexRE.MatchString(m.SHA256) || !hexRE.MatchString(m.CertSHA256):
		return m, errors.New("manifest: sha256 and cert_sha256 must be 64 lower-case hex digits")
	}
	return m, nil
}

// ParseSums reads a `sha256sum` file into name -> hex digest.
func ParseSums(b []byte) map[string]string {
	out := map[string]string{}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && hexRE.MatchString(f[0]) {
			out[strings.TrimPrefix(f[1], "*")] = f[0]
		}
	}
	return out
}

// Config builds a Mirror.
type Config struct {
	// Dir is <data>/app/android.
	Dir string
	// Repo is "owner/name" (version.Repo unless configured).
	Repo string
	// Enabled reports the off switch (app.android_mirror), asked on every run. Nil = on.
	Enabled func(context.Context) bool
	// Client downloads from GitHub: the netguard dial guard, redirects allowed (release
	// assets redirect to a CDN). Nil is http.DefaultClient (tests).
	Client *http.Client
	// APIBase is https://api.github.com; tests point it at a fake.
	APIBase string
	Log     func(format string, args ...any)
}

// Mirror is the on-disk mirror.
type Mirror struct{ c Config }

// New makes a mirror; nothing is read or written until Sync or Current.
func New(c Config) *Mirror {
	if c.Client == nil {
		c.Client = http.DefaultClient
	}
	if c.APIBase == "" {
		c.APIBase = "https://api.github.com"
	}
	if c.Log == nil {
		c.Log = func(string, ...any) {}
	}
	return &Mirror{c}
}

// Release is a mirrored release: its manifest (raw, as released) and the APK path.
type Release struct {
	Manifest Manifest
	Raw      []byte
	APKPath  string
}

// Current returns the mirrored release with the highest build, or ok=false. A directory
// whose files do not agree (a half-written copy) is ignored.
func (m *Mirror) Current() (Release, bool) {
	ents, _ := os.ReadDir(m.c.Dir)
	var best Release
	found := false
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		d := filepath.Join(m.c.Dir, e.Name())
		raw, err := os.ReadFile(filepath.Join(d, ManifestName))
		if err != nil {
			continue
		}
		mf, err := ParseManifest(raw)
		if err != nil {
			continue
		}
		apk := filepath.Join(d, mf.APK)
		if st, err := os.Stat(apk); err != nil || !st.Mode().IsRegular() {
			continue
		}
		if !found || mf.Build > best.Manifest.Build {
			best, found = Release{Manifest: mf, Raw: raw, APKPath: apk}, true
		}
	}
	return best, found
}

type ghRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// newest picks the newest non-draft, non-prerelease release tagged phone-vX.Y.Z (phone.md
// P16.2); the app's updater applies the same filter.
func newest(rs []ghRelease) (ghRelease, bool) {
	var best ghRelease
	var bestV [3]int
	ok := false
	for _, r := range rs {
		if r.Draft || r.Prerelease {
			continue
		}
		g := tagRE.FindStringSubmatch(r.Tag)
		if g == nil {
			continue
		}
		var v [3]int
		for i := range v {
			v[i], _ = strconv.Atoi(g[i+1])
		}
		if !ok || less(bestV, v) {
			best, bestV, ok = r, v, true
		}
	}
	return best, ok
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// Sync runs one mirror pass. A pass that fails leaves the mirror as it was.
func (m *Mirror) Sync(ctx context.Context) error {
	if m.c.Enabled != nil && !m.c.Enabled(ctx) {
		return nil
	}
	if m.c.Dir == "" || strings.Count(m.c.Repo, "/") != 1 {
		return fmt.Errorf("appmirror: bad repository %q", m.c.Repo)
	}
	body, err := m.get(ctx, m.c.APIBase+"/repos/"+m.c.Repo+"/releases?per_page=20", 4<<20)
	if err != nil {
		return err
	}
	var rs []ghRelease
	if err := json.Unmarshal(body, &rs); err != nil {
		return fmt.Errorf("appmirror: releases: %w", err)
	}
	rel, ok := newest(rs)
	if !ok {
		return nil
	}
	asset := func(name string) string {
		for _, a := range rel.Assets {
			if a.Name == name {
				return a.URL
			}
		}
		return ""
	}
	mu, su := asset(ManifestName), asset(SumsName)
	if mu == "" || su == "" {
		return fmt.Errorf("appmirror: %s lacks %s or %s", rel.Tag, ManifestName, SumsName)
	}
	raw, err := m.get(ctx, mu, 1<<20)
	if err != nil {
		return err
	}
	mf, err := ParseManifest(raw)
	if err != nil {
		return fmt.Errorf("appmirror: %s: %w", rel.Tag, err)
	}
	if "phone-v"+mf.Version != rel.Tag {
		return fmt.Errorf("appmirror: %s: manifest says version %s", rel.Tag, mf.Version)
	}
	if cur, ok := m.Current(); ok && cur.Manifest.Build >= mf.Build && cur.Manifest.SHA256 == mf.SHA256 {
		return nil // already mirrored
	}
	sums, err := m.get(ctx, su, 1<<20)
	if err != nil {
		return err
	}
	want, ok := ParseSums(sums)[mf.APK]
	if !ok || want != mf.SHA256 {
		return fmt.Errorf("appmirror: %s: SHA256SUMS and the manifest disagree about %s", rel.Tag, mf.APK)
	}
	au := asset(mf.APK)
	if au == "" {
		return fmt.Errorf("appmirror: %s has no %s", rel.Tag, mf.APK)
	}
	// Download into a scratch folder, verify, then rename into place: a reader never sees
	// a half-written release.
	if err := os.MkdirAll(m.c.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(m.c.Dir, ".incoming-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	got, err := m.download(ctx, au, filepath.Join(tmp, mf.APK))
	if err != nil {
		return err
	}
	if got != mf.SHA256 {
		return fmt.Errorf("appmirror: %s: %s hashes to %s, expected %s; not published", rel.Tag, mf.APK, got, mf.SHA256)
	}
	for name, b := range map[string][]byte{ManifestName: raw, SumsName: sums} {
		if err := os.WriteFile(filepath.Join(tmp, name), b, 0o600); err != nil {
			return err
		}
	}
	dst := filepath.Join(m.c.Dir, mf.Version)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	m.c.Log("app mirror: %s published (build %d)", rel.Tag, mf.Build)
	m.prune()
	return nil
}

// prune keeps the current and the previous version (the two highest builds).
func (m *Mirror) prune() {
	ents, _ := os.ReadDir(m.c.Dir)
	type dv struct {
		name  string
		build int
	}
	var all []dv
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b := -1
		if raw, err := os.ReadFile(filepath.Join(m.c.Dir, e.Name(), ManifestName)); err == nil {
			if mf, err := ParseManifest(raw); err == nil {
				b = mf.Build
			}
		}
		all = append(all, dv{e.Name(), b})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].build > all[j].build })
	for i := keep; i < len(all); i++ {
		_ = os.RemoveAll(filepath.Join(m.c.Dir, all[i].name))
	}
}

// Run syncs at start and then every interval until ctx ends.
func (m *Mirror) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := m.Sync(ctx); err != nil && ctx.Err() == nil {
			m.c.Log("app mirror: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Mirror) req(ctx context.Context, url string) (*http.Response, error) {
	rq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	rq.Header.Set("Accept", "application/vnd.github+json")
	rq.Header.Set("User-Agent", "switchyard-hub")
	res, err := m.c.Client.Do(rq)
	if err != nil {
		return nil, fmt.Errorf("appmirror: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("appmirror: %s answered %d", url, res.StatusCode)
	}
	return res, nil
}

func (m *Mirror) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	res, err := m.req(ctx, url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("appmirror: %s is larger than %d bytes", url, limit)
	}
	return b, nil
}

func (m *Mirror) download(ctx context.Context, url, path string) (string, error) {
	res, err := m.req(ctx, url)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, maxAPK+1))
	if err != nil {
		return "", err
	}
	if n > maxAPK {
		return "", errors.New("appmirror: the APK is larger than the 300 MB limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
