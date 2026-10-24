package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/pull"
	"github.com/mraj602/watchyard/internal/hub/store"
)

func cliKey(seed byte) string {
	blob := []byte{0, 0, 0, 11}
	blob = append(blob, "ssh-ed25519"...)
	blob = append(blob, 0, 0, 0, 32)
	for i := 0; i < 32; i++ {
		blob = append(blob, seed+byte(i))
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
}

type pullFixture struct {
	t   *testing.T
	db  string
	dir string
}

func newPullFixture(t *testing.T) *pullFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("WY_HOME", root)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	db := filepath.Join(root, "hub.db")
	st, err := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	// ssh-keygen and ssh-keyscan are faked: no real binary runs.
	pullRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ssh-keygen":
			for i, a := range args {
				if a == "-f" {
					_ = os.WriteFile(args[i+1], []byte("PRIVATE"), 0o600)
					_ = os.WriteFile(args[i+1]+".pub", []byte("ssh-ed25519 AAAAFAKEPUB watchyard-pull@hub\n"), 0o644)
				}
			}
			return nil, nil
		case "ssh-keyscan":
			return []byte("# h\nh " + cliKey(1) + "\nh " + cliKey(50) + "\n"), nil
		}
		return nil, errors.New("unexpected " + name)
	}
	t.Cleanup(func() { pullRunner, pullStarter = nil, nil })
	return &pullFixture{t: t, db: db, dir: filepath.Join(root, "pull")}
}

func (f *pullFixture) run(stdin string, args ...string) (string, string, error) {
	var out, errb bytes.Buffer
	err := PullIn(strings.NewReader(stdin), append(args, "--db", f.db, "--dir", f.dir), &out, &errb)
	return out.String(), errb.String(), err
}

const cliMachine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"

func TestPullAddListKeyTrustRemove(t *testing.T) {
	f := newPullFixture(t)
	out, errs, err := f.run(cliKey(1)+" root@h\n", "add", "gpu-box", "--ssh", "me@gpu-box.ts.net", "--port", "2222", "--machine", cliMachine,
		"--host-keys-stdin", "--interval", "30s", "--json")
	if err != nil {
		t.Fatal(err, errs)
	}
	var added struct {
		Target    string `json:"target"`
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || !strings.HasPrefix(added.Target, "pt_") || added.Name != "gpu-box" || !strings.HasPrefix(added.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("%q %v", out, err)
	}
	if st, err := os.Stat(filepath.Join(f.dir, "id_ed25519")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key %v %v", st, err)
	}

	out, _, err = f.run("", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []pullRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("%q %v", out, err)
	}
	r := rows[0]
	if r.Name != "gpu-box" || r.Target != "me@gpu-box.ts.net:2222" || r.State != "new" || !r.Enabled || r.IntervalS != 30 || r.Machine == nil || *r.Machine != cliMachine {
		t.Fatalf("%+v", r)
	}
	if text, _, _ := f.run("", "list"); !strings.Contains(text, "gpu-box") || !strings.Contains(text, "STATE") {
		t.Fatalf("list text: %q", text)
	}

	if out, _, err = f.run("", "key", "--json"); err != nil || !strings.Contains(out, `"public_key":"ssh-ed25519 AAAAFAKEPUB`) {
		t.Fatalf("%q %v", out, err)
	}
	// Adding again with the same name fails.
	if _, _, err := f.run(cliKey(1)+"\n", "add", "gpu-box", "--ssh", "me@other", "--host-keys-stdin"); err == nil {
		t.Fatal("duplicate name accepted")
	}

	// trust re-pins and sets state new.
	st, _ := store.Open(context.Background(), f.db, store.Options{NoBackupBeforeMigrate: true})
	_ = st.RecordPullRun(context.Background(), added.Target, store.PullResult{State: store.PullStateHostKey, Error: "changed"})
	st.Close()
	if out, errs, err = f.run(cliKey(9)+"\n", "trust", "gpu-box", "--host-keys-stdin"); err != nil || !strings.Contains(out, "Pinned 1") {
		t.Fatal(out, errs, err)
	}
	st, _ = store.Open(context.Background(), f.db, store.Options{NoBackupBeforeMigrate: true})
	tg, _ := st.PullTargetByID(context.Background(), added.Target)
	st.Close()
	if tg.State != "new" || len(tg.HostKeys) != 1 || tg.HostKeys[0] != cliKey(9) {
		t.Fatalf("%+v", tg)
	}

	if _, _, err := f.run("", "disable", "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if out, _, _ = f.run("", "list", "--json"); !strings.Contains(out, `"state":"disabled"`) || !strings.Contains(out, `"enabled":false`) {
		t.Fatal(out)
	}
	if _, _, err := f.run("", "enable", "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.run("", "remove", "gpu-box", "--revoke"); err != nil {
		t.Fatal(err)
	}
	if out, _, _ = f.run("", "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("%q", out)
	}
	if _, _, err := f.run("", "remove", "gpu-box"); err == nil {
		t.Fatal("removing twice")
	}
}

func TestPullAddHostKeyRules(t *testing.T) {
	f := newPullFixture(t)
	// Without keys and without a terminal: refused, never learned.
	if _, _, err := f.run("", "add", "a", "--ssh", "u@h"); err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("%v", err)
	}
	// --yes never accepts scanned keys on its own.
	if _, _, err := f.run("", "add", "a", "--ssh", "u@h", "--yes"); err == nil {
		t.Fatal("--yes pinned unverified keys")
	}
	// Both sources: refused.
	if _, _, err := f.run(cliKey(1)+"\n", "add", "a", "--ssh", "u@h", "--host-keys-stdin", "--fingerprint", "SHA256:"+strings.Repeat("A", 43)); err == nil {
		t.Fatal("both flags accepted")
	}
	// A fingerprint pins only the matching scanned key.
	fp, _ := pull.Fingerprint(cliKey(50))
	if _, errs, err := f.run("", "add", "a", "--ssh", "u@h", "--fingerprint", fp); err != nil {
		t.Fatal(err, errs)
	}
	st, _ := store.Open(context.Background(), f.db, store.Options{NoBackupBeforeMigrate: true})
	tg, err := st.PullTargetByName(context.Background(), "a")
	st.Close()
	if err != nil || len(tg.HostKeys) != 1 || tg.HostKeys[0] != cliKey(50) {
		t.Fatalf("%+v %v", tg, err)
	}
	// A fingerprint nothing matches pins nothing.
	if _, _, err := f.run("", "add", "b", "--ssh", "u@h", "--fingerprint", "SHA256:"+strings.Repeat("A", 43)); err == nil {
		t.Fatal("pinned a non-matching fingerprint")
	}
	// Bad input.
	for _, args := range [][]string{
		{"add", "bad name", "--ssh", "u@h"},
		{"add", "c", "--ssh", "u@h", "--interval", "5s"},
		{"add", "c", "--ssh", "u@-oProxyCommand=x"},
		{"add", "c", "--ssh", "u@h", "--machine", "nope"},
	} {
		if _, _, err := f.run(cliKey(1)+"\n", append(args, "--host-keys-stdin")...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestPullRunInProcessWithFakeSSH(t *testing.T) {
	f := newPullFixture(t)
	if _, _, err := f.run(cliKey(1)+"\n", "add", "gpu", "--ssh", "me@gpu", "--host-keys-stdin"); err != nil {
		t.Fatal(err)
	}
	var argv []string
	pullStarter = func(_ context.Context, a []string) (pull.Proc, error) {
		argv = a
		return failedProc{}, nil
	}
	out, errs, err := f.run("", "run", "gpu", "--json")
	if err != nil {
		t.Fatal(err, errs)
	}
	if len(argv) != 8 || argv[0] != "-F" || argv[7] != "--stdio" {
		t.Fatalf("argv %q", argv)
	}
	var rows []pullRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].State != "auth" || rows[0].LastError == nil || !strings.Contains(*rows[0].LastError, "Permission denied") {
		t.Fatalf("%q %v", out, err)
	}
	if _, _, err := f.run("", "run", "nope"); err == nil {
		t.Fatal("unknown target")
	}
	// --wait turns a bad end state into an error.
	if _, _, err := f.run("", "run", "gpu", "--wait"); err == nil {
		t.Fatal("--wait must fail when the run ends in auth")
	}
}

type failedProc struct{}

func (failedProc) Stdin() io.WriteCloser { return nopCloser{io.Discard} }
func (failedProc) Stdout() io.Reader     { return strings.NewReader("") }
func (failedProc) Kill()                 {}
func (failedProc) Wait() (int, string, error) {
	return 255, "me@gpu: Permission denied (publickey).", nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func TestPullUsageAndUnknown(t *testing.T) {
	f := newPullFixture(t)
	for _, args := range [][]string{{}, {"bogus"}, {"add"}, {"list", "extra"}, {"enable"}} {
		var out, errb bytes.Buffer
		err := PullIn(strings.NewReader(""), append(args, "--db", f.db), &out, &errb)
		if !errors.Is(err, ErrUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
}
