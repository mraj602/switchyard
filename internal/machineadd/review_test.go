package machineadd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/sshx/sshxtest"
)

// Regression tests for the machine add / ssh side.

func TestSameNameMachinesDoNotCollide(t *testing.T) {
	r := &Registry{V: 1}
	now := time.Now()
	r.Upsert(Entry{Name: "ubuntu", Machine: "m_AAAA1111", SSH: "a"}, now)
	if got := r.UniqueName("ubuntu", "m_AAAA1111"); got != "ubuntu" {
		t.Fatalf("a machine's own name changed: %q", got)
	}
	r.Upsert(Entry{Name: "ubuntu", Machine: "m_BBBB2222", SSH: "b"}, now)
	if len(r.Machines) != 2 {
		t.Fatalf("a same-name machine replaced the first: %+v", r.Machines)
	}
	if got := r.UniqueName("ubuntu", "m_CCCC3333"); got == "ubuntu" || !strings.HasPrefix(got, "ubuntu-") {
		t.Fatalf("unique name %q", got)
	}
}

func TestAddSecondMachineSameDefaultName(t *testing.T) {
	w := newWorld(t)
	reg := mustReg(t, w.state)
	reg.Upsert(Entry{Name: "gpu-box", Machine: "m_01OTHER", SSH: "other", Reach: ReachDirect}, w.now)
	reg.Save()
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if sum.Name == "gpu-box" || !strings.HasPrefix(sum.Name, "gpu-box-") {
		t.Fatalf("name %q collides with the other machine", sum.Name)
	}
	reg = mustReg(t, w.state)
	if len(reg.Machines) != 2 {
		t.Fatalf("registry %+v", reg.Machines)
	}
	if e, ok := reg.FindMachine("m_01OTHER"); !ok || e.SSH != "other" {
		t.Fatalf("the other machine was overwritten: %+v", reg.Machines)
	}
	// An explicit name that another machine owns is refused.
	_, err = Add(context.Background(), Options{Host: testHost, Name: "gpu-box", Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env())
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitUsage {
		t.Fatalf("want exit 2, got %v", err)
	}
}

func TestAddPullTargetByMachine(t *testing.T) {
	w := newWorld(t)
	other := "m_01OTHER"
	adds := 0
	w.admin.answer = func(args []string, stdin []byte) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "pull list":
			return []byte(`[{"id":"pt_OTHER","name":"gpu-box","machine":"` + other + `","target":"x","state":"ok"},` +
				`{"id":"pt_MINE","name":"gpu-box-mine","machine":"` + testMID + `","target":"y","state":"ok"}]`), nil
		case "pull key":
			return []byte(`{"public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub"}`), nil
		case "pull add":
			adds++
			return []byte(`{"target":"pt_NEW","name":"` + args[2] + `","public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub"}`), nil
		}
		return hubAnswers(w.now)(args, stdin)
	}
	pt := PullTarget{Name: "gpu-box", User: "me", Host: "gpu-box", Port: 22}
	got, err := AddPullTarget(context.Background(), w.admin, pt, testMID, []string{"ssh-ed25519 AAAA"})
	if err != nil || got.Target != "pt_MINE" || adds != 0 {
		t.Fatalf("this machine's own target not reused: %+v %v (adds %d)", got, err, adds)
	}
	got, err = AddPullTarget(context.Background(), w.admin, pt, "m_01THIRD0000", []string{"ssh-ed25519 AAAA"})
	if err != nil || adds != 1 || got.Target != "pt_NEW" || got.Name == "gpu-box" {
		t.Fatalf("a machine with a taken name must get its own target: %+v %v", got, err)
	}
}

func TestRepointForwardMachine(t *testing.T) {
	w := newWorld(t)
	os.MkdirAll(filepath.Join(w.remote, ".local/bin"), 0o755)
	os.WriteFile(filepath.Join(w.remote, ".local/bin/sy"), []byte(fakeSy), 0o755)
	f := Forward{Host: HostPattern(testHost), Machine: testMID, Name: "gpu-box", Port: 47830, Target: "old.example:443"}
	res, err := WriteForward(w.home, w.state, f, "test", w.now)
	if err != nil {
		t.Fatal(err)
	}
	reg := mustReg(t, w.state)
	reg.Upsert(Entry{Name: "gpu-box", Machine: testMID, SSH: testHost, Reach: ReachForward, Port: 47830, ForwardTarget: "old.example:443",
		ForwardSet: res.Set, HubURL: "https://old.example"}, w.now)
	reg.Save()
	me := &ManageEnv{SSH: w.env().SSH, Admin: w.admin, StateDir: w.state, Home: w.home}
	if _, err := Repoint(context.Background(), "https://new.example", nil, me); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(w.read("connect.args")); got != "connect https://new.example --keep-token --dial 127.0.0.1:47830 --reach forward" {
		t.Fatalf("connect args %q", got)
	}
	conf, _ := os.ReadFile(filepath.Join(w.home, ".ssh", ForwardConf))
	if !strings.Contains(string(conf), "RemoteForward 127.0.0.1:47830 new.example:443\n") || strings.Contains(string(conf), "old.example") {
		t.Fatalf("forward block not repointed:\n%s", conf)
	}
	e, _ := mustReg(t, w.state).Find("gpu-box")
	if e.ForwardTarget != "new.example:443" || e.HubURL != "https://new.example" {
		t.Fatalf("registry %+v", e)
	}

	// A failing remote puts the old block back: the machine still talks to the old hub.
	os.WriteFile(filepath.Join(w.remote, "connect.exit"), []byte("1"), 0o644)
	if _, err := Repoint(context.Background(), "https://newer.example", nil, me); err == nil {
		t.Fatal("failure not reported")
	}
	conf, _ = os.ReadFile(filepath.Join(w.home, ".ssh", ForwardConf))
	if !strings.Contains(string(conf), "new.example:443") || strings.Contains(string(conf), "newer.example") {
		t.Fatalf("block not restored after a failed repoint:\n%s", conf)
	}
}

func TestLeavingPullRemovesKeyAndTarget(t *testing.T) {
	w := newWorld(t)
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachPull, Binary: w.bin}, w.env()); err != nil {
		t.Fatalf("pull add: %v", err)
	}
	w.admin.calls = nil
	// Now the same machine goes direct. The hub's pull key must leave authorized_keys.
	w.probe(ProbeReport{V: 1, Machine: testMID, Name: "gpu-box", Hub: &ProbeHub{URL: testHubURL, Reachable: true}})
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env()); err != nil {
		t.Fatalf("direct add: %v %+v", err, w.events)
	}
	if got := w.read("authorize.args"); !strings.Contains(got, "authorize-pull --remove --target pt_01ABC") {
		t.Fatalf("authorize args %q", got)
	}
	if w.admin.called("pull remove gpu-box") == nil {
		t.Fatalf("no pull remove on the hub: %q", w.admin.calls)
	}
	if e, _ := mustReg(t, w.state).Find("gpu-box"); e.PullTarget != "" || e.Reach != ReachDirect {
		t.Fatalf("registry %+v", e)
	}
}

func TestLeavingPullKeepsTargetUntilRemoved(t *testing.T) {
	w := newWorld(t)
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachPull, Binary: w.bin}, w.env()); err != nil {
		t.Fatalf("pull add: %v", err)
	}
	base := w.admin.answer
	w.admin.answer = func(args []string, stdin []byte) ([]byte, error) {
		if args[0] == "pull" && args[1] == "remove" {
			return nil, &AdminError{Args: args, Exit: 1, Stderr: "hub down"}
		}
		return base(args, stdin)
	}
	w.probe(ProbeReport{V: 1, Machine: testMID, Name: "gpu-box", Hub: &ProbeHub{URL: testHubURL, Reachable: true}})
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env()); err != nil {
		t.Fatalf("direct add: %v", err)
	}
	if e, _ := mustReg(t, w.state).Find("gpu-box"); e.PullTarget != "pt_01ABC" {
		t.Fatalf("pull target forgotten although the hub still has it: %+v", e)
	}
	// A rerun finishes the job once the hub answers.
	w.admin.answer = base
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachDirect, HubURL: testHubURL, Binary: w.bin, Yes: true}, w.env()); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if e, _ := mustReg(t, w.state).Find("gpu-box"); e.PullTarget != "" {
		t.Fatalf("pull target left after the rerun: %+v", e)
	}
}

func TestOwnBlockIsNotAdopted(t *testing.T) {
	w := newWorld(t)
	e := w.env()
	e.OperatorHub = testHubURL
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachForward, Binary: w.bin}, e); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// ssh -G now shows sy's own block (the Include is in effect).
	w.fake.SetG(testHost, "hostname gpu-box\nuser me\nport 22\nremoteforward [127.0.0.1]:47830 hub.example:8443\n")
	w.probe(ProbeReport{V: 1, Machine: testMID, Name: "gpu-box", Free: []int{47830, 47831}, Restrict: true})
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachForward, Binary: w.bin, Port: 47831}, e)
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if sum.Port != 47831 {
		t.Fatalf("--port ignored because sy's own forward looked adopted: port %d", sum.Port)
	}
	ent, _ := mustReg(t, w.state).Find("gpu-box")
	if ent.ForwardAdopted {
		t.Fatalf("sy's own block marked adopted: %+v", ent)
	}
	conf, _ := os.ReadFile(filepath.Join(w.home, ".ssh", ForwardConf))
	if !strings.Contains(string(conf), "127.0.0.1:47831") || strings.Contains(string(conf), "127.0.0.1:47830") {
		t.Fatalf("block not rewritten:\n%s", conf)
	}
	if _, err := Remove(context.Background(), "gpu-box", RemoveOptions{}, &ManageEnv{SSH: w.env().SSH, StateDir: w.state, Home: w.home}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.home, ".ssh", ForwardConf)); !os.IsNotExist(err) {
		t.Fatalf("remove left the block behind")
	}
}

func TestRemoveForwardDoesNotRestoreOlderBlock(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	f1 := Forward{Host: "gpu-box", Machine: testMID, Name: "gpu-box", Port: 47830, Target: "hub.example:443"}
	if _, err := WriteForward(home, state, f1, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	f2 := f1
	f2.Port = 47831
	r2, err := WriteForward(home, state, f2, "test", time.Now())
	if err != nil || r2.Set == "" {
		t.Fatalf("rewrite %+v %v", r2, err)
	}
	done, err := RemoveForward(home, state, testMID, r2.Set, "test", time.Now())
	if err != nil || !done {
		t.Fatalf("remove %v %v", done, err)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".ssh", ForwardConf)); err == nil && strings.Contains(string(b), "sy-forward") {
		t.Fatalf("undoing the second set brought the older block back:\n%s", b)
	}
}

func TestForwardPortHolderChecked(t *testing.T) {
	w := newWorld(t)
	w.fake.Rules(sshxtest.Rule{Match: "'setup'", ForwardOnly: true, Exit: 255, Stderr: "Error: remote port forwarding failed for listen port 47830\n"})
	e := w.env()
	e.OperatorHub = "http://127.0.0.1:8787"
	e.HubHealth = func(context.Context) (client.Health, error) {
		return client.Health{OK: true, Version: "1.2.3", Schema: 7}, nil
	}
	// Something else listens on the port: no secret may go there.
	w.probe(ProbeReport{V: 1, Machine: testMID, Name: "gpu-box", Free: []int{47830}, Restrict: true,
		Hub: &ProbeHub{URL: "http://127.0.0.1:47830", Reachable: true, Version: "9.9.9", Schema: 1}})
	_, err := Add(context.Background(), Options{Host: testHost, Reach: ReachForward, Binary: w.bin, CodeStdin: true, Code: "123-456", Port: 47830}, e)
	if err == nil || !strings.Contains(err.Error(), "not your hub") {
		t.Fatalf("a foreign listener on the forward port was accepted: %v", err)
	}
	if s := w.read("setup.stdin"); s != "" {
		t.Fatalf("the secret reached the remote: %q", s)
	}

	// Our own hub there (the person's other ssh session holds the forward): reuse it.
	w.probe(ProbeReport{V: 1, Machine: testMID, Name: "gpu-box", Free: []int{47830}, Restrict: true,
		Hub: &ProbeHub{URL: "http://127.0.0.1:47830", Reachable: true, Version: "1.2.3", Schema: 7}})
	if _, err := Add(context.Background(), Options{Host: testHost, Reach: ReachForward, Binary: w.bin, CodeStdin: true, Code: "123-456", Port: 47830}, e); err != nil {
		t.Fatalf("own hub on the port refused: %v", err)
	}
	if w.read("setup.stdin") != "123-456\n" {
		t.Fatalf("setup stdin %q", w.read("setup.stdin"))
	}
}

func TestPullKeyLineRejectsShellMetacharacters(t *testing.T) {
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub"
	for _, sy := range []string{"/home/me/$(touch x)/sy", "/home/me/`id`/sy", "/home/me/a;b/sy", "/home/me/a&b/sy", "/home/me/a|b/sy",
		"/home/me/a b/sy", "/home/me/a'b/sy", "/home/me/a*/sy", "/home/me/~/sy", "/home/me/a<b/sy", "/home/me/a(b)/sy", "/home/me/a\nb/sy", "/home/me/a#b/sy"} {
		if _, _, err := PullKeyLine(pub, sy, "pt_01ABC", "", true); err == nil {
			t.Errorf("sy path %q accepted", sy)
		}
	}
	if _, _, err := PullKeyLine(pub, "/home/me/.local/bin/sy", "pt_01ABC", "", true); err != nil {
		t.Fatalf("plain path refused: %v", err)
	}
}

func TestConcurrentForwardWritesKeepEveryBlock(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	var wg sync.WaitGroup
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := Forward{Host: fmt.Sprintf("host%d", i), Machine: fmt.Sprintf("m_%d", i), Name: fmt.Sprintf("n%d", i), Port: 47830 + i, Target: "hub.example:443"}
			_, err := WriteForward(home, state, f, "test", time.Now())
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(home, ".ssh", ForwardConf))
	for i := 0; i < n; i++ {
		if !strings.Contains(string(b), fmt.Sprintf("machine=m_%d ", i)) {
			t.Fatalf("block %d lost:\n%s", i, b)
		}
	}
	cfg, _ := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if strings.Count(string(cfg), "Include") != 1 {
		t.Fatalf("config %q", cfg)
	}
}

func TestSymlinkedSSHFilesStaySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	home, state, dots := t.TempDir(), t.TempDir(), t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	os.MkdirAll(ssh, 0o700)
	os.WriteFile(filepath.Join(dots, "config"), []byte("Host a\n  User x\n"), 0o600)
	os.WriteFile(filepath.Join(dots, "authorized_keys"), []byte("ssh-ed25519 AAAAB3NzaC1lZDI1NTE5AAAAIother me@x\n"), 0o600)
	for _, n := range []string{"config", "authorized_keys"} {
		if err := os.Symlink(filepath.Join(dots, n), filepath.Join(ssh, n)); err != nil {
			t.Fatal(err)
		}
	}
	f := Forward{Host: "gpu-box", Machine: testMID, Name: "gpu-box", Port: 47830, Target: "hub.example:443"}
	if _, err := WriteForward(home, state, f, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	line, key, err := PullKeyLine("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHub", "/home/me/.local/bin/sy", "pt_01ABC", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizePull(home, line, key, "pt_01ABC"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"config", "authorized_keys"} {
		fi, err := os.Lstat(filepath.Join(ssh, n))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is no longer a symlink: %v", n, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dots, "config")); !strings.HasPrefix(string(b), IncludeLine) {
		t.Fatalf("the target of the config link was not updated: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dots, "authorized_keys")); !strings.Contains(string(b), "sy-pull:pt_01ABC") {
		t.Fatalf("the target of the authorized_keys link was not updated: %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dots, "authorized_keys")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestPickPortIgnoresOtherMachines(t *testing.T) {
	w := newWorld(t)
	reg := mustReg(t, w.state)
	for i := 0; i < 25; i++ {
		reg.Upsert(Entry{Name: fmt.Sprintf("m%d", i), Machine: fmt.Sprintf("m_%d", i), SSH: fmt.Sprintf("host%d", i), Reach: ReachForward, Port: 47830}, w.now)
	}
	reg.Save()
	e := w.env()
	e.OperatorHub = testHubURL
	sum, err := Add(context.Background(), Options{Host: testHost, Reach: ReachForward, Binary: w.bin}, e)
	if err != nil {
		t.Fatalf("the 26th machine failed: %v", err)
	}
	if sum.Port != 47830 {
		t.Fatalf("port %d", sum.Port)
	}
}

func TestHasIncludeVariants(t *testing.T) {
	home := "/home/me"
	for cfg, want := range map[string]bool{
		"Include switchyard.conf\nHost a\n":       true,
		"Include \"switchyard.conf\"\n":           true,
		"include ~/.ssh/switchyard.conf\n":        true,
		"Include /home/me/.ssh/switchyard.conf\n": true,
		"Include ~/.ssh/*.conf\n":                 true,
		"Include config.d/* switchyard.conf\n":    true,
		"Include *\n":                             true,
		"Include other.conf\n":                    false,
		"Include config.d/*\n":                    false,
		"Host a\nInclude switchyard.conf\n":       false,
		"# Include switchyard.conf\n":             false,
		"Include \"~/.ssh/my switchyard.conf\"\n": false,
		"": false,
		"Include /home/other/.ssh/switchyard.conf\n":      false,
		"Include switchyard.conf # added by sy earlier\n": true,
	} {
		if got := hasInclude([]byte(cfg), home); got != want {
			t.Errorf("hasInclude(%q) = %v, want %v", cfg, got, want)
		}
	}
	// A glob include is not added a second time.
	h, st := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(h, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(h, ".ssh", "config"), []byte("Include \"~/.ssh/*.conf\"\nHost a\n"), 0o600)
	f := Forward{Host: "gpu-box", Machine: testMID, Name: "gpu-box", Port: 47830, Target: "hub.example:443"}
	if _, err := WriteForward(h, st, f, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(h, ".ssh", "config")); strings.Count(string(b), "Include") != 1 {
		t.Fatalf("a second Include was added:\n%s", b)
	}
}

func TestLoginCommandQuoting(t *testing.T) {
	for _, tc := range []struct{ folder, want string }{
		{"~/.claude-2", `ssh -t me@box .local/bin/sy account login '~/.claude-2'`},
		{".claude", `ssh -t me@box .local/bin/sy account login .claude`},
		{"my folder; rm -rf x", `ssh -t me@box ''\''.local/bin/sy'\'' '\''account'\'' '\''login'\'' '\''my folder; rm -rf x'\'''`},
	} {
		got := LoginCommand("me@box", tc.folder)
		if got != tc.want {
			t.Errorf("LoginCommand(%q) = %q, want %q", tc.folder, got, tc.want)
		}
	}
}
