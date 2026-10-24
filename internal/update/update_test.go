package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const goos, goarch = "linux", "amd64"

func fakeBin(version string, schema int) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\ncase \"$1 $2\" in\n\"version --short\") echo %s;;\n\"hub schema\") echo %d;;\nesac\n", version, schema))
}

func tgz(t *testing.T, files map[string][]byte, typ byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	for n, d := range files {
		h := &tar.Header{Name: n, Mode: 0o755, Size: int64(len(d)), Typeflag: typ}
		if typ == tar.TypeSymlink {
			h.Size, h.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write(d)
	}
	tw.Close()
	zw.Close()
	return b.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type relFiles map[string][]byte // request path -> body

// add serves a version's linux/amd64 archive and SHA256SUMS under download/v<v>/ (and latest/).
func (r relFiles) add(t *testing.T, version string, schema int, latest bool) {
	arch := tgz(t, map[string][]byte{BinaryName(goos): fakeBin(version, schema), "README.md": []byte("r")}, tar.TypeReg)
	name := "wy_" + version + "_linux_amd64.tar.gz"
	sums := []byte(sum(arch) + "  " + name + "\n" + strings.Repeat("0", 64) + "  wy_" + version + "_darwin_arm64.tar.gz\n")
	dirs := []string{"/download/v" + version}
	if latest {
		dirs = append(dirs, "/latest/download")
	}
	for _, d := range dirs {
		r[d+"/"+name] = arch
		r[d+"/SHA256SUMS"] = sums
	}
}

func server(t *testing.T, r relFiles) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, ok := r[q.URL.Path]
		if !ok {
			http.NotFound(w, q)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func setup(t *testing.T, cur string) (Options, string, relFiles, *httptest.Server) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake binaries are shell scripts")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", "wy")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, fakeBin(cur, 4), 0o755)
	r := relFiles{}
	s := server(t, r)
	return Options{Source: Source{BaseURL: s.URL}, Current: cur, Exe: exe, StateDir: filepath.Join(dir, "state"),
		GOOS: goos, GOARCH: goarch}, exe, r, s
}

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func code(err error) int { return ExitCode(err) }

func TestHappyPathLatestAndRollback(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, true)
	oldBytes := read(t, exe)
	res, err := Apply(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "updated" || res.To != "0.0.1-e2e" || res.From != "0.0.0-e2e" {
		t.Fatalf("%+v", res)
	}
	if !bytes.Equal(read(t, exe), fakeBin("0.0.1-e2e", 4)) || !bytes.Equal(read(t, exe+".old"), oldBytes) {
		t.Fatal("binary not replaced or wy.old not kept")
	}
	if st, _ := os.Stat(exe); st.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", st.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".wy.") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
	o.Current = "0.0.1-e2e"
	res, err = Apply(context.Background(), o)
	if err != nil || res.Status != "current" {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = Rollback(context.Background(), o)
	if err != nil || res.Status != "rolled_back" || res.To != "0.0.0-e2e" {
		t.Fatalf("%+v %v", res, err)
	}
	if !bytes.Equal(read(t, exe), oldBytes) || !bytes.Equal(read(t, exe+".old"), fakeBin("0.0.1-e2e", 4)) {
		t.Fatal("rollback did not swap")
	}
}

func TestExplicitVersionAndCheck(t *testing.T) {
	o, _, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, false)
	o.Version = "v0.0.1-e2e"
	c, err := Check(context.Background(), o)
	if err != nil || !c.Newer || c.To != "0.0.1-e2e" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(o.StateDir, "update")); err == nil {
		t.Error("--check wrote the update folder")
	}
	o.Version = "9.9.9"
	if _, err := Check(context.Background(), o); code(err) != ExitDownload {
		t.Fatalf("missing version: %v", err)
	}
}

func TestTamperedArchiveLeavesBinary(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, true)
	for k := range r {
		if strings.HasSuffix(k, ".tar.gz") {
			r[k] = append([]byte{}, r[k]...)
			r[k][len(r[k])-3] ^= 0xff
		}
	}
	before := read(t, exe)
	_, err := Apply(context.Background(), o)
	if code(err) != ExitVerify {
		t.Fatalf("want exit 3, got %v", err)
	}
	if !bytes.Equal(read(t, exe), before) {
		t.Fatal("binary changed")
	}
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error("wy.old written for a failed update")
	}
	left, _ := filepath.Glob(filepath.Join(o.StateDir, "update", "*"))
	if len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestMissingSumsLine(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, true)
	for k := range r {
		if strings.HasSuffix(k, "SHA256SUMS") {
			r[k] = []byte(strings.Repeat("0", 64) + "  wy_0.0.1-e2e_darwin_arm64.tar.gz\n")
		}
	}
	before := read(t, exe)
	if _, err := Apply(context.Background(), o); code(err) != ExitVerify {
		t.Fatalf("got %v", err)
	}
	if !bytes.Equal(read(t, exe), before) {
		t.Fatal("changed")
	}
}

func TestDowngradeRefusedUnlessAllowed(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.1-e2e")
	r.add(t, "0.0.0-e2e", 4, false)
	o.Version = "0.0.0-e2e"
	before := read(t, exe)
	if _, err := Apply(context.Background(), o); code(err) != ExitRefused {
		t.Fatalf("got %v", err)
	}
	if !bytes.Equal(read(t, exe), before) {
		t.Fatal("changed")
	}
	o.AllowDowngrade = true
	if res, err := Apply(context.Background(), o); err != nil || res.Status != "updated" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestPackageManagerRefused(t *testing.T) {
	for _, p := range []string{"/opt/homebrew/Cellar/wy/1/bin/wy", "/usr/local/Cellar/wy/1/bin/wy", "/home/linuxbrew/.linuxbrew/bin/wy",
		"/nix/store/abc-wy/bin/wy", "/usr/bin/wy", "/usr/sbin/wy", `C:\Users\a\AppData\Local\Microsoft\WinGet\Packages\x\wy.exe`} {
		if _, owned := PackageManager(p); !owned {
			t.Errorf("%s should be refused", p)
		}
	}
	for _, p := range []string{"/home/a/.local/bin/wy", "/usr/local/bin/wy", "/usr/binx/wy", "/opt/homebrewish/wy"} {
		if _, owned := PackageManager(p); owned {
			t.Errorf("%s should be allowed", p)
		}
	}
	o, _, _, _ := setup(t, "0.0.0-e2e")
	o.Exe = "/usr/bin/wy"
	if _, err := Apply(context.Background(), o); code(err) != ExitRefused {
		t.Fatalf("got %v", err)
	}
}

func TestHubSchemaIncompatibleRefused(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.1-e2e")
	r.add(t, "0.0.0-e2e", 3, false) // the new binary opens up to v3 only
	o.Version, o.AllowDowngrade, o.HubSchema = "0.0.0-e2e", true, 4
	before := read(t, exe)
	_, err := Apply(context.Background(), o)
	if code(err) != ExitRefused || !strings.Contains(err.Error(), "cannot open the hub database (v4)") {
		t.Fatalf("got %v", err)
	}
	if !bytes.Equal(read(t, exe), before) {
		t.Fatal("changed")
	}
	o.HubSchema = 3
	if _, err := Apply(context.Background(), o); err != nil {
		t.Fatal(err)
	}
}

func TestSmokeFailureAndWrongVersion(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	arch := tgz(t, map[string][]byte{BinaryName(goos): fakeBin("0.0.9", 4)}, tar.TypeReg) // claims 0.0.1, says 0.0.9
	name := "wy_0.0.1_linux_amd64.tar.gz"
	r["/latest/download/"+name], r["/download/v0.0.1/"+name] = arch, arch
	r["/latest/download/SHA256SUMS"] = []byte(sum(arch) + "  " + name + "\n")
	r["/download/v0.0.1/SHA256SUMS"] = r["/latest/download/SHA256SUMS"]
	before := read(t, exe)
	if _, err := Apply(context.Background(), o); code(err) != ExitVerify {
		t.Fatalf("got %v", err)
	}
	arch = tgz(t, map[string][]byte{BinaryName(goos): []byte("not a program")}, tar.TypeReg)
	r["/latest/download/"+name], r["/download/v0.0.1/"+name] = arch, arch
	r["/latest/download/SHA256SUMS"] = []byte(sum(arch) + "  " + name + "\n")
	r["/download/v0.0.1/SHA256SUMS"] = r["/latest/download/SHA256SUMS"]
	if _, err := Apply(context.Background(), o); code(err) != ExitVerify {
		t.Fatalf("got %v", err)
	}
	if !bytes.Equal(read(t, exe), before) {
		t.Fatal("changed")
	}
}

func TestDownloadFailureIsExit4(t *testing.T) {
	o, _, _, s := setup(t, "0.0.0-e2e")
	s.Close()
	if _, err := Apply(context.Background(), o); code(err) != ExitDownload {
		t.Fatalf("got %v", err)
	}
}

func TestFromFile(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, false)
	dir := t.TempDir()
	name := "wy_0.0.1-e2e_linux_amd64.tar.gz"
	os.WriteFile(filepath.Join(dir, name), r["/download/v0.0.1-e2e/"+name], 0o600)
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), r["/download/v0.0.1-e2e/SHA256SUMS"], 0o600)
	o.Source = Source{BaseURL: "http://127.0.0.1:1"} // never used
	o.FromFile, o.SumsFile = filepath.Join(dir, name), filepath.Join(dir, "SHA256SUMS")
	o.Version = "0.0.1-e2e"
	res, err := Apply(context.Background(), o)
	if err != nil || res.Status != "updated" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Error("from-file archive was removed")
	}
	if !bytes.Equal(read(t, exe), fakeBin("0.0.1-e2e", 4)) {
		t.Fatal("not replaced")
	}
	os.WriteFile(filepath.Join(dir, name), []byte("junk"), 0o600)
	o.Current = "0.0.0-e2e"
	if _, err := Apply(context.Background(), o); code(err) != ExitVerify {
		t.Fatalf("got %v", err)
	}
	o.FromFile = ""
	if _, err := Apply(context.Background(), o); code(err) != ExitUsage {
		t.Fatalf("lone --sums-file: %v", err)
	}
}

func TestAttestation(t *testing.T) {
	ctx := context.Background()
	if ok, err := VerifyAttestation(ctx, Attestation{}, "x"); err != nil || ok {
		t.Fatal(ok, err)
	}
	t.Setenv("PATH", t.TempDir()) // no gh
	_, err := VerifyAttestation(ctx, Attestation{Required: true}, "x")
	if code(err) != ExitVerify || !strings.Contains(err.Error(), "gh") {
		t.Fatalf("missing gh must fail closed: %v", err)
	}
	// the default: no gh means checksum only, said honestly by the caller
	if ok, err := VerifyAttestation(ctx, Attestation{Auto: true}, "x"); err != nil || ok {
		t.Fatalf("auto without gh: %v %v", ok, err)
	}
	var got []string
	run := func(_ context.Context, name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}
	if ok, err := VerifyAttestation(ctx, Attestation{Auto: true, GH: "/bin/gh", Run: run}, "/tmp/a.tar.gz"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	want := "/bin/gh attestation verify /tmp/a.tar.gz --repo mraj602/watchyard --signer-workflow mraj602/watchyard/.github/workflows/release.yml"
	if strings.Join(got, " ") != want {
		t.Fatalf("argv %q", strings.Join(got, " "))
	}
	bad := func(context.Context, string, ...string) error { return errors.New("no attestation") }
	for _, a := range []Attestation{{Required: true, GH: "gh", Run: bad}, {Auto: true, GH: "gh", Run: bad}} {
		if _, err := VerifyAttestation(ctx, a, "a"); code(err) != ExitVerify {
			t.Fatalf("%v", err)
		}
	}
}

func TestAttestationInApply(t *testing.T) {
	o, exe, r, _ := setup(t, "0.0.0-e2e")
	r.add(t, "0.0.1-e2e", 4, true)
	o.Attestation = Attestation{Required: true, GH: "gh", Run: func(context.Context, string, ...string) error { return errors.New("bad") }}
	before := read(t, exe)
	if _, err := Apply(context.Background(), o); code(err) != ExitVerify {
		t.Fatalf("%v", err)
	}
	if !bytes.Equal(read(t, exe), before) {
		t.Fatal("changed")
	}
}

func TestBaseURLRules(t *testing.T) {
	ctx := context.Background()
	if _, err := Resolve(ctx, Source{BaseURL: "http://example.com/r"}, Latest); code(err) != ExitUsage {
		t.Fatalf("plain http to a remote host must be refused: %v", err)
	}
	if _, err := Resolve(ctx, Source{BaseURL: "ftp://x"}, Latest); code(err) != ExitUsage {
		t.Fatal(err)
	}
	if _, err := Resolve(ctx, Source{BaseURL: "http://127.0.0.1:1"}, "../x"); code(err) != ExitUsage {
		t.Fatal(err)
	}
}

func TestRedirectToPlainHTTPRefused(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/SHA256SUMS", http.StatusFound)
	}))
	defer s.Close()
	_, err := Resolve(context.Background(), Source{BaseURL: s.URL}, Latest)
	if code(err) != ExitDownload || !strings.Contains(err.Error(), "only https") {
		t.Fatalf("got %v", err)
	}
}

func TestFetchCacheAndOtherPlatform(t *testing.T) {
	_, _, r, s := setup(t, "0.0.0")
	arch := tgz(t, map[string][]byte{BinaryName(goos): []byte("mach-o")}, tar.TypeReg)
	nm := "wy_0.5.0_darwin_arm64.tar.gz"
	r["/latest/download/"+nm], r["/download/v0.5.0/"+nm] = arch, arch
	r["/latest/download/SHA256SUMS"] = []byte(sum(arch) + "  " + nm + "\n")
	r["/download/v0.5.0/SHA256SUMS"] = r["/latest/download/SHA256SUMS"]
	rel, err := Resolve(context.Background(), Source{BaseURL: s.URL}, Latest)
	if err != nil || rel.Version != "0.5.0" || len(rel.SumsRaw) == 0 {
		t.Fatalf("%+v %v", rel, err)
	}
	cache := t.TempDir()
	a, err := Fetch(context.Background(), rel, "darwin", "arm64", cache)
	if err != nil || a.SHA256 != sum(arch) {
		t.Fatalf("%+v %v", a, err)
	}
	bin, h, err := Extract(a)
	if err != nil || h != sum([]byte("mach-o")) || string(read(t, bin)) != "mach-o" {
		t.Fatalf("%s %s %v", bin, h, err)
	}
	s.Close() // the second fetch comes from the cache
	if _, err := Fetch(context.Background(), rel, "darwin", "arm64", cache); err != nil {
		t.Fatalf("cache: %v", err)
	}
	if _, err := Fetch(context.Background(), rel, "linux", "arm64", cache); code(err) != ExitVerify {
		t.Fatalf("unlisted platform: %v", err)
	}
}

func TestExtractRefusals(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, data []byte) Archive {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o600)
		return Archive{Path: p, GOOS: "linux", GOARCH: "amd64", Name: name}
	}
	for label, data := range map[string][]byte{
		"nested":  tgz(t, map[string][]byte{"bin/wy": []byte("x")}, tar.TypeReg),
		"dotted":  tgz(t, map[string][]byte{"./wy": []byte("x")}, tar.TypeReg),
		"missing": tgz(t, map[string][]byte{"README.md": []byte("x")}, tar.TypeReg),
		"symlink": tgz(t, map[string][]byte{BinaryName(goos): nil}, tar.TypeSymlink),
		"notgz":   []byte("plain"),
	} {
		if _, _, err := Extract(mk(label+".tgz", data)); code(err) != ExitVerify {
			t.Errorf("%s: %v", label, err)
		}
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("wy.exe")
	w.Write([]byte("MZ"))
	zw.Close()
	a := mk("w.zip", zb.Bytes())
	a.GOOS = "windows"
	bin, h, err := Extract(a)
	if err != nil || filepath.Base(bin) != "wy.exe" || h != sum([]byte("MZ")) {
		t.Fatalf("%s %s %v", bin, h, err)
	}
}

func TestWindowsRenameDance(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "wy.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	os.WriteFile(exe+".old", []byte("older"), 0o755)
	nb := filepath.Join(dir, "new")
	os.WriteFile(nb, []byte("new"), 0o755)
	if err := Replace(exe, nb, "windows"); err != nil {
		t.Fatal(err)
	}
	if string(read(t, exe)) != "new" || string(read(t, exe+".old")) != "old" {
		t.Fatal("dance wrong")
	}
	if err := swapOld(exe, exe+".old", "windows"); err != nil {
		t.Fatal(err)
	}
	if string(read(t, exe)) != "old" || string(read(t, exe+".old")) != "new" {
		t.Fatal("swap wrong")
	}
	CleanupOld(exe, "linux")
	if _, err := os.Stat(exe + ".old"); err != nil {
		t.Fatal("CleanupOld removed wy.old off Windows")
	}
	CleanupOld(exe, "windows")
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Fatal("not cleaned")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.4.2", "0.5.0", -1, true}, {"0.5.0", "0.5.0", 0, true}, {"1.0.0", "0.9.9", 1, true},
		{"0.0.0-e2e", "0.0.1-e2e", -1, true}, {"1.0.0-rc.1", "1.0.0", -1, true},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1, true}, {"1.0.0-alpha", "1.0.0-alpha.1", -1, true},
		{"v0.5.0", "0.5.0+build", 0, true}, {"dev", "0.5.0", 0, false},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("%s vs %s: %d %v", c.a, c.b, got, ok)
		}
	}
}

func TestPostUpdateSteps(t *testing.T) {
	postSteps = nil
	defer func() { postSteps = nil }()
	RegisterPostUpdate(PostStep{Name: "a", Run: func(context.Context, string) (string, error) { return "ok", nil }})
	RegisterPostUpdate(PostStep{Name: "b", Run: func(_ context.Context, old string) (string, error) { return "", errors.New("boom " + old) }})
	rs := RunPostUpdate(context.Background(), "0.4.2")
	if len(rs) != 2 || rs[0].Detail != "ok" || rs[1].Error != "boom 0.4.2" || !PostUpdateFailed(rs) {
		t.Fatalf("%+v", rs)
	}
}

// Legacy compat (rename.md 2.13): an `sy update` from before the rename kept sy.old next to
// the binary; `wy update --rollback` swaps that in when there is no wy.old.
func TestRollbackFindsLegacyOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "wy")
	for _, n := range []string{"wy", "sy.old"} { // rename:keep
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := rollbackSource(exe); got != filepath.Join(dir, "sy.old") { // rename:keep
		t.Fatalf("got %s", got)
	}
	if got := rollbackSource(filepath.Join(dir, "other")); got != filepath.Join(dir, "other.old") {
		t.Fatalf("other name: %s", got)
	}
	if err := os.WriteFile(exe+".old", []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := rollbackSource(exe); got != filepath.Join(dir, "wy.old") {
		t.Fatalf("wy.old wins: %s", got)
	}
}
