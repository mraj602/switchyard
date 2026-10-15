package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/ntfy"
	"github.com/mraj602/switchyard/internal/hub/push/ntfytest"
	"github.com/mraj602/switchyard/internal/hub/push/webpush"
	"github.com/mraj602/switchyard/internal/hub/push/webpushtest"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// A hub with both real senders pointed at the fakes, and a clock the test moves.
type pushRig struct {
	t     *testing.T
	st    *store.Store
	http  *httptest.Server
	wp    *webpushtest.Server
	nt    *ntfytest.Server
	owner string
	phone string // push-scoped device 1
	other string // push-scoped device 2
	clock *pushClock
}

type pushClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *pushClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *pushClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newPushRig(t *testing.T, withSenders bool) *pushRig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := &pushRig{t: t, st: st, wp: webpushtest.New(), nt: ntfytest.New(), clock: &pushClock{t: time.Now()}}
	cfg := api.Config{Store: st, Now: r.clock.now, PingEvery: time.Hour, PublicURL: "https://hub.example.ts.net"}
	if withSenders {
		keys, err := webpush.LoadVAPID(ctx, st)
		if err != nil {
			t.Fatal(err)
		}
		r.wp.ExpectVapidKey(keys.PublicKey())
		cfg.Senders = map[string]model.Sender{
			model.KindWebPush: webpush.New(webpush.Options{Keys: keys, Client: r.wp.Client(), AllowAnyEndpoint: true,
				Subject: func() string { return "https://hub.example.ts.net" }}),
			model.KindNtfy: ntfy.New(ntfy.Options{}),
		}
	}
	srv := api.New(cfg)
	r.http = httptest.NewServer(srv.Handler())
	t.Cleanup(func() { srv.Close(); r.http.Close(); r.wp.Close(); r.nt.Close(); st.Close() })
	mk := func(kind, name string, scopes []string) string {
		c, err := auth.Create(ctx, st, auth.CreateRequest{Kind: kind, Name: name, Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
		return c.Token
	}
	r.owner = ownerSessionToken(t, st).Token
	r.phone = mk("phone", "phone-1", nil)
	r.other = mk("phone", "phone-2", nil)
	return r
}

func (r *pushRig) do(method, path, token string, body any) (int, http.Header, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, r.http.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func (r *pushRig) json(method, path, token string, body any, wantStatus int) map[string]any {
	r.t.Helper()
	st, _, b := r.do(method, path, token, body)
	if st != wantStatus {
		r.t.Fatalf("%s %s = %d, want %d: %s", method, path, st, wantStatus, b)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (r *pushRig) deviceID(token string) string {
	if auth.IsOwnerToken(token) { // keyed hash: find it by its hint
		all, _ := r.st.Devices(context.Background(), false)
		for _, d := range all {
			if d.TokenHint == auth.Hint(token) {
				return d.ID
			}
		}
	}
	d, err := r.st.DeviceByHash(context.Background(), auth.Hash(token))
	if err != nil {
		r.t.Fatal(err)
	}
	return d.ID
}

func browserKeys(t *testing.T) map[string]string {
	t.Helper()
	w := webpushtest.New()
	defer w.Close()
	s := w.NewSubscription()
	return map[string]string{"p256dh": s.P256dh, "auth": s.Auth}
}

func TestPushEndpointsRequireTheRightScope(t *testing.T) {
	r := newPushRig(t, false)
	machine, err := auth.Create(context.Background(), r.st, auth.CreateRequest{Kind: "machine", Name: "m", Machine: "m_1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/v1/push/config", "", 401},
		{"GET", "/v1/push/config", machine.Token, 403},
		{"GET", "/v1/push/targets", "", 401},
		{"GET", "/v1/push/targets", machine.Token, 403},
		{"PUT", "/v1/push/channels/ntfy", r.phone, 403},
		{"PUT", "/v1/push/channels/ntfy", "", 401},
		{"POST", "/v1/push/targets/pt_x/test", machine.Token, 403},
	} {
		if st, _, _ := r.do(c.method, c.path, c.token, map[string]any{}); st != c.want {
			t.Errorf("%s %s as %q: %d, want %d", c.method, c.path, c.token[:min(len(c.token), 6)], st, c.want)
		}
	}
}

func TestPushConfig(t *testing.T) {
	r := newPushRig(t, false)
	cfg := r.json("GET", "/v1/push/config", r.phone, nil, 200)
	key, _ := cfg["vapid_public_key"].(string)
	if len(key) != 87 || cfg["ntfy_default_server"] != "https://ntfy.sh" {
		t.Fatalf("%v", cfg)
	}
	chans := cfg["channels"].([]any)
	if len(chans) != 2 { // webpush, ntfy (link is not listed here)
		t.Fatalf("channels %v", chans)
	}
	for _, c := range chans {
		cm := c.(map[string]any)
		if cm["enabled"] != true || cm["settings"] == nil {
			t.Fatalf("%v", cm)
		}
	}
	// Stable across calls, and the same key the senders sign with.
	if again := r.json("GET", "/v1/push/config", r.owner, nil, 200)["vapid_public_key"]; again != key {
		t.Fatal("key changed between calls")
	}
	v, _ := webpush.LoadVAPID(context.Background(), r.st)
	if v.PublicKey() != key {
		t.Fatal("config key is not the stored VAPID key")
	}
	// The private key is not in any response.
	_, _, body := r.do("GET", "/v1/push/config", r.owner, nil)
	sec, _ := r.st.HubSecret(context.Background(), webpush.SecretVapidPrivate)
	if bytes.Contains(body, sec) {
		t.Fatal("raw secret in the response")
	}
}

func TestPutChannel(t *testing.T) {
	r := newPushRig(t, false)
	got := r.json("PUT", "/v1/push/channels/ntfy", r.owner, map[string]any{"kind": "ntfy", "enabled": false, "settings": map[string]any{"x": 1}}, 200)
	if got["kind"] != "ntfy" || got["enabled"] != false || got["settings"].(map[string]any)["x"] != float64(1) {
		t.Fatalf("%v", got)
	}
	// Enabled alone keeps the settings.
	got = r.json("PUT", "/v1/push/channels/ntfy", r.owner, map[string]any{"enabled": true}, 200)
	if got["enabled"] != true || got["settings"].(map[string]any)["x"] != float64(1) {
		t.Fatalf("%v", got)
	}
	cfg := r.json("GET", "/v1/push/config", r.owner, nil, 200)
	for _, c := range cfg["channels"].([]any) {
		if c.(map[string]any)["kind"] == "webpush" && c.(map[string]any)["enabled"] != true {
			t.Fatal("webpush changed")
		}
	}
	for name, c := range map[string]struct {
		path string
		body any
	}{
		"unknown kind": {"/v1/push/channels/apns", map[string]any{"enabled": true}},
		"no enabled":   {"/v1/push/channels/ntfy", map[string]any{}},
		"kind clash":   {"/v1/push/channels/ntfy", map[string]any{"kind": "webpush", "enabled": true}},
		"settings arr": {"/v1/push/channels/ntfy", map[string]any{"enabled": true, "settings": []int{1}}},
	} {
		if st, _, _ := r.do("PUT", c.path, r.owner, c.body); st != 400 {
			t.Errorf("%s: %d", name, st)
		}
	}
}

func TestAddWebPushTarget(t *testing.T) {
	r := newPushRig(t, false)
	keys := browserKeys(t)
	body := map[string]any{"kind": "webpush", "endpoint": "https://web.push.apple.com/QGzAbc", "keys": keys, "name": "iPhone · Safari"}
	got := r.json("POST", "/v1/push/targets", r.phone, body, 201)
	if got["kind"] != "webpush" || got["endpoint_host"] != "web.push.apple.com" || got["device"] != r.deviceID(r.phone) ||
		got["name"] != "iPhone · Safari" || got["status"] != "new" || got["enabled"] != true || got["token_set"] != false ||
		got["endpoint"] != "https://web.push.apple.com/QGzAbc" || !strings.HasPrefix(got["id"].(string), "pt_") {
		t.Fatalf("%v", got)
	}
	// Upsert by endpoint: same id, no second row.
	again := r.json("POST", "/v1/push/targets", r.phone, body, 201)
	if again["id"] != got["id"] {
		t.Fatal("re-subscribing made a new target")
	}
	if n := len(r.json("GET", "/v1/push/targets", r.phone, nil, 200)["targets"].([]any)); n != 1 {
		t.Fatalf("%d targets", n)
	}
	// Default name when none given.
	nameless := r.json("POST", "/v1/push/targets", r.phone, map[string]any{"kind": "webpush", "endpoint": "https://fcm.googleapis.com/fcm/send/xyz", "keys": keys}, 201)
	if nameless["name"] != "Browser" {
		t.Fatalf("%v", nameless)
	}
}

func TestAddWebPushTargetValidation(t *testing.T) {
	r := newPushRig(t, false)
	keys := browserKeys(t)
	good := "https://fcm.googleapis.com/fcm/send/abc"
	for name, body := range map[string]map[string]any{
		"no endpoint":  {"kind": "webpush", "keys": keys},
		"no keys":      {"kind": "webpush", "endpoint": good},
		"http":         {"kind": "webpush", "endpoint": "http://fcm.googleapis.com/x", "keys": keys},
		"unknown host": {"kind": "webpush", "endpoint": "https://evil.example/x", "keys": keys},
		"loopback":     {"kind": "webpush", "endpoint": "https://127.0.0.1/x", "keys": keys},
		"port":         {"kind": "webpush", "endpoint": "https://fcm.googleapis.com:8443/x", "keys": keys},
		"short p256dh": {"kind": "webpush", "endpoint": good, "keys": map[string]string{"p256dh": "AAAA", "auth": keys["auth"]}},
		"short auth":   {"kind": "webpush", "endpoint": good, "keys": map[string]string{"p256dh": keys["p256dh"], "auth": "AAAA"}},
		"no kind":      {"endpoint": good, "keys": keys},
		"bad kind":     {"kind": "carrier", "endpoint": good, "keys": keys},
		"long name":    {"kind": "webpush", "endpoint": good, "keys": keys, "name": strings.Repeat("n", 61)},
	} {
		st, _, b := r.do("POST", "/v1/push/targets", r.phone, body)
		if st != 400 {
			t.Errorf("%s: %d %s", name, st, b)
		}
	}
	if st, _, _ := r.do("POST", "/v1/push/targets", r.phone, "not an object"); st != 400 {
		t.Errorf("string body: %d", st)
	}
	// The owner can widen the allow-list (push.settings.webpush_hosts).
	if err := r.st.PutPushSetting(context.Background(), "push.settings", json.RawMessage(`{"webpush_hosts":["push.example.org"]}`)); err != nil {
		t.Fatal(err)
	}
	r.json("POST", "/v1/push/targets", r.phone, map[string]any{"kind": "webpush", "endpoint": "https://push.example.org/x", "keys": keys}, 201)
	if st, _, _ := r.do("POST", "/v1/push/targets", r.phone, map[string]any{"kind": "webpush", "endpoint": good, "keys": keys}); st != 400 {
		t.Errorf("the default list should no longer apply: %d", st)
	}
}

func TestEndpointOfAnotherLiveDeviceIsNotTakenOver(t *testing.T) {
	r := newPushRig(t, false)
	body := map[string]any{"kind": "webpush", "endpoint": "https://web.push.apple.com/abc", "keys": browserKeys(t)}
	first := r.json("POST", "/v1/push/targets", r.phone, body, 201)
	if st, _, _ := r.do("POST", "/v1/push/targets", r.other, body); st != 409 {
		t.Fatalf("takeover by another phone: %d", st)
	}
	// The owner may re-bind it (and then it is the owner's device's)...
	r.json("POST", "/v1/push/targets", r.owner, body, 201)
	if got := r.json("GET", "/v1/push/targets", r.owner, nil, 200)["targets"].([]any)[0].(map[string]any); got["id"] != first["id"] || got["device"] != r.deviceID(r.owner) {
		t.Fatalf("%v", got)
	}
	if st, _, _ := r.do("POST", "/v1/push/targets", r.phone, body); st != 409 {
		t.Fatalf("phone taking it back from the owner's device: %d", st)
	}
	// ...and anyone may once the holder is revoked.
	if err := auth.Revoke(context.Background(), r.st, r.deviceID(r.owner)); err != nil {
		t.Fatal(err)
	}
	r.json("POST", "/v1/push/targets", r.other, body, 201)
}

func TestAddNtfyTarget(t *testing.T) {
	r := newPushRig(t, false)
	// A phone's topic on ntfy.sh: generated, private by default, belongs to the phone; the token is never echoed.
	got := r.json("POST", "/v1/push/targets", r.phone, map[string]any{"kind": "ntfy", "token": "tk_supersecret"}, 201)
	topic, _ := got["topic"].(string)
	if !strings.HasPrefix(topic, "sy-") || len(topic) != 27 || got["server"] != "https://ntfy.sh" || got["token_set"] != true ||
		got["device"] != r.deviceID(r.phone) || got["endpoint_host"] != "ntfy.sh" || got["endpoint"] != "https://ntfy.sh/"+topic {
		t.Fatalf("%v", got)
	}
	if p := got["prefs"].(map[string]any); p["private"] != true {
		t.Fatalf("ntfy.sh target not private by default: %v", got["prefs"])
	}
	if !strings.HasPrefix(got["name"].(string), "ntfy.sh/sy-") {
		t.Fatalf("name %v", got["name"])
	}
	for _, path := range []string{"/v1/push/targets"} {
		for _, tok := range []string{r.phone, r.owner} {
			_, _, b := r.do("GET", path, tok, nil)
			if bytes.Contains(b, []byte("tk_supersecret")) {
				t.Fatalf("token leaked in %s", path)
			}
		}
	}
	// Owner: no device; self-hosted server over http on loopback; private not forced.
	own := r.json("POST", "/v1/push/targets", r.owner, map[string]any{"kind": "ntfy", "server": r.nt.URL(), "topic": "my-topic", "name": "Hub topic"}, 201)
	if own["device"] != nil || own["topic"] != "my-topic" || own["server"] != r.nt.URL() || own["token_set"] != false ||
		own["prefs"].(map[string]any)["private"] != false {
		t.Fatalf("%v", own)
	}
	// Explicit private wins, and re-adding without a token keeps the stored one.
	r.json("POST", "/v1/push/targets", r.owner, map[string]any{"kind": "ntfy", "server": "https://ntfy.example.com", "topic": "t1", "token": "tk_keep"}, 201)
	again := r.json("POST", "/v1/push/targets", r.owner, map[string]any{"kind": "ntfy", "server": "https://ntfy.example.com", "topic": "t1", "private": true}, 201)
	if again["token_set"] != true || again["prefs"].(map[string]any)["private"] != true {
		t.Fatalf("%v", again)
	}
	for name, body := range map[string]map[string]any{
		"http non-local": {"kind": "ntfy", "server": "http://ntfy.example.com"},
		"bad topic":      {"kind": "ntfy", "topic": "has space"},
		"long topic":     {"kind": "ntfy", "topic": strings.Repeat("a", 65)},
		"bad server":     {"kind": "ntfy", "server": "gopher://x"},
	} {
		if st, _, _ := r.do("POST", "/v1/push/targets", r.phone, body); st != 400 {
			t.Errorf("%s: %d", name, st)
		}
	}
}

func TestTargetVisibilityPatchAndDelete(t *testing.T) {
	r := newPushRig(t, false)
	mine := r.json("POST", "/v1/push/targets", r.phone, map[string]any{"kind": "ntfy", "topic": "phone-topic", "server": "https://ntfy.example.com"}, 201)
	hubs := r.json("POST", "/v1/push/targets", r.owner, map[string]any{"kind": "ntfy", "topic": "hub-topic", "server": r.nt.URL()}, 201)
	id, hubID := mine["id"].(string), hubs["id"].(string)

	list := func(tok string) int {
		return len(r.json("GET", "/v1/push/targets", tok, nil, 200)["targets"].([]any))
	}
	if list(r.phone) != 1 || list(r.other) != 0 || list(r.owner) != 2 {
		t.Fatalf("visibility: phone %d other %d owner %d", list(r.phone), list(r.other), list(r.owner))
	}
	// Someone else's target is "not found" for every action.
	for _, c := range []struct{ method, path string }{{"PATCH", "/v1/push/targets/" + id}, {"DELETE", "/v1/push/targets/" + id}, {"POST", "/v1/push/targets/" + id + "/test"},
		{"PATCH", "/v1/push/targets/" + hubID}, {"DELETE", "/v1/push/targets/" + hubID}} {
		tok := r.other
		if strings.HasSuffix(c.path, hubID) {
			tok = r.phone
		}
		if st, _, _ := r.do(c.method, c.path, tok, map[string]any{"enabled": false}); st != 404 {
			t.Errorf("%s %s: %d", c.method, c.path, st)
		}
	}

	got := r.json("PATCH", "/v1/push/targets/"+id, r.phone, map[string]any{"enabled": false, "name": "  Renamed "}, 200)
	if got["enabled"] != false || got["status"] != "disabled" || got["name"] != "Renamed" {
		t.Fatalf("%v", got)
	}
	// Device-owned targets follow the device's prefs; hub targets take their own.
	if st, _, _ := r.do("PATCH", "/v1/push/targets/"+id, r.phone, map[string]any{"prefs": map[string]any{"tap": "claude"}}); st != 400 {
		t.Errorf("prefs on a device target: %d", st)
	}
	prefs := map[string]any{"mute": []string{"limit_80"}, "tap": "session", "private": true, "tz": "Asia/Kolkata",
		"quiet_hours": map[string]any{"enabled": true, "start": "22:00", "end": "07:00", "tz": "Asia/Kolkata"}}
	got = r.json("PATCH", "/v1/push/targets/"+hubID, r.owner, map[string]any{"prefs": prefs}, 200)
	p := got["prefs"].(map[string]any)
	if p["tap"] != "session" || p["private"] != true || p["mute"].([]any)[0] != "limit_80" || p["quiet_hours"].(map[string]any)["start"] != "22:00" {
		t.Fatalf("%v", p)
	}
	for name, bad := range map[string]map[string]any{
		"tap":     {"tap": "nope"},
		"mute":    {"mute": []string{"nonsense"}},
		"tz":      {"tz": "Mars/Base"},
		"quiet":   {"quiet_hours": map[string]any{"enabled": true, "start": "25:00", "end": "07:00"}},
		"long nm": nil,
	} {
		body := map[string]any{"prefs": bad}
		if bad == nil {
			body = map[string]any{"name": strings.Repeat("x", 61)}
		}
		if st, _, _ := r.do("PATCH", "/v1/push/targets/"+hubID, r.owner, body); st != 400 {
			t.Errorf("%s: %d", name, st)
		}
	}
	if st, _, _ := r.do("PATCH", "/v1/push/targets/pt_missing", r.owner, map[string]any{"enabled": true}); st != 404 {
		t.Errorf("missing: %d", st)
	}

	if st, _, _ := r.do("DELETE", "/v1/push/targets/"+id, r.phone, nil); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	if st, _, _ := r.do("DELETE", "/v1/push/targets/"+id, r.phone, nil); st != 404 {
		t.Fatalf("second delete: %d", st)
	}
	if st, _, _ := r.do("DELETE", "/v1/push/targets/"+hubID, r.owner, nil); st != 204 {
		t.Fatalf("owner delete: %d", st)
	}
}

// addFakeWebPush registers a subscription of the fake service (its loopback endpoint cannot
// pass the production allow-list, so it goes in through the store).
func (r *pushRig) addFakeWebPush(deviceToken string) (id string, sub *webpushtest.Subscription) {
	r.t.Helper()
	sub = r.wp.NewSubscription()
	tg, err := r.st.UpsertTarget(context.Background(), model.Target{Kind: model.KindWebPush, DeviceID: r.deviceID(deviceToken),
		Name: "fake", Endpoint: sub.Endpoint, Keys: &model.Keys{P256dh: sub.P256dh, Auth: sub.Auth}})
	if err != nil {
		r.t.Fatal(err)
	}
	return tg.ID, sub
}

func TestTestPushWebPushRateLimitAndResults(t *testing.T) {
	r := newPushRig(t, true)
	id, _ := r.addFakeWebPush(r.phone)
	path := "/v1/push/targets/" + id + "/test"

	got := r.json("POST", path, r.phone, nil, 200)
	if got["outcome"] != "sent" || got["http_status"] != float64(201) || got["error"] != nil {
		t.Fatalf("%v", got)
	}
	rec := r.wp.Requests()
	if len(rec) != 1 || rec[0].VapidErr != nil || rec[0].DecodeErr != nil {
		t.Fatalf("%+v", rec)
	}
	var p model.Payload
	if err := json.Unmarshal(rec[0].Plain, &p); err != nil || p.Type != "test" || p.Tag != "sy-test" || p.Title == "" ||
		p.Actions != nil || rec[0].Header.Get("Ttl") != "60" || rec[0].Header.Get("Urgency") != "high" {
		t.Fatalf("%s %v %v", rec[0].Plain, err, rec[0].Header)
	}
	// The result is on the target.
	tg := r.json("GET", "/v1/push/targets", r.phone, nil, 200)["targets"].([]any)[0].(map[string]any)
	if tg["status"] != "ok" || tg["last_ok_at"] == nil {
		t.Fatalf("%v", tg)
	}

	// 1 per 5 s per target.
	st, hdr, _ := r.do("POST", path, r.phone, nil)
	if st != 429 || hdr.Get("Retry-After") == "" {
		t.Fatalf("second test within 5 s: %d %v", st, hdr)
	}
	if n := len(r.wp.Requests()); n != 1 {
		t.Fatalf("a limited request still sent (%d)", n)
	}
	r.clock.advance(4 * time.Second)
	if st, _, _ := r.do("POST", path, r.phone, nil); st != 429 {
		t.Fatalf("after 4 s: %d", st)
	}
	r.clock.advance(1100 * time.Millisecond)
	r.wp.Script(webpushtest.Reply{Status: 410, Body: "gone"})
	got = r.json("POST", path, r.phone, nil, 200)
	if got["outcome"] != "gone" || got["http_status"] != float64(410) || !strings.HasPrefix(got["error"].(string), "410") {
		t.Fatalf("%v", got)
	}
	tg = r.json("GET", "/v1/push/targets", r.phone, nil, 200)["targets"].([]any)[0].(map[string]any)
	if tg["status"] != "gone" || tg["enabled"] != false {
		t.Fatalf("gone not recorded: %v", tg)
	}

	// Another target is not limited by this one.
	id2, _ := r.addFakeWebPush(r.phone)
	r.json("POST", "/v1/push/targets/"+id2+"/test", r.phone, nil, 200)

	// A refusal is a result, not an HTTP error.
	r.clock.advance(6 * time.Second)
	r.wp.Script(webpushtest.Reply{Status: 403, Body: `{"reason":"BadJwtToken"}`})
	got = r.json("POST", "/v1/push/targets/"+id2+"/test", r.phone, nil, 200)
	if got["outcome"] != "failed" || got["http_status"] != float64(403) || !strings.Contains(got["error"].(string), "BadJwtToken") {
		t.Fatalf("%v", got)
	}
}

func TestTestPushNtfy(t *testing.T) {
	r := newPushRig(t, true)
	r.nt.RequireToken("tk_abc")
	tg := r.json("POST", "/v1/push/targets", r.owner, map[string]any{"kind": "ntfy", "server": r.nt.URL(), "topic": "hub-topic", "token": "tk_abc"}, 201)
	path := "/v1/push/targets/" + tg["id"].(string) + "/test"
	got := r.json("POST", path, r.owner, nil, 200)
	if got["outcome"] != "sent" {
		t.Fatalf("%v", got)
	}
	m := r.nt.Requests()[0]
	if m.Err != nil || m.Msg.Topic != "hub-topic" || m.Header.Get("Authorization") != "Bearer tk_abc" || m.Msg.Click != "https://hub.example.ts.net/#/" ||
		len(m.Msg.Actions) != 1 || m.Msg.Actions[0].Label != "Open" {
		t.Fatalf("%+v", m)
	}
	// A wrong token surfaces as a failed result; the token is not in the error text.
	r.clock.advance(6 * time.Second)
	r.nt.RequireToken("something-else")
	_, _, b := r.do("POST", path, r.owner, nil)
	var res map[string]any
	_ = json.Unmarshal(b, &res)
	if res["outcome"] != "failed" || res["http_status"] != float64(401) || bytes.Contains(b, []byte("tk_abc")) {
		t.Fatalf("%s", b)
	}
	got2 := r.json("GET", "/v1/push/targets", r.owner, nil, 200)["targets"].([]any)[0].(map[string]any)
	if got2["status"] != "failing" || got2["last_error"] == nil {
		t.Fatalf("%v", got2)
	}
}

func TestTestPushWithoutSenderIs503(t *testing.T) {
	r := newPushRig(t, false)
	tg := r.json("POST", "/v1/push/targets", r.phone, map[string]any{"kind": "ntfy", "topic": "x"}, 201)
	if st, _, _ := r.do("POST", "/v1/push/targets/"+tg["id"].(string)+"/test", r.phone, nil); st != 503 {
		t.Fatalf("%d", st)
	}
}

// No response of any push endpoint contains a stored secret.
func TestSecretsNeverInResponsesOrErrors(t *testing.T) {
	r := newPushRig(t, true)
	const tok = "tk_never_shown_8f3a"
	tg := r.json("POST", "/v1/push/targets", r.owner, map[string]any{"kind": "ntfy", "server": r.nt.URL(), "topic": "s1", "token": tok}, 201)
	id := tg["id"].(string)
	r.nt.Script(ntfytest.Reply{Status: 403, Body: "token " + tok + " rejected"})
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/v1/push/targets", nil}, {"GET", "/v1/push/config", nil},
		{"PATCH", "/v1/push/targets/" + id, map[string]any{"name": "n"}},
		{"POST", "/v1/push/targets/" + id + "/test", nil},
		{"GET", "/v1/push/targets", nil},
	} {
		_, _, b := r.do(c.method, c.path, r.owner, c.body)
		if bytes.Contains(b, []byte(tok)) {
			t.Fatalf("%s %s leaked the token: %s", c.method, c.path, b)
		}
	}
}
