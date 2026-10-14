package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// Review finding 13: /health and ingest agree on an unparseable hub.frozen: both say frozen.
func TestHealthTreatsUnparseableFrozenAsFrozen(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		return tx.PutAlertSetting(api.SettingFrozen, []byte(`"garbage"`), 1)
	}); err != nil {
		t.Fatal(err)
	}
	code, body := h.do("GET", "/health", "", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"frozen"`) {
		t.Fatalf("/health: %d %s", code, body)
	}
	code, body = h.do("POST", "/v1/ingest", tok, batch(machineInfoRec(t, h.machine)))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("ingest: %d %s", code, body)
	}
}

// Review finding 10: a pairing claim is refused with the frozen 503 while the hub is frozen,
// and the code stays usable after the unfreeze.
func TestPairClaimRefusedWhileFrozen(t *testing.T) {
	h := newPairHub(t)
	c, err := h.svc.Create(context.Background(), pair.CreateRequest{Kind: "phone", URL: pairHubURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		return tx.PutAlertSetting(api.SettingFrozen, []byte(`{"since":1,"moved_to":null}`), 1)
	}); err != nil {
		t.Fatal(err)
	}
	claim := map[string]any{"secret": c.Secret, "name": "p", "cookie": true, "expect_kind": "phone"}
	r := h.call("POST", "/v1/pair/claim", claim, opts{origin: h.http.URL})
	if r.code != http.StatusServiceUnavailable || r.header.Get("Retry-After") == "" || !strings.Contains(string(r.body), "frozen") {
		t.Fatalf("claim while frozen: %d %v %s", r.code, r.header, r.body)
	}
	if _, err := h.st.Unfreeze(context.Background()); err != nil {
		t.Fatal(err)
	}
	r = h.call("POST", "/v1/pair/claim", claim, opts{origin: h.http.URL})
	if r.code >= 300 {
		t.Fatalf("claim after unfreeze: %d %s", r.code, r.body)
	}
}
