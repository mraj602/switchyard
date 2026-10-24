package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

const (
	acctUUID = "3f1c0000-0000-4000-8000-000000000001"
	orgUUID  = "9b200000-0000-4000-8000-000000000002"
)

func webUsageRec(t *testing.T, machine, tab string, at time.Time, usage json.RawMessage, errPayload json.RawMessage) []byte {
	t.Helper()
	p := record.WebUsagePayload{TabID: tab, Usage: usage, Error: errPayload,
		Org:     json.RawMessage(`{"name":"Work Inc","capabilities":["chat"],"rate_limit_tier":"default_claude_max_5x"}`),
		Account: json.RawMessage(`{"email_address":"a@example.com","display_name":"A"}`)}
	r, err := record.NewWebUsage(record.Meta{Machine: machine, Client: "watchyard-desktop/0.6.0", Now: at,
		Seat: &record.Seat{Account: acctUUID, Org: orgUUID, Email: "a@example.com", Src: "claude_ai"}}, p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

func slotInfoRec(t *testing.T, machine string, at time.Time, label, colour string) []byte {
	t.Helper()
	extra := record.Extra{}
	if label != "" {
		extra["label"], _ = json.Marshal(label)
	}
	if colour != "" {
		extra["colour"], _ = json.Marshal(colour)
	}
	oa, _ := json.Marshal(map[string]string{"accountUuid": acctUUID, "emailAddress": "a@example.com", "organizationUuid": orgUUID})
	r, err := record.NewSlotInfo(record.Meta{Machine: machine, Slot: "~/.claude", Client: "wy/0.6.0", Now: at},
		record.SlotInfoPayload{LoggedIn: true, OAuthAccount: oa, Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

type seatJSON struct {
	ID         string                 `json:"id"`
	Label      *string                `json:"label"`
	Colour     *string                `json:"colour"`
	Order      int64                  `json:"order"`
	Hidden     bool                   `json:"hidden"`
	Windows    []struct{ Key string } `json:"windows"`
	ExtraUsage json.RawMessage        `json:"extra_usage"`
	Spend      json.RawMessage        `json:"spend"`
	Places     []struct {
		Machine       string  `json:"machine"`
		TabID         *string `json:"tab_id"`
		StoppedReason *string `json:"stopped_reason"`
	} `json:"places"`
}

func (h *hub) onlySeat(owner string) seatJSON {
	h.t.Helper()
	var list struct {
		Accounts []seatJSON `json:"accounts"`
	}
	h.get("/v1/accounts?include_hidden=true", owner, &list)
	if len(list.Accounts) != 1 {
		h.t.Fatalf("%d seats", len(list.Accounts))
	}
	return list.Accounts[0]
}

func usageDoc(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, "testdata", "usage", "response-02.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The path the desktop app uses: web_usage from a MACHINE device joins web_seat and fills
// windows, extra_usage and spend; stopped_reason shows in places.
func TestWebUsageFromMachineFillsSeat(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	rep := h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, usageDoc(t), nil))
	if len(rep.Accepted) != 1 {
		t.Fatalf("%+v", rep)
	}
	s := h.onlySeat(owner)
	if len(s.Windows) == 0 || string(s.ExtraUsage) == "null" || len(s.ExtraUsage) == 0 {
		t.Fatalf("seat not filled: %+v", s)
	}
	if len(s.Places) != 1 || s.Places[0].TabID == nil || s.Places[0].StoppedReason != nil || s.Places[0].Machine != h.machine {
		t.Fatalf("places: %+v", s.Places)
	}
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0.Add(time.Minute), nil, json.RawMessage(`{"status":429,"message":"slow down"}`)))
	s = h.onlySeat(owner)
	if s.Places[0].StoppedReason == nil || *s.Places[0].StoppedReason != "429" {
		t.Fatalf("stopped_reason: %+v", s.Places)
	}
}

func TestPatchSeat(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	desktop := h.token("desktop")
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, usageDoc(t), nil))
	id := h.onlySeat(owner).ID
	path := "/v1/accounts/" + id

	for _, c := range []struct {
		name, tok, path, body string
		want                  int
	}{
		{"owner", owner, path, `{"label":"work","colour":"#14b8a6"}`, 200},
		{"read token", h.token("phone"), path, `{"label":"x"}`, 403},
		{"desktop token", desktop, path, `{"label":"x"}`, 403},
		{"machine token", machine, path, `{"label":"x"}`, 403},
		{"no token", "", path, `{"label":"x"}`, 401},
		{"bad label", owner, path, `{"label":"Work Inc"}`, 400},
		{"long label", owner, path, `{"label":"aaaaaaaaaaaaaaaaaaaaaaaaa"}`, 400},
		{"bad colour", owner, path, `{"colour":"#14B8A6"}`, 400},
		{"unknown key", owner, path, `{"name":"x"}`, 400},
		{"empty", owner, path, `{}`, 400},
		{"not an object", owner, path, `[]`, 400},
		{"null order", owner, path, `{"order":null}`, 400},
		{"unknown seat", owner, "/v1/accounts/seat_NOPE", `{"label":"x"}`, 404},
	} {
		if code, b := h.do("PATCH", c.path, c.tok, []byte(c.body)); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, b, c.want)
		}
	}
	s := h.onlySeat(owner)
	if s.Label == nil || *s.Label != "work" || s.Colour == nil || *s.Colour != "#14b8a6" {
		t.Fatalf("after owner patch: %+v", s)
	}
	// An owner patch publishes a `usage` event for the seat, so open boards and the desktop
	// app (which refetch on it) redraw the label.
	ch, cancel := openStream(t, h, "?types=usage", h.token("phone"), "")
	defer cancel()
	next(t, ch, "hello", 2*time.Second)
	if code, b := h.do("PATCH", path, owner, []byte(`{"label":"home"}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if e := next(t, ch, "usage", 2*time.Second); !strings.Contains(e.data, id) {
		t.Fatalf("usage event for another seat: %+v", e)
	}
	if code, b := h.do("PATCH", path, owner, []byte(`{"label":"work"}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	// A missing key leaves it, null clears it, order and hidden apply.
	if code, b := h.do("PATCH", path, owner, []byte(`{"colour":null,"order":7,"hidden":true}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	s = h.onlySeat(owner)
	if s.Label == nil || *s.Label != "work" || s.Colour != nil || s.Order != 7 || !s.Hidden {
		t.Fatalf("after second patch: %+v", s)
	}
	if n := h.count(`SELECT count(*) FROM seat WHERE id = ? AND look_src = 'ui'`, id); n != 1 {
		t.Fatal("look_src is not 'ui'")
	}
}

// A slot_info label after a UI label leaves the UI label; on a seat with look_src NULL it
// sets the label; a second slot_info label still follows while look_src is 'slot'.
func TestSlotInfoLabelPrecedence(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, usageDoc(t), nil))
	s := h.onlySeat(owner)
	if s.Label != nil {
		t.Fatalf("seat born with a label: %+v", s)
	}
	if n := h.count(`SELECT count(*) FROM seat WHERE look_src IS NULL`); n != 1 {
		t.Fatal("look_src should be NULL before any label")
	}

	h.ingest(machine, slotInfoRec(t, h.machine, t0.Add(time.Minute), "personal", "#f97316"))
	s = h.onlySeat(owner)
	if s.Label == nil || *s.Label != "personal" || s.Colour == nil || *s.Colour != "#f97316" {
		t.Fatalf("slot_info on NULL look_src: %+v", s)
	}
	if n := h.count(`SELECT count(*) FROM seat WHERE look_src = 'slot'`); n != 1 {
		t.Fatal("look_src should be 'slot'")
	}
	h.ingest(machine, slotInfoRec(t, h.machine, t0.Add(2*time.Minute), "home", ""))
	if s = h.onlySeat(owner); *s.Label != "home" || *s.Colour != "#f97316" {
		t.Fatalf("slot_info on 'slot': %+v", s)
	}

	if code, b := h.do("PATCH", "/v1/accounts/"+s.ID, owner, []byte(`{"label":"work"}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	h.ingest(machine, slotInfoRec(t, h.machine, t0.Add(3*time.Minute), "other", "#64748b"))
	s = h.onlySeat(owner)
	if *s.Label != "work" || *s.Colour != "#f97316" {
		t.Fatalf("UI label overridden by slot_info: %+v", s)
	}
}
