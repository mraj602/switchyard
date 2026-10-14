package push

// Phase 8's end-to-end scenarios X1 to X8 (docs/phases/8.md test plan): the fakeclock sy
// binary with the fake Expo push service (expotest) behind SY_TEST_EXPO_DIAL, driven the way
// the phone app drives the hub (phone.md P2, P3, P6.3, P6.4). exp.host is never contacted.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/expotest"
	"github.com/mraj602/switchyard/internal/record"
)

const (
	expoToken1 = "ExponentPushToken[e2eAAAAAAAAAAAAAAAAAAAA]"
	expoToken2 = "ExponentPushToken[e2eBBBBBBBBBBBBBBBBBBBB]"
)

// expectExpo waits for the n-th message at the fake Expo service and checks it was valid.
func (r *rig) expectExpo(n int) expotest.Message {
	r.t.Helper()
	if !r.ex.Wait(n, 15*time.Second) {
		r.t.Fatalf("expo message %d did not arrive (got %d)", n, len(r.ex.Requests()))
	}
	got := r.ex.Requests()[n-1]
	if got.Err != nil {
		r.t.Fatalf("expo message %d broke push.md 12.2: %v", n, got.Err)
	}
	return got.Msg
}

// ownerSession swaps a one-time owner link for an owner session cookie.
func (r *rig) ownerSession() string {
	r.t.Helper()
	var link struct{ Secret string }
	if err := json.Unmarshal([]byte(r.run("hub", "owner-link", "--json")), &link); err != nil || link.Secret == "" {
		r.t.Fatalf("owner-link: %v %+v", err, link)
	}
	_, hdr, b := r.call("POST", "/v1/session/owner", "", map[string]any{"secret": link.Secret})
	for _, c := range hdrCookies(hdr) {
		if c.Name == "sy_session" {
			return "Cookie sy_session=" + c.Value
		}
	}
	r.t.Fatalf("no owner cookie: %v %s", hdr, b)
	return ""
}

// pairApp pairs a phone the way the app does (phone.md P2): the owner makes a code, the app
// claims it with the QR's secret, expect_kind phone and cookie false, and keeps the token
// from the body.
func (r *rig) pairApp(owner, name string) (bearer string, device string) {
	r.t.Helper()
	var code struct{ Code, Secret, URL string }
	if err := json.Unmarshal(r.must(201, "POST", "/v1/pair/codes", owner, map[string]any{"kind": "phone", "name": name}), &code); err != nil {
		r.t.Fatal(err)
	}
	if code.Secret == "" || !strings.Contains(code.URL, "s="+code.Secret) {
		r.t.Fatalf("pair code %+v has no QR secret", code)
	}
	code2, hdr, b := r.call("POST", "/v1/pair/claim", "", map[string]any{"secret": code.Secret, "name": name + " · app",
		"expect_kind": "phone", "cookie": false, "client": "app/0.8.0+1"})
	if code2 != 200 {
		r.t.Fatalf("claim: %d %s", code2, b)
	}
	if len(hdrCookies(hdr)) != 0 {
		r.t.Errorf("claim with cookie false set a cookie: %v", hdr["Set-Cookie"])
	}
	var claim struct{ Device, Token, Kind string }
	if err := json.Unmarshal(b, &claim); err != nil || claim.Kind != "phone" || claim.Token == "" {
		r.t.Fatalf("claim: %v %s", err, b)
	}
	return "Bearer " + claim.Token, claim.Device
}

// registerExpo is P6.3 step 3.
func (r *rig) registerExpo(phone, token string) (int, []byte) {
	r.t.Helper()
	code, _, b := r.call("POST", "/v1/push/targets", phone, map[string]any{"kind": "expo", "expo_token": token,
		"name": "iPhone · app", "platform": "ios", "app": map[string]string{"version": "0.8.0", "build": "1"}})
	return code, b
}

type selfTarget struct {
	ID, Kind, Endpoint, Status string
	Platform                   *string `json:"platform"`
	AppVersion                 *string `json:"app_version"`
}

func (r *rig) selfTargets(phone string) []selfTarget {
	r.t.Helper()
	var self struct{ Targets []selfTarget }
	if err := json.Unmarshal(r.must(200, "GET", "/v1/devices/self", phone, nil), &self); err != nil {
		r.t.Fatal(err)
	}
	return self.Targets
}

func (r *rig) machineToken() {
	r.t.Helper()
	r.machine = "m_" + record.NewID(time.Now())
	var tok struct{ Token string }
	if err := json.Unmarshal([]byte(r.run("hub", "token", "create", "--kind", "machine", "--name", "laptop", "--machine", r.machine, "--json")), &tok); err != nil {
		r.t.Fatal(err)
	}
	r.mtoken = tok.Token
}

// settleExpo waits a little and returns how many messages the fake received.
func (r *rig) settleExpo() int {
	time.Sleep(500 * time.Millisecond)
	return len(r.ex.Requests())
}

// TestExpoEndToEnd is X1 to X6.
func TestExpoEndToEnd(t *testing.T) {
	r := newRig(t)
	owner := r.ownerSession()

	// ---- X1: pair as the app; the bearer from the body reads /v1/me.
	phoneA, devA := r.pairApp(owner, "iPhone A")
	var me struct {
		Device struct{ ID, Kind string }
	}
	if err := json.Unmarshal(r.must(200, "GET", "/v1/me", phoneA, nil), &me); err != nil || me.Device.ID != devA || me.Device.Kind != "phone" {
		t.Fatalf("X1 /v1/me: %v %+v", err, me)
	}

	// ---- X2: register; another phone device re-binds the same token; a bad token is 400.
	if code, b := r.registerExpo(phoneA, expoToken1); code != 201 {
		t.Fatalf("X2 register: %d %s", code, b)
	}
	ts := r.selfTargets(phoneA)
	if len(ts) != 1 || ts[0].Kind != "expo" || ts[0].Endpoint != expoToken1 || ts[0].Platform == nil || *ts[0].Platform != "ios" ||
		ts[0].AppVersion == nil || *ts[0].AppVersion != "0.8.0" {
		t.Fatalf("X2 targets of A: %+v", ts)
	}
	// Another phone device may not take the token over while A is live (push.md 7: one phone
	// must not redirect another's pushes); once A is unpaired (the app was reinstalled and
	// paired again), the same token re-binds to the new device.
	phone, _ := r.pairApp(owner, "iPhone B")
	if code, b := r.registerExpo(phone, expoToken1); code != 409 {
		t.Fatalf("X2 re-bind from another live device: %d %s", code, b)
	}
	r.must(204, "DELETE", "/v1/devices/self", phoneA, nil)
	if code, b := r.registerExpo(phone, expoToken1); code != 201 {
		t.Fatalf("X2 re-bind: %d %s", code, b)
	}
	if ts := r.selfTargets(phone); len(ts) != 1 || ts[0].Endpoint != expoToken1 {
		t.Fatalf("X2 targets of B: %+v", ts)
	}
	for _, bad := range []string{"ExponentPushToken[short]", "https://exp.host/x", "ExpoPushToken[has space here]"} {
		if code, b := r.registerExpo(phone, bad); code != 400 {
			t.Errorf("X2 bad token %q: %d %s", bad, code, b)
		}
	}
	r.machineToken()

	// ---- X3: a question → nothing at 59 s, one message at 60 s.
	const s1 = "c41e0000-0000-4000-8000-0000000000e1"
	r.ask(s1)
	r.advance(59 * time.Second)
	if n := r.settleExpo(); n != 0 {
		t.Fatalf("X3 pushed before 60 s: %d", n)
	}
	r.advance(time.Second)
	m := r.expectExpo(1)
	if m.To != expoToken1 {
		t.Errorf("X3 to %q", m.To)
	}
	for k, want := range map[string]any{"v": 1.0, "type": "alert", "trigger": "asked", "session": s1, "tag": "sy-s-" + s1,
		"body": "Should I also update the migration?", "private": false, "tap": "auto"} {
		if m.Data[k] != want {
			t.Errorf("X3 data.%s = %v, want %v", k, m.Data[k], want)
		}
	}
	alertID, _ := m.Data["alert"].(string)
	if !strings.HasPrefix(alertID, "al_") || !strings.HasPrefix(str(m.Data["url"]), "/#/sessions/"+s1+"?alert="+alertID) {
		t.Errorf("X3 alert %q url %v", alertID, m.Data["url"])
	}
	acts, _ := m.Data["actions"].(map[string]any)
	snooze, _ := acts["snooze"].(string)
	if snooze == "" || acts["seen"] == nil {
		t.Fatalf("X3 actions %v", m.Data["actions"])
	}
	if m.Title == nil || !strings.Contains(*m.Title, "asked a question") || str(m.Data["title"]) != *m.Title {
		t.Errorf("X3 title %v / data.title %v", m.Title, m.Data["title"])
	}
	if str(m.CategoryID) != "sy_alert" || str(m.ChannelID) != "sy_needs_you" || m.Priority != "high" || m.TTL == nil || *m.TTL != 21600 {
		t.Errorf("X3 message fields: category %v channel %v priority %q ttl %v", str(m.CategoryID), str(m.ChannelID), m.Priority, m.TTL)
	}
	if nyou, _ := m.Data["needs_you"].(float64); m.Badge == nil || float64(*m.Badge) != nyou || nyou < 1 {
		t.Errorf("X3 badge %v, needs_you %v", m.Badge, m.Data["needs_you"])
	}
	if m.ContentAvailable != nil {
		t.Errorf("X3 an alert is not a background push")
	}

	// ---- X4: Snooze from the push: no credential; after snooze_min a "Still waiting" re-push.
	var act struct {
		Applied bool
		Alert   struct{ Status string }
	}
	if err := json.Unmarshal(r.must(200, "POST", "/v1/actions/"+snooze, "", map[string]any{}), &act); err != nil || !act.Applied {
		t.Fatalf("X4 snooze: %v %+v", err, act)
	}
	r.advance(14 * time.Minute)
	if n := r.settleExpo(); n != 1 {
		t.Fatalf("X4 re-pushed inside the snooze: %d messages", n)
	}
	for i := 0; i < 3 && len(r.ex.Requests()) < 2; i++ {
		r.advance(time.Minute)
		time.Sleep(200 * time.Millisecond)
	}
	m = r.expectExpo(2)
	if m.Title == nil || !strings.HasPrefix(*m.Title, "Still waiting · ") || m.Data["alert"] != alertID {
		t.Errorf("X4 re-push title %v alert %v", m.Title, m.Data["alert"])
	}

	// ---- X5: private pushes: title "Switchyard", no remote_url, the alert route.
	r.must(200, "PATCH", "/v1/devices/self", phone, map[string]any{"prefs": map[string]any{"private": true}})
	const s2 = "c41e0000-0000-4000-8000-0000000000e2"
	r.ask(s2)
	r.advance(60 * time.Second)
	m = r.expectExpo(3)
	if m.Title == nil || *m.Title != "Switchyard" || m.Data["private"] != true {
		t.Errorf("X5 private title %v private %v", m.Title, m.Data["private"])
	}
	if _, ok := m.Data["remote_url"]; ok {
		t.Errorf("X5 private data has remote_url")
	}
	if id := str(m.Data["alert"]); id == "" || str(m.Data["url"]) != "/#/alerts/"+id {
		t.Errorf("X5 private url %v for alert %v", m.Data["url"], m.Data["alert"])
	}
	if strings.Contains(string(r.ex.Requests()[2].Raw), "update the migration") {
		t.Errorf("X5 private message carries the question text")
	}

	// ---- X6: receipts: DeviceNotRegistered 15 min later → the target is gone, no more sends.
	r.ex.SetAllReceipts(expotest.ReceiptDeviceNotRegistered)
	sent := len(r.ex.Requests())
	r.advance(16 * time.Minute)
	if len(r.ex.ReceiptRequests()) == 0 {
		t.Fatal("X6 no getReceipts request after 16 min")
	}
	if ts := r.selfTargets(phone); len(ts) != 1 || ts[0].Status != "gone" {
		t.Fatalf("X6 target after DeviceNotRegistered: %+v", ts)
	}
	sent = max(sent, len(r.ex.Requests())) // a re-push may have raced the receipt; none after it
	const s3 = "c41e0000-0000-4000-8000-0000000000e3"
	r.ask(s3)
	r.advance(2 * time.Minute)
	if n := r.settleExpo(); n != sent {
		t.Errorf("X6 sent %d message(s) to a gone target", n-sent)
	}
}

// TestExpoWidgetRefresh is X7: silent refresh pushes for widgets, at most one per
// expo_refresh_min (15) per target.
func TestExpoWidgetRefresh(t *testing.T) {
	r := newRig(t)
	owner := r.ownerSession()
	phone, _ := r.pairApp(owner, "iPhone")
	if code, b := r.registerExpo(phone, expoToken2); code != 201 {
		t.Fatalf("register: %d %s", code, b)
	}
	r.machineToken()
	resets := r.now().Add(4 * time.Hour)
	r.limit(30, resets)
	r.advance(15 * time.Second) // past the debounce: widgets are off, so nothing is sent
	if n := r.settleExpo(); n != 0 {
		t.Fatalf("refresh sent with prefs.widgets off: %d", n)
	}
	r.must(200, "PATCH", "/v1/devices/self", phone, map[string]any{"prefs": map[string]any{"widgets": true}})

	// The first change after widgets were turned on refreshes (the hub has no digest yet).
	r.limit(40, resets)
	r.advance(15 * time.Second)
	first := r.expectExpo(1)
	if first.Data["type"] != "refresh" {
		t.Fatalf("first message %v", first.Data)
	}
	r.advance(16 * time.Minute)
	if n := r.settleExpo(); n != 1 {
		t.Fatalf("refresh without a change: %d messages", n)
	}

	// A ring moves by 10 points → one refresh.
	r.limit(50, resets)
	r.advance(15 * time.Second)
	m := r.expectExpo(2)
	if m.Data["type"] != "refresh" || m.Data["v"] != 1.0 || m.ContentAvailable == nil || !*m.ContentAvailable {
		t.Errorf("X7 refresh data %v contentAvailable %v", m.Data, m.ContentAvailable)
	}
	if m.Title != nil || m.Body != nil || m.Sound != nil || m.CategoryID != nil || m.Priority != "normal" {
		t.Errorf("X7 a refresh has no title, body, sound or category: %+v", m)
	}
	if _, ok := m.Data["needs_you"]; !ok {
		t.Errorf("X7 refresh has no needs_you")
	}

	// A second change within 15 minutes → none.
	r.limit(60, resets)
	r.advance(15 * time.Second)
	r.advance(5 * time.Minute)
	if n := r.settleExpo(); n != 2 {
		t.Errorf("X7 a change within 15 min sent %d more", n-2)
	}
}

// TestExpoWebPushUnchanged is X8: the phase 4 scenario, byte for byte, with an app (expo)
// target on the same phone, which gets each push as well.
func TestExpoWebPushUnchanged(t *testing.T) {
	r := newRig(t)
	phase4(t, r, true)
	if n := r.settleExpo(); n != 2 {
		t.Errorf("X8 the app target got %d messages, want 2 (question, limit_80)", n)
	}
}

func hdrCookies(h http.Header) []*http.Cookie { return (&http.Response{Header: h}).Cookies() }

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case *string:
		if x != nil {
			return *x
		}
	}
	return ""
}
