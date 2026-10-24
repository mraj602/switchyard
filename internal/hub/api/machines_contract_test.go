package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/drainproto"
	"github.com/mraj602/watchyard/internal/hub/store"
)

func TestHealthSaysWhatTheHubReads(t *testing.T) {
	h := startHub(t, "")
	code, body := h.do("GET", "/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("/health: %d %s", code, body)
	}
	var got struct {
		RecordVMax int             `json:"record_v_max"`
		BatchV     []int           `json:"batch_v"`
		DrainProto int             `json:"drain_proto"`
		Schema     int             `json:"schema"`
		Frozen     json.RawMessage `json:"frozen"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.RecordVMax != 1 || len(got.BatchV) != 1 || got.BatchV[0] != 1 || got.DrainProto != drainproto.Proto || got.Schema != store.LatestVersion() {
		t.Fatalf("/health: %s", body)
	}
	if got.Frozen != nil {
		t.Fatalf("frozen present on a live hub: %s", body)
	}

	// hub.frozen (machines.md 8.1) shows as frozen {since, moved_to}; /health stays 200.
	since := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		return tx.PutAlertSetting("hub.frozen", []byte(`{"since":`+itoa(since.UnixMilli())+`,"moved_to":"https://new.example.ts.net"}`), since.UnixMilli())
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body = h.do("GET", "/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("/health frozen: %d %s", code, body)
	}
	var fr struct {
		Frozen struct {
			Since   string  `json:"since"`
			MovedTo *string `json:"moved_to"`
		} `json:"frozen"`
	}
	if err := json.Unmarshal(body, &fr); err != nil {
		t.Fatal(err)
	}
	if fr.Frozen.Since != "2026-10-13T09:00:00.000Z" || fr.Frozen.MovedTo == nil || *fr.Frozen.MovedTo != "https://new.example.ts.net" {
		t.Fatalf("frozen: %s", body)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The machine operations are registered: none answers 501, and each
// needs a token with its api.yaml scope.
func TestMachineOperationsNeedTheirScopes(t *testing.T) {
	h := startHub(t, "")
	owner := ownerToken(t, h)
	phone := h.token("phone")
	machine := h.token("machine")
	for _, c := range []struct{ method, path, tok string }{
		{"PATCH", "/v1/machines/m_x", owner},
		{"DELETE", "/v1/machines/m_x", owner},
		{"POST", "/v1/machines/m_x/pull", owner},
		{"GET", "/v1/push/direct-policy", machine},
	} {
		if code, body := h.do(c.method, c.path, "", []byte("{}")); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d %s, want 401", c.method, c.path, code, body)
		}
		if code, body := h.do(c.method, c.path, phone, []byte("{}")); code != http.StatusForbidden {
			t.Errorf("%s %s with a phone token: %d %s, want 403", c.method, c.path, code, body)
		}
		if code, body := h.do(c.method, c.path, c.tok, []byte("{}")); code == http.StatusNotImplemented || code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Errorf("%s %s with its scope: %d %s", c.method, c.path, code, body)
		}
	}
}
