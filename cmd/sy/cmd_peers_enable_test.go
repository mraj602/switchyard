package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/keys"
)

// `sy peers enable` is `sy agent enable` plus matrix version 1 from the hub, stored on this
// machine; `sy peers disable` is `sy agent disable`. The service manager is the recording
// runner, the hub a test hub, the folders a temp HOME.
func TestPeersEnableDisable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}
	r := newKeysRig(t)
	old := agentServiceEnv
	t.Cleanup(func() { agentServiceEnv = old })
	agentServiceEnv = fakeServiceEnv(t, &recorder{})

	code, out, errs := runSyArgs("peers", "enable")
	if code != 0 || !strings.Contains(out, "peers are enabled") || !strings.Contains(out, "matrix v1 (hub)") {
		t.Fatalf("enable: %d %q %q", code, out, errs)
	}
	s, err := keys.LoadAcceptedMatrix(r.env.state)
	if err != nil || s == nil || s.Body.Version != 1 || len(s.Body.Allow) != 0 || !s.Body.SameAccount || s.Signer != keys.ByHub {
		t.Fatalf("stored matrix %+v %v", s, err)
	}
	// Again: the same version is not an error.
	if code, out, errs = runSyArgs("peers", "enable"); code != 0 {
		t.Fatalf("second enable: %d %q %q", code, out, errs)
	}
	if code, out, errs = runSyArgs("peers", "disable"); code != 0 || !strings.Contains(out, "peers are disabled") {
		t.Fatalf("disable: %d %q %q", code, out, errs)
	}
	// Status and audit work with no agent and no session: local log only.
	if code, out, _ = runSyArgs("peers", "status"); code != 0 || !strings.Contains(out, "matrix v1") || !strings.Contains(out, "no messages yet") {
		t.Fatalf("status: %d %q", code, out)
	}
	if code, _, _ = runSyArgs("peers", "audit", "--since", "7d"); code != 0 {
		t.Fatalf("audit: %d", code)
	}
	if code, _, _ = runSyArgs("peers", "audit", "--since", "soon"); code != 2 {
		t.Fatalf("audit bad --since: %d", code)
	}
}
