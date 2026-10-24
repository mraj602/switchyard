package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/pair"
)

// boardLogin posts token to /v1/session/login and returns the cookie's session token.
func (h *pairHub) boardLogin(token string) string {
	h.t.Helper()
	r := h.call("POST", "/v1/session/login", map[string]string{"token": token}, opts{origin: h.http.URL})
	if r.code != 204 {
		h.t.Fatalf("login: %d %s", r.code, r.body)
	}
	return sessionCookie(h.t, r).Value
}

func (h *pairHub) liveNamed(prefix string) int {
	h.t.Helper()
	ds, err := h.st.Devices(context.Background(), false)
	if err != nil {
		h.t.Fatal(err)
	}
	n := 0
	for _, d := range ds {
		if strings.HasPrefix(d.Name, prefix) {
			n++
		}
	}
	return n
}

// The desktop app's Hub view signs in with its desktop token at every start (or after a lost
// cookie): it keeps one board session, not one per start, and /v1/me says it is the app's.
func TestDesktopBoardSessionIsReplacedAndSaysVia(t *testing.T) {
	h := newPairHub(t)
	ctx := context.Background()
	dk, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "desktop", Name: "Watchyard-desktop-box", Machine: "m_box"})
	if err != nil {
		t.Fatal(err)
	}
	var cookies []string
	for range 3 {
		cookies = append(cookies, h.boardLogin(dk.Token))
	}
	if n := h.liveNamed("board via Watchyard-desktop-box"); n != 1 {
		t.Errorf("%d live board sessions of the desktop device, want 1", n)
	}
	for i, c := range cookies[:2] {
		if r := h.call("GET", "/v1/me", nil, opts{cookie: c}); r.code != 401 {
			t.Errorf("board session %d still works after a newer login: %d", i, r.code)
		}
	}
	me := h.call("GET", "/v1/me", nil, opts{cookie: cookies[2]}).json(t)
	via, _ := me["via"].(map[string]any)
	if via["kind"] != "desktop" || via["id"] != dk.Device.ID || via["name"] != "Watchyard-desktop-box" {
		t.Errorf("via %v", me["via"])
	}
	if dev := me["device"].(map[string]any); dev["kind"] != "browser" || fmt20(dev["scopes"]) != "[read]" {
		t.Errorf("device %v", dev)
	}
	// The desktop token itself: no via (it is the device).
	if me := h.call("GET", "/v1/me", nil, opts{token: dk.Token}).json(t); me["via"] != nil {
		t.Errorf("desktop token via %v", me["via"])
	}

	// An owner link opened in that board session stays tied to the app: via, a name that says
	// so, the board session dropped, and revoking the desktop device revokes it.
	l, err := h.svc.CreateOwnerLink(ctx, pair.OwnerLinkRequest{URL: pairHubURL})
	if err != nil {
		t.Fatal(err)
	}
	r := h.call("POST", "/v1/session/owner", map[string]string{"secret": l.Secret, "name": "Chrome on Linux"}, opts{origin: h.http.URL, cookie: cookies[2]})
	if r.code != 204 {
		t.Fatalf("owner sign-in: %d %s", r.code, r.body)
	}
	owner := sessionCookie(t, r).Value
	me = h.call("GET", "/v1/me", nil, opts{cookie: owner}).json(t)
	via, _ = me["via"].(map[string]any)
	dev := me["device"].(map[string]any)
	if me["owner"] != true || via["kind"] != "desktop" || via["id"] != dk.Device.ID {
		t.Errorf("owner me %v", me)
	}
	if dev["name"] != "Owner session in Watchyard-desktop-box" || dev["created_by"] != "owner-link:"+l.ID+"@"+dk.Device.ID {
		t.Errorf("owner device %v", dev)
	}
	if r := h.call("GET", "/v1/me", nil, opts{cookie: cookies[2]}); r.code != 401 {
		t.Errorf("the board session survived the owner sign-in: %d", r.code)
	}
	if err := auth.Revoke(ctx, h.st, dk.Device.ID); err != nil {
		t.Fatal(err)
	}
	if r := h.call("GET", "/v1/me", nil, opts{cookie: owner}); r.code != 401 {
		t.Errorf("the app's owner session survived revoking the desktop device: %d", r.code)
	}
}

// A token pasted into browsers is not a desktop app: each login is a session of its own,
// and neither those nor a plain owner session have a desktop via.
func TestBrowserLoginsKeepTheirSessions(t *testing.T) {
	h := newPairHub(t)
	ctx := context.Background()
	bt, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "browser", Name: "pasted"})
	if err != nil {
		t.Fatal(err)
	}
	a, b := h.boardLogin(bt.Token), h.boardLogin(bt.Token)
	for _, c := range []string{a, b} {
		me := h.call("GET", "/v1/me", nil, opts{cookie: c}).json(t)
		if via, _ := me["via"].(map[string]any); via["kind"] != "browser" {
			t.Errorf("via %v", me["via"])
		}
	}
	if n := h.liveNamed("board via pasted"); n != 2 {
		t.Errorf("%d live sessions, want 2", n)
	}
	o := h.ownerCookie()
	if me := h.call("GET", "/v1/me", nil, opts{cookie: o}).json(t); me["via"] != nil {
		t.Errorf("owner via %v", me["via"])
	}
}

func fmt20(v any) string {
	s, _ := v.([]any)
	parts := make([]string, len(s))
	for i, x := range s {
		parts[i], _ = x.(string)
	}
	return "[" + strings.Join(parts, " ") + "]"
}
