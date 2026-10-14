package api_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/push/expo"
	"github.com/mraj602/switchyard/internal/hub/push/expotest"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/ntfytest"
	"github.com/mraj602/switchyard/internal/hub/push/webpushtest"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// Expo registration, the expo channel and the test push (push.md 12.5, 12.7; phases/8.md
// WP-H), with the real expo sender pointed at the fake Expo service.

const expoTok = "ExponentPushToken[abcdefghij0123]"

func newExpoRig(t *testing.T) (*pushRig, *expotest.Server) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := expotest.New()
	r := &pushRig{t: t, st: st, wp: webpushtest.New(), nt: ntfytest.New(), clock: &pushClock{t: time.Now()}}
	sender := expo.New(expo.Options{Store: st, Client: f.Client()})
	srv := api.New(api.Config{Store: st, Now: r.clock.now, PingEvery: time.Hour, PublicURL: "https://hub.example.ts.net",
		Senders: map[string]model.Sender{model.KindExpo: sender}})
	r.http = httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		r.http.Close()
		r.wp.Close()
		r.nt.Close()
		f.Close()
		st.Close()
	})
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
	return r, f
}

func expoBody(tok string) map[string]any {
	return map[string]any{"kind": "expo", "expo_token": tok, "platform": "ios", "name": "iPhone · app",
		"app": map[string]any{"version": "0.8.0", "build": "12"}}
}

func TestAddExpoTarget(t *testing.T) {
	r, _ := newExpoRig(t)
	got := r.json("POST", "/v1/push/targets", r.phone, expoBody(expoTok), 201)
	if got["kind"] != "expo" || got["platform"] != "ios" || got["app_version"] != "0.8.0" || got["endpoint_host"] != "exp.host" ||
		got["device"] != r.deviceID(r.phone) || got["name"] != "iPhone · app" || got["status"] != "new" {
		t.Fatalf("%v", got)
	}
	id := got["id"].(string)
	tg, err := r.st.TargetByID(context.Background(), id)
	if err != nil || tg.Meta == nil || tg.Meta.AppBuild != "12" || tg.Endpoint != expoTok {
		t.Fatalf("%+v %v", tg, err)
	}
	// Registering again (app start) updates the same target: no duplicate.
	b := expoBody(expoTok)
	b["app"] = map[string]any{"version": "0.8.1"}
	again := r.json("POST", "/v1/push/targets", r.phone, b, 201)
	if again["id"] != id || again["app_version"] != "0.8.1" {
		t.Fatalf("%v", again)
	}
	list := r.json("GET", "/v1/push/targets", r.phone, nil, 200)["targets"].([]any)
	if len(list) != 1 {
		t.Fatalf("%v", list)
	}
	// Another live device cannot take the token over (409); once that device is revoked,
	// the token rebinds (a reinstall paired again).
	if st, _, _ := r.do("POST", "/v1/push/targets", r.other, expoBody(expoTok)); st != 409 {
		t.Fatalf("takeover: %d", st)
	}
	if err := auth.Revoke(context.Background(), r.st, r.deviceID(r.phone)); err != nil {
		t.Fatal(err)
	}
	moved := r.json("POST", "/v1/push/targets", r.other, expoBody(expoTok), 201)
	if moved["device"] != r.deviceID(r.other) || moved["platform"] != "ios" {
		t.Fatalf("%v", moved)
	}
	// Default name.
	b = expoBody("ExpoPushToken[zzzzzzzzzzzz]")
	delete(b, "name")
	if n := r.json("POST", "/v1/push/targets", r.other, b, 201)["name"]; n != "App" {
		t.Fatalf("name %v", n)
	}
}

func TestAddExpoTargetRejects(t *testing.T) {
	r, _ := newExpoRig(t)
	for name, mut := range map[string]func(map[string]any){
		"no token":       func(b map[string]any) { delete(b, "expo_token") },
		"bad token":      func(b map[string]any) { b["expo_token"] = "https://exp.host/x" },
		"short token":    func(b map[string]any) { b["expo_token"] = "ExponentPushToken[abc]" },
		"no platform":    func(b map[string]any) { delete(b, "platform") },
		"bad platform":   func(b map[string]any) { b["platform"] = "web" },
		"long version":   func(b map[string]any) { b["app"] = map[string]any{"version": strings.Repeat("9", 33)} },
		"long build":     func(b map[string]any) { b["app"] = map[string]any{"build": strings.Repeat("9", 33)} },
		"long name":      func(b map[string]any) { b["name"] = strings.Repeat("n", 61) },
		"unknown kind":   func(b map[string]any) { b["kind"] = "apns" },
		"token injected": func(b map[string]any) { b["expo_token"] = "ExponentPushToken[abc\"}]" },
	} {
		b := expoBody(expoTok)
		mut(b)
		if st, _, body := r.do("POST", "/v1/push/targets", r.phone, b); st != 400 {
			t.Errorf("%s: %d %s", name, st, body)
		}
	}
}

func TestExpoChannel(t *testing.T) {
	r, _ := newExpoRig(t)
	ch := func() map[string]any {
		for _, c := range r.json("GET", "/v1/push/config", r.owner, nil, 200)["channels"].([]any) {
			if m := c.(map[string]any); m["kind"] == "expo" {
				return m
			}
		}
		t.Fatal("no expo channel")
		return nil
	}
	if set := ch()["settings"].(map[string]any); set["base_url"] != "https://exp.host" || set["access_token_set"] != false || len(set) != 2 {
		t.Fatalf("%v", set)
	}
	const secret = "expo-access-token-0123456789abcdefghij"
	got := r.json("PUT", "/v1/push/channels/expo", r.owner, map[string]any{"enabled": true,
		"settings": map[string]any{"access_token": secret}}, 200)
	if set := got["settings"].(map[string]any); set["access_token_set"] != true || set["base_url"] != "https://exp.host" {
		t.Fatalf("%v", got)
	}
	// The token is write-only: never in a response or the stored settings.
	for _, path := range []string{"/v1/push/config", "/v1/push/targets"} {
		if _, _, body := r.do("GET", path, r.owner, nil); bytes.Contains(body, []byte(secret)) {
			t.Fatalf("token in %s", path)
		}
	}
	cfg, _ := r.st.ExpoConfig(context.Background())
	if cfg.AccessToken != secret {
		t.Fatal("token not stored")
	}
	// Absent keys keep values; a read-back access_token_set is ignored; base_url is set. A
	// new host without a new token drops the stored token (it must not follow the host).
	got = r.json("PUT", "/v1/push/channels/expo", r.owner, map[string]any{"enabled": false,
		"settings": map[string]any{"base_url": "https://expo.example.com/", "access_token_set": true}}, 200)
	if set := got["settings"].(map[string]any); got["enabled"] != false || set["access_token_set"] != false || set["base_url"] != "https://expo.example.com" {
		t.Fatalf("%v", got)
	}
	if cfg, _ := r.st.ExpoConfig(context.Background()); cfg.AccessToken != "" {
		t.Fatal("token followed a changed base_url")
	}
	r.json("PUT", "/v1/push/channels/expo", r.owner, map[string]any{"enabled": true,
		"settings": map[string]any{"access_token": secret}}, 200)
	// "" removes both.
	got = r.json("PUT", "/v1/push/channels/expo", r.owner, map[string]any{"enabled": true,
		"settings": map[string]any{"base_url": "", "access_token": ""}}, 200)
	if set := got["settings"].(map[string]any); set["access_token_set"] != false || set["base_url"] != "https://exp.host" {
		t.Fatalf("%v", got)
	}
	for name, set := range map[string]any{
		"http":        map[string]any{"base_url": "http://exp.host"},
		"port":        map[string]any{"base_url": "https://exp.host:8443"},
		"userinfo":    map[string]any{"base_url": "https://u:p@exp.host"},
		"query":       map[string]any{"base_url": "https://exp.host/?x=1"},
		"not string":  map[string]any{"base_url": 1},
		"token space": map[string]any{"access_token": "a b"},
		"token long":  map[string]any{"access_token": strings.Repeat("a", 513)},
		"unknown key": map[string]any{"server": "x"},
	} {
		if st, _, body := r.do("PUT", "/v1/push/channels/expo", r.owner, map[string]any{"enabled": true, "settings": set}); st != 400 {
			t.Errorf("%s: %d %s", name, st, body)
		}
	}
	if st, _, _ := r.do("PUT", "/v1/push/channels/expo", r.phone, map[string]any{"enabled": true}); st != 403 {
		t.Fatalf("phone set the channel: %d", st)
	}
}

// The access token is Expo's credential: it never follows a changed base host.
func TestExpoTokenDoesNotFollowBaseURL(t *testing.T) {
	r, _ := newExpoRig(t)
	ctx := context.Background()
	put := func(set map[string]any) {
		t.Helper()
		r.json("PUT", "/v1/push/channels/expo", r.owner, map[string]any{"enabled": true, "settings": set}, 200)
	}
	tok := func() string { c, _ := r.st.ExpoConfig(ctx); return c.AccessToken }
	put(map[string]any{"access_token": "tok-0123456789"})
	put(map[string]any{"base_url": "https://expo.example.com"})
	if tok() != "" {
		t.Fatal("token kept after base_url moved to another host")
	}
	// A token arriving with the new host is kept.
	put(map[string]any{"base_url": "https://other.example.com", "access_token": "tok-new-0123456789"})
	if tok() != "tok-new-0123456789" {
		t.Fatalf("token %q", tok())
	}
	// The same host (different path or case) and an unchanged URL keep it.
	put(map[string]any{"base_url": "https://other.example.com"})
	put(map[string]any{"base_url": "https://OTHER.example.com/"})
	if tok() != "tok-new-0123456789" {
		t.Fatal("token dropped though the host did not change")
	}
	// Back to the default host (base_url "") is a host change too.
	put(map[string]any{"base_url": ""})
	if tok() != "" {
		t.Fatal("token kept after returning to the default host")
	}
}

// A phone that unpaired while offline never revoked its device; once it has been unseen for
// 30 days another device may take its Expo token. A recently seen one may not.
func TestExpoTokenRebindFromStaleDevice(t *testing.T) {
	r, _ := newExpoRig(t)
	ctx := context.Background()
	r.json("POST", "/v1/push/targets", r.phone, expoBody(expoTok), 201)
	seen := func(ago time.Duration) {
		t.Helper()
		if err := r.st.TouchDevice(ctx, r.deviceID(r.phone), time.Now().Add(-ago).UnixMilli(), ""); err != nil {
			t.Fatal(err)
		}
	}
	seen(29 * 24 * time.Hour)
	if st, _, _ := r.do("POST", "/v1/push/targets", r.other, expoBody(expoTok)); st != 409 {
		t.Fatalf("takeover from a device seen 29 days ago: %d", st)
	}
	seen(31 * 24 * time.Hour)
	moved := r.json("POST", "/v1/push/targets", r.other, expoBody(expoTok), 201)
	if moved["device"] != r.deviceID(r.other) {
		t.Fatalf("%v", moved)
	}
}

func TestExpoTestPushAndTokenReload(t *testing.T) {
	r, f := newExpoRig(t)
	// The sender reads its base URL from the store: point it at the fake (on a random port,
	// which the API refuses, so straight into the store).
	base := f.URL()
	if _, err := r.st.SetExpoChannel(context.Background(), true, &base, nil); err != nil {
		t.Fatal(err)
	}
	id := r.json("POST", "/v1/push/targets", r.phone, expoBody(expoTok), 201)["id"].(string)
	got := r.json("POST", "/v1/push/targets/"+id+"/test", r.phone, nil, 200)
	if got["outcome"] != "sent" || got["error"] != nil {
		t.Fatalf("%v", got)
	}
	reqs := f.Requests()
	if len(reqs) != 1 || reqs[0].Err != nil || reqs[0].Msg.To != expoTok || reqs[0].Msg.Data["type"] != "test" ||
		*reqs[0].Msg.ChannelID != "sy_test" || reqs[0].Header.Get("Authorization") != "" {
		t.Fatalf("%+v", reqs)
	}
	// Setting an access token reloads the sender: the next push carries it.
	f.ExpectToken("tok-0123456789")
	r.json("PUT", "/v1/push/channels/expo", r.owner, map[string]any{"enabled": true,
		"settings": map[string]any{"access_token": "tok-0123456789"}}, 200)
	r.clock.advance(6 * time.Second)
	if got = r.json("POST", "/v1/push/targets/"+id+"/test", r.phone, nil, 200); got["outcome"] != "sent" {
		t.Fatalf("%v", got)
	}
	if h := f.Requests()[1].Header.Get("Authorization"); h != "Bearer tok-0123456789" {
		t.Fatalf("auth %q", h)
	}
	// DeviceNotRegistered marks the target gone.
	f.Script(expotest.Reply{TicketError: "DeviceNotRegistered"})
	r.clock.advance(6 * time.Second)
	if got = r.json("POST", "/v1/push/targets/"+id+"/test", r.phone, nil, 200); got["outcome"] != "gone" {
		t.Fatalf("%v", got)
	}
	if tg, _ := r.st.TargetByID(context.Background(), id); tg.GoneAt == 0 {
		t.Fatalf("%+v", tg)
	}
}
