package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Exit codes of `sy update` (machines.md 7.3).
const (
	ExitFailed   = 1 // replace or post-update failed
	ExitUsage    = 2
	ExitVerify   = 3 // verification failed
	ExitDownload = 4 // download failed
	ExitRefused  = 5 // refused (downgrade, package manager, hub schema)
)

// Size caps (machines.md 7.3 steps 3 and 4).
const (
	MaxArchiveBytes = 200 << 20
	MaxBinaryBytes  = 100 << 20
	maxSumsBytes    = 1 << 20
)

// Error carries the exit code `sy update` ends with. Every error of this package that has a
// meaning for the caller is an *Error; errors.As finds it.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func fail(code int, format string, a ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, a...)}
}

// ExitCode is the exit code for err: its *Error code, else 1.
func ExitCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitFailed
}

var versionRE = regexp.MustCompile(`^[0-9A-Za-z.+-]+$`)

// NormalizeVersion trims whitespace and a leading v and rejects anything that is not a
// version-shaped token (it ends up in URLs and file names).
func NormalizeVersion(v string) (string, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" || !versionRE.MatchString(v) || strings.Contains(v, "..") {
		return "", fail(ExitUsage, "bad version %q", v)
	}
	return v, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkBase validates and normalises a base URL: https, or http for a loopback host.
func checkBase(base string) (string, error) {
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fail(ExitUsage, "bad release base URL %q", base)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
	default:
		return "", fail(ExitUsage, "release base URL %q must be https (http only for a loopback host)", base)
	}
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// httpClient returns c (or a default) with the redirect rule of machines.md 7.3 step 1:
// redirects go only to https, or to a loopback host (tests).
func httpClient(c *http.Client) *http.Client {
	var out http.Client
	if c != nil {
		out = *c
	} else {
		out = http.Client{Timeout: 15 * time.Minute, Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		}}
	}
	prev := out.CheckRedirect
	out.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && isLoopbackHost(req.URL.Hostname())) {
			return fmt.Errorf("refusing redirect to %s: only https", req.URL.Redacted())
		}
		if prev != nil {
			return prev(req, via)
		}
		return nil
	}
	return &out
}

// get downloads url into w, at most max bytes. A 404 and every other failure are exit 4.
func get(ctx context.Context, c *http.Client, rawurl string, max int64, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return fail(ExitDownload, "download failed: %v", err)
	}
	req.Header.Set("User-Agent", "sy-update")
	resp, err := c.Do(req)
	if err != nil {
		return fail(ExitDownload, "download failed: %s: %v", rawurl, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(ExitDownload, "download failed: %s: %s", rawurl, resp.Status)
	}
	if resp.ContentLength > max {
		return fail(ExitDownload, "download failed: %s is %d bytes, more than the %d byte limit", rawurl, resp.ContentLength, max)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return fail(ExitDownload, "download failed: %s: %v", rawurl, err)
	}
	if n > max {
		return fail(ExitDownload, "download failed: %s is larger than the %d byte limit", rawurl, max)
	}
	return nil
}

var archiveNameRE = regexp.MustCompile(`^sy_(.+)_(linux|darwin|windows)_(amd64|arm64)\.(tar\.gz|zip)$`)

// ParseSums parses a SHA256SUMS file ("<hex>  <name>" or "<hex> *<name>").
func ParseSums(raw []byte) ([]SumLine, error) {
	var out []SumLine
	sc := bufio.NewScanner(bytes.NewReader(raw))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, name, ok := strings.Cut(line, " ")
		name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
		sum = strings.ToLower(sum)
		if !ok || len(sum) != 64 || name == "" || strings.ContainsAny(name, "/\\") {
			return nil, fail(ExitVerify, "SHA256SUMS line %d is malformed", n)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fail(ExitVerify, "SHA256SUMS line %d is malformed", n)
		}
		out = append(out, SumLine{SHA256: sum, Name: name})
	}
	if len(out) == 0 {
		return nil, fail(ExitVerify, "SHA256SUMS is empty")
	}
	return out, nil
}

// versionFromSums reads the version out of the archive names (all must agree).
func versionFromSums(sums []SumLine) (string, error) {
	v := ""
	for _, s := range sums {
		m := archiveNameRE.FindStringSubmatch(s.Name)
		if m == nil {
			continue
		}
		if v != "" && v != m[1] {
			return "", fail(ExitVerify, "SHA256SUMS lists archives of two versions (%s and %s)", v, m[1])
		}
		v = m[1]
	}
	if v == "" {
		return "", fail(ExitVerify, "SHA256SUMS lists no sy archive")
	}
	return v, nil
}

// ReleaseFromSums builds a Release from SHA256SUMS bytes already in hand (`--sums-file`).
// version may be empty: it is then read from the archive names.
func ReleaseFromSums(raw []byte, version string) (Release, error) {
	sums, err := ParseSums(raw)
	if err != nil {
		return Release{}, err
	}
	v, err := versionFromSums(sums)
	if err != nil {
		return Release{}, err
	}
	if version != "" && version != Latest {
		want, err := NormalizeVersion(version)
		if err != nil {
			return Release{}, err
		}
		if want != v {
			return Release{}, fail(ExitVerify, "SHA256SUMS is for %s, not %s", v, want)
		}
	}
	return Release{Version: v, Sums: sums, SumsRaw: raw}, nil
}

// Resolve finds a release: version is Latest or a version ("0.5.0" or "v0.5.0"). It fetches
// and parses SHA256SUMS (machines.md 7.3 step 1) and returns it; it downloads no archive.
func Resolve(ctx context.Context, src Source, version string) (Release, error) {
	base, err := checkBase(src.BaseURL)
	if err != nil {
		return Release{}, err
	}
	var dir string
	want := ""
	if version == "" || version == Latest {
		dir = base + "/latest/download"
	} else {
		if want, err = NormalizeVersion(version); err != nil {
			return Release{}, err
		}
		dir = base + "/download/v" + want
	}
	c := httpClient(src.HTTP)
	var buf bytes.Buffer
	sumsURL := dir + "/SHA256SUMS"
	if err := get(ctx, c, sumsURL, maxSumsBytes, &buf); err != nil {
		return Release{}, err
	}
	rel, err := ReleaseFromSums(buf.Bytes(), want)
	if err != nil {
		return Release{}, err
	}
	if want == "" {
		// "latest" is a moving alias: pin the tag it points at and download from the tag's
		// own URL, so the archives and SHA256SUMS cannot come from two different releases.
		sumsURL = base + "/download/v" + rel.Version + "/SHA256SUMS"
		var pinned bytes.Buffer
		if err := get(ctx, c, sumsURL, maxSumsBytes, &pinned); err != nil {
			return Release{}, err
		}
		if !bytes.Equal(pinned.Bytes(), buf.Bytes()) {
			return Release{}, fail(ExitDownload, "the latest release changed while it was being resolved; try again")
		}
	}
	rel.Base, rel.SumsURL, rel.http = base, sumsURL, c
	return rel, nil
}

func (r Release) line(name string) (string, bool) {
	for _, s := range r.Sums {
		if s.Name == name {
			return s.SHA256, true
		}
	}
	return "", false
}

// Fetch downloads the archive for goos/goarch into cacheDir (`<state>/update/cache/` for
// platforms other than this one), at most 200 MB, and verifies its SHA-256 against the
// release's SHA256SUMS line. A mismatch or a missing line is an error and nothing is left
// behind (machines.md 7.3 steps 2-3). An already cached file that verifies is reused.
func Fetch(ctx context.Context, rel Release, goos, goarch, cacheDir string) (Archive, error) {
	name := rel.ArchiveName(goos, goarch)
	if _, err := NormalizeVersion(rel.Version); err != nil {
		return Archive{}, err
	}
	want, ok := rel.line(name)
	if !ok {
		return Archive{}, fail(ExitVerify, "%s is not listed in SHA256SUMS; nothing installed", name)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return Archive{}, fail(ExitFailed, "%v", err)
	}
	final := filepath.Join(cacheDir, name)
	a := Archive{Release: rel, GOOS: goos, GOARCH: goarch, Name: name, Path: final, SHA256: want}
	if got, err := hashFile(final); err == nil {
		if got == want {
			return a, nil
		}
		_ = os.Remove(final) // stale or tampered cache entry
	}
	if rel.SumsURL == "" {
		return Archive{}, fail(ExitDownload, "no download location for %s (the release was not resolved from a source)", name)
	}
	tmp, err := os.CreateTemp(cacheDir, "."+name+".part-*")
	if err != nil {
		return Archive{}, fail(ExitFailed, "%v", err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	c := rel.http
	if c == nil {
		c = httpClient(nil)
	}
	dl := rel.SumsURL[:strings.LastIndex(rel.SumsURL, "/")]
	if err := get(ctx, c, dl+"/"+name, MaxArchiveBytes, io.MultiWriter(tmp, h)); err != nil {
		tmp.Close()
		return Archive{}, err
	}
	if err := tmp.Close(); err != nil {
		return Archive{}, fail(ExitFailed, "%v", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return Archive{}, fail(ExitVerify, "checksum mismatch for %s; nothing installed", name)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return Archive{}, fail(ExitFailed, "%v", err)
	}
	return a, nil
}

// LocalArchive verifies an archive file already on disk (`--from-file`) against rel's
// SHA256SUMS: its base name must be the archive name of goos/goarch and its line must match.
func LocalArchive(rel Release, goos, goarch, file string) (Archive, error) {
	name := rel.ArchiveName(goos, goarch)
	if filepath.Base(file) != name {
		return Archive{}, fail(ExitVerify, "%s is not %s (the archive for this machine)", filepath.Base(file), name)
	}
	want, ok := rel.line(name)
	if !ok {
		return Archive{}, fail(ExitVerify, "%s is not listed in SHA256SUMS; nothing installed", name)
	}
	st, err := os.Stat(file)
	if err != nil {
		return Archive{}, fail(ExitDownload, "%v", err)
	}
	if st.Size() > MaxArchiveBytes {
		return Archive{}, fail(ExitVerify, "%s is larger than the %d byte limit", name, MaxArchiveBytes)
	}
	got, err := hashFile(file)
	if err != nil {
		return Archive{}, fail(ExitDownload, "%v", err)
	}
	if got != want {
		return Archive{}, fail(ExitVerify, "checksum mismatch for %s; nothing installed", name)
	}
	return Archive{Release: rel, GOOS: goos, GOARCH: goarch, Name: name, Path: file, SHA256: want}, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// BinaryName is the executable's file name on goos.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "sy.exe"
	}
	return "sy"
}

// Extract takes the single `sy` (`sy.exe` on Windows) entry out of a verified archive:
// a regular file, no path components, at most 100 MB (machines.md 7.3 step 4). It returns the
// extracted file's path (in a folder next to the archive, `<archive>.x/`) and its SHA-256,
// which `sy machine add` compares with the hash the remote prints after receiving the stream.
func Extract(a Archive) (binPath string, sha256sum string, err error) {
	if a.Path == "" {
		return "", "", fail(ExitVerify, "no archive to extract")
	}
	dir := a.Path + ".x"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fail(ExitFailed, "%v", err)
	}
	bin := BinaryName(a.GOOS)
	tmp, err := os.CreateTemp(dir, ".sy-*")
	if err != nil {
		return "", "", fail(ExitFailed, "%v", err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	if a.GOOS == "windows" {
		err = extractZip(a.Path, bin, w)
	} else {
		err = extractTarGz(a.Path, bin, w)
	}
	if cerr := tmp.Close(); err == nil && cerr != nil {
		err = fail(ExitFailed, "%v", cerr)
	}
	if err != nil {
		return "", "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", "", fail(ExitFailed, "%v", err)
	}
	final := filepath.Join(dir, bin)
	_ = os.Remove(final)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", "", fail(ExitFailed, "%v", err)
	}
	return final, hex.EncodeToString(h.Sum(nil)), nil
}

func copyCapped(w io.Writer, r io.Reader, name string) error {
	n, err := io.Copy(w, io.LimitReader(r, MaxBinaryBytes+1))
	if err != nil {
		return fail(ExitVerify, "reading %s from the archive: %v", name, err)
	}
	if n > MaxBinaryBytes {
		return fail(ExitVerify, "%s in the archive is larger than the %d byte limit", name, MaxBinaryBytes)
	}
	return nil
}

func extractTarGz(archive, bin string, w io.Writer) error {
	f, err := os.Open(archive)
	if err != nil {
		return fail(ExitVerify, "%v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fail(ExitVerify, "%s is not a gzip file: %v", filepath.Base(archive), err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(ExitVerify, "reading %s: %v", filepath.Base(archive), err)
		}
		if h.Name != bin {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fail(ExitVerify, "%s in the archive is not a regular file", bin)
		}
		if found {
			return fail(ExitVerify, "the archive has two %s entries", bin)
		}
		if h.Size > MaxBinaryBytes {
			return fail(ExitVerify, "%s in the archive is larger than the %d byte limit", bin, MaxBinaryBytes)
		}
		if err := copyCapped(w, tr, bin); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return fail(ExitVerify, "%s does not contain %s", filepath.Base(archive), bin)
	}
	return nil
}

func extractZip(archive, bin string, w io.Writer) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fail(ExitVerify, "%s is not a zip file: %v", filepath.Base(archive), err)
	}
	defer zr.Close()
	var hit *zip.File
	for _, f := range zr.File {
		if f.Name != bin {
			continue
		}
		if !f.Mode().IsRegular() {
			return fail(ExitVerify, "%s in the archive is not a regular file", bin)
		}
		if hit != nil {
			return fail(ExitVerify, "the archive has two %s entries", bin)
		}
		hit = f
	}
	if hit == nil {
		return fail(ExitVerify, "%s does not contain %s", filepath.Base(archive), bin)
	}
	if hit.UncompressedSize64 > MaxBinaryBytes {
		return fail(ExitVerify, "%s in the archive is larger than the %d byte limit", bin, MaxBinaryBytes)
	}
	rc, err := hit.Open()
	if err != nil {
		return fail(ExitVerify, "%v", err)
	}
	defer rc.Close()
	return copyCapped(w, rc, bin)
}

