package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/brand"
)

// DefaultOwner and SignerWorkflow pin who built a release: attestations are checked against
// this repository's release workflow only (install.sh does the same).
const (
	DefaultOwner   = "mraj602"
	SignerWorkflow = ".github/workflows/release.yml"
)

// Attestation says whether and how to check a release archive's build provenance with the
// gh CLI (machines.md 7.3 step 3).
type Attestation struct {
	// Auto is the default of `wy update`: verify when the gh CLI is installed, and say so when
	// it is not (the archive is then checked against SHA256SUMS only, which proves it was not
	// damaged but not who built it). A failed verification is exit 3. Off by default here so
	// that code using the package without gh is unchanged; cmd/wy turns it on unless
	// WY_VERIFY_ATTESTATION=0, update.verify_attestation = false or --no-attestation.
	Auto bool
	// Required: WY_VERIFY_ATTESTATION=1. When set and gh is missing the update fails closed
	// (exit 3), exactly as install.sh does.
	Required bool
	// GH is the gh executable ("" = look up "gh" on PATH).
	GH string
	// Owner defaults to DefaultOwner. The repository is <owner>/watchyard and the signer
	// workflow <owner>/watchyard/.github/workflows/release.yml (after a rename, the old
	// repository name is accepted too).
	Owner string
	// Run runs the command (tests); nil runs it for real.
	Run func(ctx context.Context, name string, args ...string) error
}

// VerifyAttestation runs `gh attestation verify <archive> --repo <o>/watchyard
// --signer-workflow <o>/watchyard/.github/workflows/release.yml`. It is a no-op unless
// a.Required or a.Auto. verified says the attestation was checked and held. With only Auto and
// no gh it returns (false, nil); with Required a missing gh, and in both modes a failed
// verification, is exit 3.
func VerifyAttestation(ctx context.Context, a Attestation, archive string) (verified bool, err error) {
	if !a.Required && !a.Auto {
		return false, nil
	}
	gh := a.GH
	if gh == "" {
		p, err := exec.LookPath("gh")
		if err != nil {
			if a.Required {
				return false, fail(ExitVerify, "attestation verification was requested but the gh CLI is not installed; nothing installed")
			}
			return false, nil
		}
		gh = p
	}
	owner := a.Owner
	if owner == "" {
		owner = DefaultOwner
	}
	run := a.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%v: %s", err, lastLine(out.String()))
			}
			return nil
		}
	}
	// A release built before a rename was attested by the repository under its old name
	// (`wy update --version` to an older release); it is checked against that name too
	// (rename.md 2.7).
	repos := []string{owner + "/" + brand.Slug}
	if brand.Renamed() {
		repos = append(repos, owner+"/"+brand.OldSlug())
	}
	var verr error
	for _, repo := range repos {
		args := []string{"attestation", "verify", archive, "--repo", repo, "--signer-workflow", repo + "/" + SignerWorkflow}
		if verr = run(ctx, gh, args...); verr == nil {
			break
		}
	}
	if verr != nil {
		return false, fail(ExitVerify, "attestation verification failed for %s; nothing installed (%v). If gh cannot reach GitHub or is not signed in, fix that, or skip the check with --no-attestation", filepath.Base(archive), verr)
	}
	return true, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// ---- versions ----

type semver struct {
	core [3]int
	pre  []string
}

func parseSemver(v string) (semver, bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	var s semver
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return s, false
		}
		s.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return s, false
		}
		s.pre = strings.Split(pre, ".")
	}
	return s, true
}

// CompareVersions compares two semantic versions (-1, 0, 1). ok is false when either is not a
// semver ("dev" builds), in which case no ordering is claimed.
func CompareVersions(a, b string) (cmp int, ok bool) {
	x, ok1 := parseSemver(a)
	y, ok2 := parseSemver(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := range x.core {
		if x.core[i] != y.core[i] {
			if x.core[i] < y.core[i] {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case len(x.pre) == 0 && len(y.pre) == 0:
		return 0, true
	case len(x.pre) == 0:
		return 1, true
	case len(y.pre) == 0:
		return -1, true
	}
	for i := 0; i < len(x.pre) && i < len(y.pre); i++ {
		p, q := x.pre[i], y.pre[i]
		pn, perr := strconv.Atoi(p)
		qn, qerr := strconv.Atoi(q)
		switch {
		case perr == nil && qerr == nil:
			if pn != qn {
				if pn < qn {
					return -1, true
				}
				return 1, true
			}
		case perr == nil:
			return -1, true
		case qerr == nil:
			return 1, true
		case p != q:
			if p < q {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case len(x.pre) < len(y.pre):
		return -1, true
	case len(x.pre) > len(y.pre):
		return 1, true
	}
	return 0, true
}

// ---- package managers ----

// PackageManager reports whether path (a resolved executable path) belongs to a package
// manager, and the hint to print (machines.md 7.3 step 5).
func PackageManager(path string) (hint string, owned bool) {
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	for _, pre := range []string{"/opt/homebrew", "/usr/local/cellar", "/home/linuxbrew", "/nix/store", "/usr/bin", "/usr/sbin"} {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return "update with brew/winget/your package manager (" + path + " is managed by one)", true
		}
	}
	if strings.Contains(p, "/scoop/apps/") || strings.Contains(p, "/scoop/shims/") {
		return "update with scoop update wy (" + path + " is managed by it)", true
	}
	if strings.Contains(p, "/winget/packages/") || strings.Contains(p, "/winget/links/") {
		return "update with winget (" + path + " is managed by it)", true
	}
	return "", false
}

// ---- running the binary ----

// RunFunc runs a program and returns its standard output. Tests replace it.
type RunFunc func(ctx context.Context, bin string, args ...string) ([]byte, error)

func runBinary(ctx context.Context, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%v: %s", err, lastLine(errb.String()))
	}
	return out.Bytes(), nil
}

// Smoke runs `<bin> version --short`. It must succeed and, when want is not empty, print want
// (machines.md 7.3 step 4: proves the new binary runs here). It returns the version printed.
func Smoke(ctx context.Context, run RunFunc, bin, want string) (string, error) {
	if run == nil {
		run = runBinary
	}
	out, err := run(ctx, bin, "version", "--short")
	if err != nil {
		return "", fail(ExitVerify, "the new binary does not run here (%v); nothing installed", err)
	}
	got := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	if want != "" && got != want {
		return got, fail(ExitVerify, "the new binary reports version %q, expected %q; nothing installed", got, want)
	}
	return got, nil
}

// HubSchemaMax runs `<bin> hub schema --max`: the newest hub schema that binary opens.
func HubSchemaMax(ctx context.Context, run RunFunc, bin string) (int, error) {
	if run == nil {
		run = runBinary
	}
	out, err := run(ctx, bin, "hub", "schema", "--max")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("unexpected output %q", strings.TrimSpace(string(out)))
	}
	return n, nil
}

// ---- platform ----

// Platform is this machine's release platform: runtime.GOOS/GOARCH, with arm64 on Darwin
// under Rosetta (as install.sh does).
func Platform() (goos, goarch string) {
	goos, goarch = runtime.GOOS, runtime.GOARCH
	if goos == "darwin" && goarch == "amd64" {
		if out, err := exec.Command("sysctl", "-n", "sysctl.proc_translated").Output(); err == nil && strings.TrimSpace(string(out)) == "1" {
			goarch = "arm64"
		}
	}
	return
}

// ---- Apply ----

// Options configures Check, Apply and Rollback.
type Options struct {
	Source  Source
	Fetcher Fetcher // nil = Real{}
	// Version is Latest (or empty) or a version.
	Version string
	// Current is the running version (version.Version).
	Current string
	// Exe is the resolved path of the running binary (os.Executable with symlinks resolved).
	Exe string
	// StateDir: downloads go to <StateDir>/update/.
	StateDir string
	// GOOS and GOARCH of the running binary; empty = Platform().
	GOOS, GOARCH string
	// AllowDowngrade lets a target older than Current through.
	AllowDowngrade bool
	// FromFile and SumsFile install a local archive (both required together).
	FromFile, SumsFile string
	Attestation        Attestation
	// HubSchema is the database's schema version when this machine hosts a hub (0 = no hub
	// here); a new binary that cannot open it is refused.
	HubSchema int
	// HubDBErr is set when a hub database exists here but its schema version could not be
	// read: the guard cannot vouch for any binary, so Apply and Rollback refuse (exit 5).
	HubDBErr string
	// Run runs binaries (tests); nil runs them for real.
	Run RunFunc
}

func (o *Options) defaults() {
	if o.Fetcher == nil {
		o.Fetcher = Real{}
	}
	if o.GOOS == "" || o.GOARCH == "" {
		o.GOOS, o.GOARCH = Platform()
	}
	if o.Version == "" {
		o.Version = Latest
	}
}

// Result is what happened. Status is "updated", "current", "checked" or "rolled_back".
type Result struct {
	Status   string `json:"status"`
	From     string `json:"from"`
	To       string `json:"to"`
	Path     string `json:"path,omitempty"`
	Archive  string `json:"archive,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Newer    bool   `json:"newer,omitempty"`
	OldKept  string `json:"old_kept,omitempty"` // the wy.old path to roll back to
	Attested bool   `json:"attested,omitempty"` // the build attestation was checked with gh; false: checksum only
}

func (o Options) check() error {
	if o.Exe == "" {
		return fail(ExitFailed, "cannot tell which wy binary is running")
	}
	if (o.FromFile == "") != (o.SumsFile == "") {
		return fail(ExitUsage, "--from-file and --sums-file go together")
	}
	return nil
}

func (o Options) refuseManaged() error {
	if hint, owned := PackageManager(o.Exe); owned {
		return fail(ExitRefused, "%s", hint)
	}
	return nil
}

func (o Options) release(ctx context.Context) (Release, error) {
	if o.FromFile != "" {
		raw, err := os.ReadFile(o.SumsFile)
		if err != nil {
			return Release{}, fail(ExitDownload, "%v", err)
		}
		return ReleaseFromSums(raw, o.Version)
	}
	return o.Fetcher.Resolve(ctx, o.Source, o.Version)
}

// Check is `wy update --check`: steps 1-2 only. Nothing is downloaded or changed. Result.To is
// the release version, Result.Newer whether it is above Current.
func Check(ctx context.Context, o Options) (Result, error) {
	o.defaults()
	if err := o.check(); err != nil {
		return Result{}, err
	}
	rel, err := o.release(ctx)
	if err != nil {
		return Result{}, err
	}
	name := rel.ArchiveName(o.GOOS, o.GOARCH)
	if _, ok := rel.line(name); !ok {
		return Result{}, fail(ExitVerify, "SHA256SUMS lists no %s", name)
	}
	r := Result{Status: "checked", From: o.Current, To: rel.Version, Archive: name}
	if c, ok := CompareVersions(o.Current, rel.Version); ok {
		r.Newer = c < 0
	} else {
		r.Newer = o.Current != rel.Version
	}
	return r, nil
}

// Apply is `wy update` for one machine (machines.md 7.3 steps 1-6). It does not run the
// post-update steps: the caller starts the new binary for those (RunPostUpdate).
func Apply(ctx context.Context, o Options) (Result, error) {
	o.defaults()
	if err := o.check(); err != nil {
		return Result{}, err
	}
	if err := o.refuseManaged(); err != nil {
		return Result{}, err
	}
	unlock, err := lockUpdate(o.StateDir)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	rel, err := o.release(ctx)
	if err != nil {
		return Result{}, err
	}
	r := Result{From: o.Current, To: rel.Version, Path: o.Exe}
	cmp, ordered := CompareVersions(o.Current, rel.Version)
	if o.Current == rel.Version {
		r.Status = "current"
		return r, nil
	}
	if ordered && cmp > 0 && !o.AllowDowngrade {
		return r, fail(ExitRefused, "%s is older than the installed %s; pass --allow-downgrade to go back", rel.Version, o.Current)
	}

	var arch Archive
	if o.FromFile != "" {
		arch, err = LocalArchive(rel, o.GOOS, o.GOARCH, o.FromFile)
	} else {
		arch, err = o.Fetcher.Fetch(ctx, rel, o.GOOS, o.GOARCH, filepath.Join(o.StateDir, "update"))
	}
	if err != nil {
		return r, err
	}
	r.Archive, r.SHA256 = arch.Name, arch.SHA256
	attested, err := VerifyAttestation(ctx, o.Attestation, arch.Path)
	if err != nil {
		return r, err
	}
	r.Attested = attested
	bin, _, err := o.Fetcher.Extract(arch)
	if err != nil {
		return r, err
	}
	if _, err := Smoke(ctx, o.Run, bin, rel.Version); err != nil {
		return r, err
	}
	if err := o.hubGuard(ctx, bin, "this version"); err != nil {
		return r, err
	}
	if err := Replace(o.Exe, bin, o.GOOS); err != nil {
		return r, err
	}
	r.Status, r.OldKept = "updated", o.Exe+".old"
	cleanup(filepath.Dir(arch.Path), arch, o.FromFile != "")
	return r, nil
}

// hubGuard refuses a binary that cannot open this machine's hub database, and refuses
// everything when the database exists but its version is unknown (fail closed).
func (o Options) hubGuard(ctx context.Context, bin, what string) error {
	if o.HubDBErr != "" {
		return fail(ExitRefused, "the hub database here cannot be read (%s); not changing the binary", o.HubDBErr)
	}
	if o.HubSchema <= 0 {
		return nil
	}
	max, err := HubSchemaMax(ctx, o.Run, bin)
	if err != nil || max < o.HubSchema {
		why := "it does not report which hub schema it opens"
		if err == nil {
			why = fmt.Sprintf("it opens up to v%d", max)
		}
		return fail(ExitRefused, "%s cannot open the hub database (v%d): %s", what, o.HubSchema, why)
	}
	return nil
}

// cleanup removes the downloaded archive and its extraction folder from the update folder
// (a from-file archive belongs to the caller and stays).
func cleanup(updateDir string, a Archive, fromFile bool) {
	if fromFile || filepath.Dir(a.Path) != updateDir || filepath.Base(updateDir) != "update" {
		return
	}
	_ = os.Remove(a.Path)
	_ = os.RemoveAll(a.Path + ".x")
}

// Rollback is `wy update --rollback`: swap <exe>.old back in. The binary that was running
// becomes the new <exe>.old, so a second rollback undoes the first.
func Rollback(ctx context.Context, o Options) (Result, error) {
	o.defaults()
	if o.Exe == "" {
		return Result{}, fail(ExitFailed, "cannot tell which wy binary is running")
	}
	if err := o.refuseManaged(); err != nil {
		return Result{}, err
	}
	unlock, err := lockUpdate(o.StateDir)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	old := rollbackSource(o.Exe)
	if _, err := os.Stat(old); err != nil {
		return Result{}, fail(ExitFailed, "there is no %s to roll back to", old)
	}
	got, err := Smoke(ctx, o.Run, old, "")
	if err != nil {
		return Result{}, fail(ExitFailed, "%s does not run here, not rolling back: %v", old, err)
	}
	// A migration since the update may have moved hub.db past what the old binary opens.
	if err := o.hubGuard(ctx, old, filepath.Base(old)); err != nil {
		return Result{}, err
	}
	if err := swapOld(o.Exe, old, o.GOOS); err != nil {
		return Result{}, err
	}
	return Result{Status: "rolled_back", From: o.Current, To: got, Path: o.Exe, OldKept: old}, nil
}

// rollbackSource is <exe>.old, or, when there is none and exe has the command's current name,
// the <old cli>.old an update from before the rename kept next to it (legacy compat,
// rename.md 2.13). The binary that was running then becomes that file.
func rollbackSource(exe string) string {
	old := exe + ".old"
	if _, err := os.Stat(old); err == nil || !brand.CLIRenamed() {
		return old
	}
	base := filepath.Base(exe)
	ext := strings.TrimPrefix(base, brand.CLI)
	if ext == base || (ext != "" && ext != ".exe") {
		return old
	}
	legacy := filepath.Join(filepath.Dir(exe), brand.OldCLI()+ext+".old")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return old
}

// ---- replacing the binary ----

func copyFileSync(dst, src string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Chmod(dst, perm)
}

func tmpName(exe string) string {
	return filepath.Join(filepath.Dir(exe), fmt.Sprintf(".wy.%d.tmp", os.Getpid()))
}

// Replace puts newBin in place of exe (machines.md 7.3 step 6) and keeps the old binary as
// <exe>.old. Unix: copy exe to exe.old, write a temp file next to exe, rename it over exe
// (a running hook keeps its old inode). Windows: rename exe to exe.old (allowed while it
// runs), move the new file in. When it fails, exe is as it was and the error says so.
func Replace(exe, newBin, goos string) error {
	st, err := os.Stat(exe)
	if err != nil {
		return fail(ExitFailed, "cannot replace %s: %v", exe, err)
	}
	if !st.Mode().IsRegular() {
		return fail(ExitFailed, "cannot replace %s: not a regular file", exe)
	}
	tmp := tmpName(exe)
	defer os.Remove(tmp)
	old := exe + ".old"
	if goos == "windows" {
		return replaceWindows(exe, old, newBin, tmp)
	}
	// The kept copy is written through a temp name so wy.old is always a complete binary.
	oldTmp := tmp + ".old"
	defer os.Remove(oldTmp)
	if err := copyFileSync(oldTmp, exe, st.Mode().Perm()|0o100); err != nil {
		return fail(ExitFailed, "cannot keep %s: %v; wy is unchanged (wy.old not written)", old, err)
	}
	if err := copyFileSync(tmp, newBin, 0o755); err != nil {
		return fail(ExitFailed, "cannot write the new binary next to %s: %v; wy is unchanged", exe, err)
	}
	if err := os.Rename(oldTmp, old); err != nil {
		return fail(ExitFailed, "cannot write %s: %v; wy is unchanged", old, err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		return fail(ExitFailed, "cannot replace %s: %v; wy is unchanged (%s is a copy of it)", exe, err, old)
	}
	return nil
}

func replaceWindows(exe, old, newBin, tmp string) error {
	if err := copyFileSync(tmp, newBin, 0o755); err != nil {
		return fail(ExitFailed, "cannot write the new binary next to %s: %v; wy is unchanged", exe, err)
	}
	_ = os.Remove(old) // an older .old that is not running any more
	if err := os.Rename(exe, old); err != nil {
		return fail(ExitFailed, "cannot move %s aside: %v; wy is unchanged", exe, err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fail(ExitFailed, "cannot move the new binary in (%v) and cannot restore %s (%v): copy %s back by hand", err, exe, rerr, old)
		}
		return fail(ExitFailed, "cannot move the new binary in: %v; the old wy was restored", err)
	}
	return nil
}

func swapOld(exe, old, goos string) error {
	hold := exe + ".swap"
	_ = os.Remove(hold)
	if goos == "windows" {
		if err := os.Rename(exe, hold); err != nil {
			return fail(ExitFailed, "cannot move %s aside: %v; nothing changed", exe, err)
		}
		if err := os.Rename(old, exe); err != nil {
			_ = os.Rename(hold, exe)
			return fail(ExitFailed, "cannot restore %s: %v; the current wy was kept", old, err)
		}
		if err := os.Rename(hold, old); err != nil {
			return fail(ExitFailed, "rolled back, but cannot keep the replaced binary as %s: %v", old, err)
		}
		return nil
	}
	// Unix: copy the kept binary to a temp name and rename it over exe; the replaced one
	// becomes wy.old.
	tmp := tmpName(exe)
	defer os.Remove(tmp)
	if err := copyFileSync(tmp, old, 0o755); err != nil {
		return fail(ExitFailed, "cannot read %s: %v; nothing changed", old, err)
	}
	if err := copyFileSync(hold, exe, 0o755); err != nil {
		return fail(ExitFailed, "cannot keep the current binary: %v; nothing changed", err)
	}
	defer os.Remove(hold)
	if err := os.Rename(tmp, exe); err != nil {
		return fail(ExitFailed, "cannot replace %s: %v; nothing changed", exe, err)
	}
	if err := os.Rename(hold, old); err != nil {
		return fail(ExitFailed, "rolled back, but cannot keep the replaced binary as %s: %v", old, err)
	}
	return nil
}

// CleanupOld deletes a leftover <exe>.old on Windows, where it only exists because a running
// image cannot be overwritten. It must not run before --rollback or --check (the file is the
// rollback target); Replace removes a stale one itself. It does nothing elsewhere.
func CleanupOld(exe, goos string) {
	if goos == "windows" {
		_ = os.Remove(exe + ".old")
	}
}

// ---- post-update steps ----

// PostStep is one thing the new binary does after an update (machines.md 7.3 step 7): refresh
// the plugin marketplace, rewrite the timer and hub service units, restart the hub service,
// send machine_info. cmd/wy registers the real ones with RegisterPostUpdate; this
// package never imports them.
type PostStep struct {
	Name string
	// Run gets the version that was replaced. Its returned detail is printed on success.
	Run func(ctx context.Context, oldVersion string) (detail string, err error)
}

// StepResult is the outcome of one post-update step.
type StepResult struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

var postSteps []PostStep

// RegisterPostUpdate adds a step; steps run in registration order. Call it from an init().
func RegisterPostUpdate(s PostStep) { postSteps = append(postSteps, s) }

// PostUpdateSteps returns the registered steps in run order.
func PostUpdateSteps() []PostStep { return append([]PostStep(nil), postSteps...) }

// RunPostUpdate runs every registered step. A failing step does not stop the others.
func RunPostUpdate(ctx context.Context, oldVersion string) []StepResult {
	out := make([]StepResult, 0, len(postSteps))
	for _, s := range postSteps {
		out = append(out, runStep(ctx, s, oldVersion))
	}
	return out
}

// stepTimeout bounds one post-update step (a hung `claude` or service manager call).
const stepTimeout = 3 * time.Minute

// runStep runs one step with a time limit; an error or a panic becomes its Error so the
// remaining steps still run.
func runStep(ctx context.Context, s PostStep, oldVersion string) (r StepResult) {
	r.Name = s.Name
	defer func() {
		if p := recover(); p != nil {
			r.Error = fmt.Sprintf("panic: %v", p)
		}
	}()
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	d, err := s.Run(sctx, oldVersion)
	r.Detail = d
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// PostUpdateFailed is true when any step failed.
func PostUpdateFailed(rs []StepResult) bool {
	for _, r := range rs {
		if r.Error != "" {
			return true
		}
	}
	return false
}

// PostUpdateError wraps a failed post-update run: the binary was replaced and stays so.
func PostUpdateError(err error) error {
	return &Error{Code: ExitFailed, Err: fmt.Errorf("updated, but the post-update steps failed: %v (wy.old is kept: wy update --rollback undoes the update)", err)}
}

// postUpdateTimeout bounds the whole post-update process.
const postUpdateTimeout = 10 * time.Minute

// StartPostUpdate runs `<exe> update --post-update <oldVersion>` (the new binary does the
// work, so it uses its own code). stdout and stderr receive its output.
func StartPostUpdate(ctx context.Context, exe, oldVersion string, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, postUpdateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "update", "--post-update", oldVersion)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("post-update steps failed (exit %d)", ee.ExitCode())
		}
		return err
	}
	return nil
}

// updateLockStale is how old a lock file must be before it is taken over (a crashed update).
const updateLockStale = 15 * time.Minute

// lockUpdate takes <StateDir>/update.lock so two `wy update` runs cannot race on wy.old. It
// is a create-exclusive file (works on every OS); one older than updateLockStale is stale.
// An empty stateDir takes no lock.
func lockUpdate(stateDir string) (unlock func(), err error) {
	if stateDir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fail(ExitFailed, "%v", err)
	}
	p := filepath.Join(stateDir, "update.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { _ = os.Remove(p) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fail(ExitFailed, "%v", err)
		}
		st, serr := os.Stat(p)
		if serr == nil && time.Since(st.ModTime()) < updateLockStale {
			return nil, fail(ExitFailed, "another wy update is running (%s); wait for it or remove the file if it crashed", p)
		}
		_ = os.Remove(p)
	}
	return nil, fail(ExitFailed, "cannot take the update lock %s", p)
}
