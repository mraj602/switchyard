package pull_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/pull"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// ssh expands %-tokens in file names, so a state folder with a % in it is written doubled.
func TestWriteConfigEscapesPercentInPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "100% state")
	tg := store.PullTarget{ID: "pt_01JA", Name: "gpu", Host: "gpu-box", Port: 22, User: "me", HostKeys: []string{hostKeyA}}
	if err := pull.WriteConfig(dir, []store.PullTarget{tg}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "ssh_config"))
	want := `UserKnownHostsFile "` + strings.ReplaceAll(filepath.Join(dir, "known_hosts"), "%", "%%") + `"`
	if !strings.Contains(string(cfg), want) {
		t.Fatalf("ssh_config does not escape %%:\n%s", cfg)
	}
}

func TestWriteConfigGeneratesTheDocumentedBlock(t *testing.T) {
	dir := t.TempDir()
	tgs := []store.PullTarget{
		{ID: "pt_01JA", Name: "gpu", Host: "gpu-box.example.ts.net", Port: 22, User: "me", HostKeys: []string{hostKeyA}},
		{ID: "pt_01JB", Name: "odd", Host: "10.0.0.7", Port: 2222, User: "dev", HostKeys: []string{hostKeyA, hostKeyB}},
	}
	if err := pull.WriteConfig(dir, tgs); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "ssh_config"))
	for _, want := range []string{
		"Host sy-pt_01JA\n  HostName gpu-box.example.ts.net\n  Port 22\n  User me\n  HostKeyAlias sy-pt_01JA\n",
		"  UserKnownHostsFile " + filepath.Join(dir, "known_hosts") + "\n",
		"  GlobalKnownHostsFile /dev/null\n  StrictHostKeyChecking yes\n  UpdateHostKeys no\n",
		"  IdentityFile " + filepath.Join(dir, "id_ed25519") + "\n  IdentitiesOnly yes\n  IdentityAgent none\n  BatchMode yes\n",
		"  ForwardAgent no\n  ForwardX11 no\n  ClearAllForwardings yes\n  RequestTTY no\n  ControlMaster no\n  ControlPath none\n",
		"  ConnectTimeout 10\n  ServerAliveInterval 15\n  ServerAliveCountMax 2\n  LogLevel ERROR\n",
		"Host sy-pt_01JB\n  HostName 10.0.0.7\n  Port 2222\n",
	} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("ssh_config misses %q:\n%s", want, cfg)
		}
	}
	// ProxyJump is never written.
	if strings.Contains(string(cfg), "ProxyJump") {
		t.Fatalf("ProxyJump count:\n%s", cfg)
	}
	if strings.Contains(string(cfg), "accept-new") || strings.Contains(string(cfg), "StrictHostKeyChecking no") {
		t.Fatal("host key checking turned off")
	}
	kh, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	for _, want := range []string{"sy-pt_01JA " + hostKeyA + "\n", "sy-pt_01JB " + hostKeyA + "\n", "sy-pt_01JB " + hostKeyB + "\n", "[sy-pt_01JB]:2222 " + hostKeyB + "\n"} {
		if !strings.Contains(string(kh), want) {
			t.Fatalf("known_hosts misses %q:\n%s", want, kh)
		}
	}
	if strings.Contains(string(kh), "[sy-pt_01JA]") {
		t.Fatal("default port gets no bracket form")
	}
	for _, f := range []string{"ssh_config", "known_hosts"} {
		if st, _ := os.Stat(filepath.Join(dir, f)); st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", f, st.Mode())
		}
	}
	// Rewriting with a target gone removes it.
	if err := pull.WriteConfig(dir, tgs[:1]); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(filepath.Join(dir, "ssh_config"))
	if strings.Contains(string(cfg), "pt_01JB") {
		t.Fatal("removed target still in ssh_config")
	}
}

func TestWriteConfigRefusesInjection(t *testing.T) {
	bad := []store.PullTarget{
		{ID: "pt_1", Host: "host\n  ProxyCommand evil", Port: 22, User: "me"},
		{ID: "pt_2", Host: "good", Port: 22, User: "me extra"},
		{ID: "pt_3", Host: "-oProxyCommand=evil", Port: 22, User: "me"},
		{ID: "pt_4", Host: "good", Port: 22, User: "me", Jump: "a b"},
		{ID: "pt_5", Host: "good", Port: 22, User: "-oBad"},
		{ID: "pt_6", Host: "good", Port: 0, User: "me"},
		{ID: "pt_7", Host: "good", Port: 22, User: "me", RemoteSy: "sy; rm -rf /"},
	}
	for _, tg := range bad {
		if err := pull.ValidateTarget(tg); err == nil {
			t.Errorf("%+v accepted", tg)
		}
	}
	dir := t.TempDir()
	good := store.PullTarget{ID: "pt_ok", Name: "ok", Host: "good.example", Port: 22, User: "me", HostKeys: []string{hostKeyA}}
	err := pull.WriteConfig(dir, append(bad, good))
	if err == nil {
		t.Fatal("want the bad targets reported")
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "ssh_config"))
	if strings.Contains(string(cfg), "evil") || !strings.Contains(string(cfg), "Host sy-pt_ok") {
		t.Fatalf("ssh_config:\n%s", cfg)
	}
}

func TestParseHostKeysAndFingerprint(t *testing.T) {
	in := "# from the probe\n" + hostKeyA + " root@gpu-box\n\n" + hostKeyB + "\n" + hostKeyA + "\n"
	keys, err := pull.ParseHostKeys(strings.NewReader(in))
	if err != nil || len(keys) != 2 || keys[0] != hostKeyA || keys[1] != hostKeyB {
		t.Fatalf("%v %v", keys, err)
	}
	// ssh-keyscan lines carry a host first.
	keys, err = pull.ParseHostKeys(strings.NewReader("# gpu-box:22 SSH-2.0-OpenSSH_9\ngpu-box " + hostKeyA + "\n"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("%v %v", keys, err)
	}
	for _, bad := range []string{"", "not a key\n", "ssh-ed25519 !!!notbase64\n", "ssh-rsa " + strings.Fields(hostKeyA)[1] + "\n"} {
		if _, err := pull.ParseHostKeys(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	fp, err := pull.Fingerprint(hostKeyA)
	if err != nil || !strings.HasPrefix(fp, "SHA256:") || len(fp) != 7+43 {
		t.Fatalf("%q %v", fp, err)
	}
	if pinned, err := pull.PinByFingerprint([]string{hostKeyA, hostKeyB}, fp); err != nil || len(pinned) != 1 || pinned[0] != hostKeyA {
		t.Fatalf("%v %v", pinned, err)
	}
	if _, err := pull.PinByFingerprint([]string{hostKeyB}, fp); err == nil {
		t.Fatal("a key with another fingerprint was pinned")
	}
}

func TestFingerprintMatchesSSHKeygen(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := t.TempDir()
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, "k")).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(filepath.Join(dir, "k.pub"))
	keys, err := pull.ParseHostKeys(strings.NewReader(string(pub)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := pull.Fingerprint(keys[0])
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("ssh-keygen", "-lf", filepath.Join(dir, "k.pub")).Output()
	if !strings.Contains(string(out), got) {
		t.Fatalf("fingerprint %s, ssh-keygen says %s", got, out)
	}
}

func TestScanWithFakeRunner(t *testing.T) {
	var gotName string
	var gotArgs []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("# gpu-box:2222 SSH-2.0-OpenSSH_9.6\ngpu-box " + hostKeyA + "\n"), nil
	}
	keys, err := pull.Scan(context.Background(), run, "gpu-box", 2222)
	if err != nil || len(keys) != 1 || keys[0] != hostKeyA {
		t.Fatalf("%v %v", keys, err)
	}
	if gotName != "ssh-keyscan" || strings.Join(gotArgs, " ") != "-T 10 -p 2222 -- gpu-box" {
		t.Fatalf("%s %q", gotName, gotArgs)
	}
	_, err = pull.Scan(context.Background(), func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("down") }, "h", 22)
	if err == nil {
		t.Fatal("scan error swallowed")
	}
}

func TestEnsureKeyMakesAndKeepsTheKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := filepath.Join(t.TempDir(), "pull")
	pub, err := pull.EnsureKey(context.Background(), dir, "hub", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, "switchyard-pull@hub") {
		t.Fatalf("%q", pub)
	}
	st, err := os.Stat(filepath.Join(dir, "id_ed25519"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("private key %v %v", st, err)
	}
	if d, _ := os.Stat(dir); d.Mode().Perm() != 0o700 {
		t.Fatalf("pull dir mode %v", d.Mode())
	}
	again, err := pull.EnsureKey(context.Background(), dir, "other", nil)
	if err != nil || again != pub {
		t.Fatalf("an existing key must be kept: %q %v", again, err)
	}
	got, err := pull.PublicKey(dir)
	if err != nil || got != pub {
		t.Fatalf("%q %v", got, err)
	}
	// Lost .pub is rebuilt from the private key.
	_ = os.Remove(filepath.Join(dir, "id_ed25519.pub"))
	if rebuilt, err := pull.EnsureKey(context.Background(), dir, "hub", nil); err != nil || !strings.HasPrefix(rebuilt, "ssh-ed25519 ") {
		t.Fatalf("%q %v", rebuilt, err)
	}
}

func TestEnsureKeyUsesKeygenWithoutSecretsInArgv(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		// Pretend to be ssh-keygen: make both files.
		for i, a := range args {
			if a == "-f" {
				_ = os.WriteFile(args[i+1], []byte("PRIVATE"), 0o600)
				_ = os.WriteFile(args[i+1]+".pub", []byte("ssh-ed25519 AAAA switchyard-pull@h\n"), 0o644)
			}
		}
		return nil, nil
	}
	if _, err := pull.EnsureKey(context.Background(), t.TempDir(), "h", run); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0][0] != "ssh-keygen" || !strings.Contains(strings.Join(calls[0], " "), "-t ed25519 -N  -C switchyard-pull@h") {
		t.Fatalf("%q", calls)
	}
}

// Review finding 11: a jump host cannot be pinned, so it is refused with a clear reason
// instead of being written as an unpinned ProxyJump.
func TestProxyJumpIsRefused(t *testing.T) {
	tg := store.PullTarget{ID: "pt_01JA", Name: "gpu", Host: "gpu-box", Port: 22, User: "me", Jump: "bastion", HostKeys: []string{hostKeyA}}
	err := pull.ValidateTarget(tg)
	if err == nil || !strings.Contains(err.Error(), "ProxyJump") {
		t.Fatalf("err = %v", err)
	}
	dir := t.TempDir()
	if err := pull.WriteConfig(dir, []store.PullTarget{tg}); err == nil {
		t.Fatal("WriteConfig accepted a jump host")
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "ssh_config"))
	if strings.Contains(string(cfg), "ProxyJump") || strings.Contains(string(cfg), "gpu-box") {
		t.Fatalf("target with a jump host was written:\n%s", cfg)
	}
}
