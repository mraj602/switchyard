package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
)

// legacyToken turns a new token into the shape releases before the rename made (rename.md 2.12).
func legacyToken(tok string) string { return LegacyTokenPrefix + strings.TrimPrefix(tok, TokenPrefix) }

// Legacy compat: sy_ tokens made before the rename keep their shape, owner prefix and hash.
func TestLegacyTokenShape(t *testing.T) {
	for _, kind := range []string{"machine", "desktop", "phone", "browser", "owner"} {
		tok, err := NewToken(kind)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tok, "wy_") {
			t.Fatalf("new %s token %q", kind, tok[:5])
		}
		old := legacyToken(tok)
		if !strings.HasPrefix(old, "sy_") || !LooksLikeToken(old) {
			t.Errorf("legacy %s token refused: %q", kind, old[:5])
		}
		if IsOwnerToken(old) != (kind == "owner") || IsOwnerToken(tok) != (kind == "owner") {
			t.Errorf("IsOwnerToken %s: new %v, legacy %v", kind, IsOwnerToken(tok), IsOwnerToken(old))
		}
	}
	for _, bad := range []string{"zy_m_" + strings.Repeat("a", 43), "sy_m_short", "sy-m_" + strings.Repeat("a", 43)} {
		if LooksLikeToken(bad) {
			t.Errorf("LooksLikeToken(%q)", bad)
		}
	}
}

// Legacy compat: a device paired before the rename (its row holds the hash of an sy_ token)
// still authenticates, as a bearer and as an owner session.
func TestLegacyTokensAuthenticate(t *testing.T) {
	a, st, _ := newAuth(t)
	ctx := context.Background()
	// legacyRow stores a prepared device as a release before the rename did: the hash (plain
	// or peppered) of its sy_ token.
	legacyRow := func(req CreateRequest) (string, store.Device) {
		p, err := Prepare(req, t0)
		if err != nil {
			t.Fatal(err)
		}
		old := legacyToken(p.Token)
		p.Hash, p.Device.TokenHint = Hash(old), Hint(old)
		if req.Owner {
			p.Hash = OwnerHash(testPepper, old)
		}
		if err := st.Write(ctx, func(tx *store.Tx) error { return p.Insert(tx) }); err != nil {
			t.Fatal(err)
		}
		return old, p.Device
	}
	old, d := legacyRow(CreateRequest{Kind: "machine", Name: "laptop", Machine: "m_1"})
	if got, err := a.Lookup(ctx, old); err != nil || got.ID != d.ID || !strings.HasPrefix(old, "sy_m_") {
		t.Fatalf("legacy machine token: %+v %v", got, err)
	}
	// An owner session made before the rename: an sy_o_ token whose hash is keyed with the pepper.
	oold, o := legacyRow(CreateRequest{Kind: "browser", Name: "owner", Owner: true, CreatedBy: "owner-link:pc_x", Pepper: testPepper})
	if got, err := a.LookupDetail(ctx, oold); err != nil || got.Device.ID != o.ID || !got.HasScope("owner") || !strings.HasPrefix(oold, "sy_o_") {
		t.Fatalf("legacy owner token: %+v %v", got, err)
	}
}

// Legacy compat: the board's cookie was sy_session before the rename. The hub reads it when
// wy_session is absent, sets only wy_session, and logout clears both.
func TestLegacySessionCookie(t *testing.T) {
	a, st, _ := newAuth(t)
	c, err := Create(context.Background(), st, CreateRequest{Kind: "browser", Name: "b", CreatedBy: "login:dev_x", Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Require(Options{}, "read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, name := range []string{CookieName, LegacyCookieName} {
		r := httptest.NewRequest("GET", "/v1/overview", nil)
		r.AddCookie(&http.Cookie{Name: name, Value: c.Token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Errorf("cookie %s: %d", name, w.Code)
		}
	}
	// wy_session wins over a stale sy_session.
	r := httptest.NewRequest("GET", "/v1/overview", nil)
	r.AddCookie(&http.Cookie{Name: LegacyCookieName, Value: "sy_b_stale"})
	r.AddCookie(&http.Cookie{Name: CookieName, Value: c.Token})
	if v, ok := SessionCookieValue(r); !ok || v != c.Token {
		t.Errorf("SessionCookieValue = %q, %v", v, ok)
	}
	if sc := SessionCookie(r, c.Token, time.Hour); sc.Name != "wy_session" {
		t.Errorf("sets %s", sc.Name)
	}
	w := httptest.NewRecorder()
	ClearCookie(w)
	got := strings.Join(w.Header().Values("Set-Cookie"), "\n")
	if !strings.Contains(got, "wy_session=;") || !strings.Contains(got, "sy_session=;") {
		t.Errorf("ClearCookie: %s", got)
	}
}
