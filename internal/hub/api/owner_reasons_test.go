package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// UI audit O1: a refused owner sign-in says why (pairing.md 4): used, expired, cancelled or
// unknown, with when.
func TestOwnerSignInReasons(t *testing.T) {
	h := newPairHub(t)
	same := opts{origin: h.http.URL}
	ctx := context.Background()
	link := func() pair.OwnerLink {
		l, err := h.svc.CreateOwnerLink(ctx, pair.OwnerLinkRequest{URL: pairHubURL})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	used := link()
	if r := h.call("POST", "/v1/session/owner", map[string]string{"secret": used.Secret}, same); r.code != 204 {
		t.Fatalf("first sign-in %d %s", r.code, r.body)
	}
	expired := link()
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE pair_code SET expires_at = 1000 WHERE id = ?`, expired.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cancelled := link()
	if err := h.svc.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	// A phone code's secret is not an owner link.
	phone, err := h.svc.Create(ctx, pair.CreateRequest{Kind: "phone", URL: pairHubURL})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, secret, code, msg string
		at                      bool
	}{
		{"used", used.Secret, "link_used", "already used", true},
		{"expired", expired.Secret, "link_expired", "expired", true},
		{"cancelled", cancelled.Secret, "link_cancelled", "cancelled", true},
		{"unknown", strings.Repeat("A", 22), "link_unknown", "does not know", false},
		{"phone code", phone.Secret + strings.Repeat("x", max(0, 22-len(phone.Secret))), "link_unknown", "does not know", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.call("POST", "/v1/session/owner", map[string]string{"secret": tc.secret}, same)
			if r.code != 401 {
				t.Fatalf("%d %s", r.code, r.body)
			}
			m := r.json(t)
			if m["code"] != tc.code || !strings.Contains(m["error"].(string), tc.msg) {
				t.Fatalf("%v", m)
			}
			if _, has := m["at"]; has != tc.at {
				t.Fatalf("at: %v", m)
			}
		})
	}
	if tc := h.call("POST", "/v1/session/owner", map[string]string{"secret": expired.Secret}, same).json(t); tc["at"] != "1970-01-01T00:00:01.000Z" {
		t.Errorf("expired at %v", tc["at"])
	}
}
