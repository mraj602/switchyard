package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/update"
)

func tgzOf(t *testing.T, name string, data []byte) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
	tw.Write(data)
	tw.Close()
	zw.Close()
	return b.Bytes()
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func fakeWy(version string, schema int) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\ncase \"$1 $2\" in\n\"version --short\") echo %s;;\n\"hub schema\") echo %d;;\nesac\n", version, schema))
}

// releaseServer serves /latest/download and /download/v<v> for linux_amd64 archives.
func releaseServer(t *testing.T, versions map[string][]byte) *httptest.Server {
	files := map[string][]byte{}
	for v, bin := range versions {
		arch := tgzOf(t, "wy", bin)
		name := "wy_" + v + "_linux_amd64.tar.gz"
		sums := []byte(sha(arch) + "  " + name + "\n")
		files["/download/v"+v+"/"+name], files["/download/v"+v+"/SHA256SUMS"] = arch, sums
		files["/latest/download/"+name], files["/latest/download/SHA256SUMS"] = arch, sums
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func testEnv(t *testing.T, cur string, srv *httptest.Server) (updateEnv, string) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binaries")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", "wy")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, fakeWy(cur, 4), 0o755)
	env := map[string]string{}
	if srv != nil {
		env["WY_INSTALL_BASE_URL"] = srv.URL
	}
	cfg := config.Defaults()
	cfg.Update.VerifyAttestation = false // tests must not run the real gh; see TestUpdateAttestationDefault
	return updateEnv{
		stdin: strings.NewReader(""), exe: exe, current: cur, cfg: cfg,
		dirs:   paths.Dirs{State: paths.Dir{Path: filepath.Join(dir, "state")}, Data: paths.Dir{Path: filepath.Join(dir, "data")}, Config: paths.Dir{Path: filepath.Join(dir, "config")}},
		getenv: func(k string) string { return env[k] }, goos: "linux", goarch: "amd64",
	}, exe
}

func codeOf(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exitError); ok {
		return ee.code
	}
	return 1
}

func TestUpdateCommandHappyPathJSON(t *testing.T) {
	srv := releaseServer(t, map[string][]byte{"0.0.1-e2e": fakeWy("0.0.1-e2e", 4)})
	env, exe := testEnv(t, "0.0.0-e2e", srv)
	var postOld string
	env.postUpdate = func(_ context.Context, bin, old string, _, _ io.Writer) error { postOld = old; return nil }
	var out, errb bytes.Buffer
	if err := updateMain(context.Background(), env, []string{"--yes", "--json"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	var r struct{ Status, From, To string }
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &r); err != nil || r.Status != "updated" || r.To != "0.0.1-e2e" || r.From != "0.0.0-e2e" {
		t.Fatalf("%s %v", out.String(), err)
	}
	if postOld != "0.0.0-e2e" {
		t.Errorf("post-update not run with the old version: %q", postOld)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, fakeWy("0.0.1-e2e", 4)) {
		t.Error("not replaced")
	}
	// again: current
	env.current = "0.0.1-e2e"
	out.Reset()
	if err := updateMain(context.Background(), env, []string{"--yes", "--json"}, &out, &errb); err != nil || !strings.Contains(out.String(), `"current"`) {
		t.Fatal(out.String(), err)
	}
	// rollback
	out.Reset()
	if err := updateMain(context.Background(), env, []string{"--rollback", "--json"}, &out, &errb); err != nil || !strings.Contains(out.String(), "rolled_back") {
		t.Fatal(out.String(), err, errb.String())
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, fakeWy("0.0.0-e2e", 4)) {
		t.Error("rollback did not restore")
	}
}

func TestUpdateCommandExitCodes(t *testing.T) {
	ctx := context.Background()
	srv := releaseServer(t, map[string][]byte{"0.0.1-e2e": fakeWy("0.0.1-e2e", 4)})
	env, exe := testEnv(t, "0.0.0-e2e", srv)
	before, _ := os.ReadFile(exe)
	run := func(e updateEnv, args ...string) (error, string) {
		var out, errb bytes.Buffer
		err := updateMain(ctx, e, args, &out, &errb)
		return err, out.String() + errb.String()
	}
	// usage
	for _, a := range [][]string{{"--bogus"}, {"extra"}, {"--from-file", "x"}, {"--rollback", "--check"}, {"--check", "--machines", "all"}} {
		if err, _ := run(env, a...); err != errUsage {
			t.Errorf("%v: %v", a, err)
		}
	}
	// 4: unknown version
	if err, _ := run(env, "--yes", "--version", "9.9.9"); codeOf(err) != 4 {
		t.Errorf("download: %v", err)
	}
	// 5: downgrade
	env2, exe2 := testEnv(t, "0.0.9", srv)
	if err, _ := run(env2, "--yes", "--version", "0.0.1-e2e"); codeOf(err) != 5 {
		t.Errorf("downgrade: %v", err)
	}
	if err, _ := run(env2, "--yes", "--version", "0.0.1-e2e", "--allow-downgrade"); err != nil {
		t.Errorf("allowed downgrade: %v", err)
	}
	_ = exe2
	// 3: wrong checksum
	tamper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".tar.gz") {
			w.Write([]byte("tampered"))
			return
		}
		resp, err := http.Get(srv.URL + r.URL.Path)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		w.Write(b.Bytes())
	}))
	defer tamper.Close()
	env3 := env
	env3.getenv = func(k string) string {
		if k == "WY_INSTALL_BASE_URL" {
			return tamper.URL
		}
		return ""
	}
	err, out := run(env3, "--yes", "--json")
	if codeOf(err) != 3 || !strings.Contains(out, `"code":3`) {
		t.Errorf("tampered: %v %s", err, out)
	}
	if after, _ := os.ReadFile(exe); !bytes.Equal(before, after) {
		t.Error("binary changed on a failed update")
	}
	// attestation requested without gh: fail closed
	env4 := env
	env4.getenv = func(k string) string {
		switch k {
		case "WY_INSTALL_BASE_URL":
			return srv.URL
		case "WY_VERIFY_ATTESTATION":
			return "1"
		}
		return ""
	}
	t.Setenv("PATH", t.TempDir())
	if err, _ := run(env4, "--yes"); codeOf(err) != 3 {
		t.Errorf("attestation without gh: %v", err)
	}
	if after, _ := os.ReadFile(exe); !bytes.Equal(before, after) {
		t.Error("binary changed")
	}
}

func TestUpdateCheckAndPostUpdate(t *testing.T) {
	srv := releaseServer(t, map[string][]byte{"0.5.0": fakeWy("0.5.0", 4)})
	env, exe := testEnv(t, "0.4.2", srv)
	before, _ := os.ReadFile(exe)
	var out, errb bytes.Buffer
	if err := updateMain(context.Background(), env, []string{"--check"}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "latest:  0.5.0") || !strings.Contains(out.String(), "update is available") {
		t.Errorf("%s", out.String())
	}
	if after, _ := os.ReadFile(exe); !bytes.Equal(before, after) {
		t.Error("--check changed the binary")
	}
	// internal --post-update runs the registered steps
	update.RegisterPostUpdate(update.PostStep{Name: "probe", Run: func(_ context.Context, old string) (string, error) { return "from " + old, nil }})
	out.Reset()
	if err := updateMain(context.Background(), env, []string{"--post-update", "0.4.2"}, &out, &errb); err != nil || !strings.Contains(out.String(), "probe: from 0.4.2") {
		t.Fatal(out.String(), err)
	}
}

type fleetReg struct{ ms []update.FleetMachine }

func (r fleetReg) Machines() ([]update.FleetMachine, error) { return r.ms, nil }

type fleetRemote struct{ argv [][]string }

func (f *fleetRemote) Run(_ context.Context, m update.FleetMachine, argv []string) (string, int, error) {
	f.argv = append(f.argv, append([]string{m.Name}, argv...))
	return `{"status":"updated","from":"0.4.2","to":"0.5.0"}`, 0, nil
}
func (f *fleetRemote) Platform(context.Context, update.FleetMachine) (string, string, error) {
	return "linux", "amd64", nil
}
func (f *fleetRemote) Push(context.Context, update.FleetMachine, string, string) (string, error) {
	return "", nil
}

func TestUpdateMachinesFleet(t *testing.T) {
	srv := releaseServer(t, map[string][]byte{"0.5.0": fakeWy("0.5.0", 4)})
	env, _ := testEnv(t, "0.4.2", srv)
	env.postUpdate = func(context.Context, string, string, io.Writer, io.Writer) error { return nil }
	fr := &fleetRemote{}
	env.fleet = func() (update.Registry, update.Remote, error) {
		return fleetReg{[]update.FleetMachine{{Name: "gpu"}, {Name: "hub-host", Hub: true}}}, fr, nil
	}
	var out, errb bytes.Buffer
	if err := updateMain(context.Background(), env, []string{"--machines", "all", "--yes"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	if len(fr.argv) != 2 || fr.argv[0][0] != "hub-host" {
		t.Errorf("hub host must go first: %v", fr.argv)
	}
	if got := strings.Join(fr.argv[0][1:], " "); got != ".local/bin/wy update --yes --json --version 0.5.0 --no-attestation" {
		t.Errorf("remote argv %q", got)
	}
	if !strings.Contains(out.String(), "hub-host") || !strings.Contains(out.String(), "0.4.2 -> 0.5.0") {
		t.Errorf("table: %s", out.String())
	}
	// unwired
	env.fleet = func() (update.Registry, update.Remote, error) { return nil, nil, fmt.Errorf("not wired") }
	if err := updateMain(context.Background(), env, []string{"--machines", "all", "--yes"}, &out, &errb); codeOf(err) != 1 {
		t.Errorf("unwired: %v", err)
	}
	// unknown name
	env.fleet = func() (update.Registry, update.Remote, error) { return fleetReg{}, fr, nil }
	if err := updateMain(context.Background(), env, []string{"--machines", "zz", "--yes"}, &out, &errb); err != errUsage {
		t.Errorf("unknown machine: %v", err)
	}
}

// TestRealBinariesUpdate builds two real wy binaries, packs them like build.sh does, and updates
// from one to the other: the smoke run and `hub schema --max` run the real thing.
func TestRealBinariesUpdate(t *testing.T) {
	if testing.Short() || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("builds binaries; linux/amd64 only")
	}
	dir := t.TempDir()
	build := func(v string) []byte {
		out := filepath.Join(dir, "wy-"+v)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X github.com/mraj602/watchyard/internal/version.Version="+v, "-o", out, ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build: %v\n%s", err, b)
		}
		b, _ := os.ReadFile(out)
		return b
	}
	v0, v1 := build("0.0.0-e2e"), build("0.0.1-e2e")
	srv := releaseServer(t, map[string][]byte{"0.0.1-e2e": v1})
	env, exe := testEnv(t, "0.0.0-e2e", srv)
	os.WriteFile(exe, v0, 0o755)
	// pretend this is a hub host with a v4 database
	hub := filepath.Join(env.dirs.Data.Path, "hub.db")
	os.MkdirAll(filepath.Dir(hub), 0o755)
	wy := exe
	if out, err := exec.Command(wy, "hub", "token", "create", "--kind", "phone", "--name", "p", "--db", hub).CombinedOutput(); err != nil {
		t.Fatalf("seed hub: %v %s", err, out)
	}
	var out, errb bytes.Buffer
	if err := updateMain(context.Background(), env, []string{"--yes", "--json"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	got, _ := exec.Command(exe, "version", "--short").Output()
	if strings.TrimSpace(string(got)) != "0.0.1-e2e" {
		t.Fatalf("new binary says %q", got)
	}
	if b, _ := os.ReadFile(exe + ".old"); !bytes.Equal(b, v0) {
		t.Error("wy.old is not the previous binary")
	}
}

// fakeGH puts a `gh` that exits with code on PATH (alone) and logs its arguments.
func fakeGH(t *testing.T, code int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "gh.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %q\nexit %d\n", log, code)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return log
}

// By default `wy update` verifies the build attestation when gh is installed, fails when that
// check fails, and says plainly when it could not check (checksum only).
func TestUpdateAttestationDefault(t *testing.T) {
	srv := releaseServer(t, map[string][]byte{"0.5.0": fakeWy("0.5.0", 4)})
	run := func(env updateEnv, args ...string) (error, string, string) {
		var out, errb bytes.Buffer
		err := updateMain(context.Background(), env, append(args, "--yes"), &out, &errb)
		return err, out.String(), errb.String()
	}
	// gh present and happy: verified
	env, exe := testEnv(t, "0.4.2", srv)
	env.cfg.Update.VerifyAttestation = true
	log := fakeGH(t, 0)
	err, out, _ := run(env)
	if err != nil || !strings.Contains(out, "build attestation ok") {
		t.Fatalf("with gh: %v %s", err, out)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "attestation verify") {
		t.Errorf("gh was not asked: %q", b)
	}
	// gh present and the attestation fails: nothing installed
	env, exe = testEnv(t, "0.4.2", srv)
	env.cfg.Update.VerifyAttestation = true
	before, _ := os.ReadFile(exe)
	fakeGH(t, 1)
	if err, _, _ := run(env); codeOf(err) != 3 {
		t.Errorf("failed attestation: %v", err)
	}
	if after, _ := os.ReadFile(exe); !bytes.Equal(before, after) {
		t.Error("binary changed after a failed attestation")
	}
	// the opt-outs skip gh
	for name, e := range map[string]updateEnv{"flag": env, "env": env} {
		if name == "env" {
			base := e.getenv
			e.getenv = func(k string) string {
				if k == "WY_VERIFY_ATTESTATION" {
					return "0"
				}
				return base(k)
			}
		}
		args := []string{}
		if name == "flag" {
			args = append(args, "--no-attestation")
		}
		if err, _, _ := run(e, args...); err != nil {
			t.Errorf("opt-out %s: %v", name, err)
		}
		env, exe = testEnv(t, "0.4.2", srv)
		env.cfg.Update.VerifyAttestation = true
	}
	// no gh: installs on the checksum alone and says so
	env, _ = testEnv(t, "0.4.2", srv)
	env.cfg.Update.VerifyAttestation = true
	t.Setenv("PATH", t.TempDir())
	err, out, errs := run(env)
	if err != nil || strings.Contains(out, "attestation ok") || !strings.Contains(errs, "gh CLI is not installed") {
		t.Fatalf("without gh: %v %q %q", err, out, errs)
	}
}
