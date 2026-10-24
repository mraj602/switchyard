package api_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/ntfytest"
)

// A phone token must not make the hub call internal services.
func TestPhoneCannotRegisterInternalNtfyServer(t *testing.T) {
	r := newPushRig(t, true)
	for _, srv := range []string{r.nt.URL(), "http://127.0.0.1:9", "https://127.0.0.1:9", "https://10.0.0.5", "https://169.254.169.254",
		"https://localhost", "https://oci-hub", "https://ntfy.tail1234.ts.net", "https://100.101.102.103", "https://[::1]"} {
		st, _, b := r.do("POST", "/v1/push/targets", r.phone, map[string]any{"kind": "ntfy", "server": srv + "/admin/api", "topic": "x", "token": "attacker"})
		if st != 400 {
			t.Errorf("phone registered %q: %d %s", srv, st, b)
		}
	}
	if n := len(r.json("GET", "/v1/push/targets", r.owner, nil, 200)["targets"].([]any)); n != 0 {
		t.Fatalf("%d targets stored", n)
	}
}

// Even a target already in the store (registered before the fix) never reaches a private
// address from a device, and its test result carries no response body.
func TestDeviceNtfyTargetIsDialGuardedAndTestHasNoBody(t *testing.T) {
	r := newPushRig(t, true)
	r.nt.Script(ntfytest.Reply{Status: 403, Body: `{"internal_secret":"s3cr3t-admin-data"}`})
	tg, err := r.st.UpsertTarget(context.Background(), model.Target{Kind: model.KindNtfy, DeviceID: r.deviceID(r.phone), Name: "x",
		Endpoint: r.nt.URL() + "/sy-topic", Secret: "attacker"})
	if err != nil {
		t.Fatal(err)
	}
	st, _, b := r.do("POST", "/v1/push/targets/"+tg.ID+"/test", r.phone, nil)
	if st != 200 || bytes.Contains(b, []byte("s3cr3t")) || !bytes.Contains(b, []byte("non-public")) {
		t.Fatalf("%d %s", st, b)
	}
	if n := len(r.nt.Requests()); n != 0 {
		t.Fatalf("the internal server received %d requests", n)
	}
}

// The owner may use their own loopback/tailnet server, and still gets only status + code.
func TestOwnerNtfyTestReturnsOutcomeAndStatusOnly(t *testing.T) {
	r := newPushRig(t, true)
	tg := r.json("POST", "/v1/push/targets", r.owner, map[string]any{"kind": "ntfy", "server": r.nt.URL(), "topic": "hub-topic"}, 201)
	r.nt.Script(ntfytest.Reply{Status: 403, Body: `{"internal_secret":"s3cr3t-admin-data"}`})
	st, _, b := r.do("POST", "/v1/push/targets/"+tg["id"].(string)+"/test", r.owner, nil)
	if st != 200 || bytes.Contains(b, []byte("s3cr3t")) || !bytes.Contains(b, []byte(`"http_status":403`)) || !bytes.Contains(b, []byte(`"error":"HTTP 403"`)) {
		t.Fatalf("%d %s", st, b)
	}
	got := r.json("GET", "/v1/push/targets", r.owner, nil, 200)["targets"].([]any)[0].(map[string]any)
	if got["last_error"] != "HTTP 403" {
		t.Fatalf("last_error %v", got["last_error"])
	}
}

func TestPerDeviceTargetCap(t *testing.T) {
	r := newPushRig(t, false)
	add := func(i int, token string) int {
		st, _, _ := r.do("POST", "/v1/push/targets", token, map[string]any{"kind": "ntfy", "server": "https://ntfy.example.com", "topic": fmt.Sprintf("t%d", i)})
		return st
	}
	for i := 0; i < 10; i++ {
		if st := add(i, r.phone); st != 201 {
			t.Fatalf("target %d: %d", i, st)
		}
	}
	if st := add(10, r.phone); st != 409 {
		t.Fatalf("11th target: %d", st)
	}
	if st := add(3, r.phone); st != 201 { // re-adding an own target is not a new one
		t.Fatalf("re-add: %d", st)
	}
	if st := add(0, r.other); st != 409 { // t0 belongs to a live phone-1
		t.Fatalf("other device taking t0: %d", st)
	}
	if st := add(100, r.other); st != 201 {
		t.Fatalf("other device is not capped by phone-1: %d", st)
	}
	if st := add(11, r.owner); st != 201 {
		t.Fatalf("owner: %d", st)
	}
}
