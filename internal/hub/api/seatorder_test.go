package api_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func seatIDs(h *hub, owner string) []string {
	h.t.Helper()
	var list struct {
		Accounts []seatJSON `json:"accounts"`
	}
	h.get("/v1/accounts?include_hidden=true", owner, &list)
	out := make([]string, 0, len(list.Accounts))
	for _, s := range list.Accounts {
		out = append(out, s.ID)
	}
	return out
}

func sameIDs(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// PUT /v1/seats/order: owner only, every id must exist and none may repeat, a partial list
// puts its seats first, the order is the one GET /v1/accounts and /v1/overview return, and a
// `seats` stream event carries it.
func TestPutSeatOrder(t *testing.T) {
	h := startHub(t, "")
	owner := ownerToken(t, h)
	h.exec(`INSERT INTO account (uuid, email, first_seen, last_seen) VALUES ('acc_a', 'a@x.io', 1, 1), ('acc_b', 'b@x.io', 1, 1), ('acc_c', 'c@x.io', 1, 1)`)
	h.exec(`INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org', 1, 1)`)
	h.exec(`INSERT INTO seat (id, account_uuid, org_uuid, created_at) VALUES ('seat_a', 'acc_a', 'org', 1), ('seat_b', 'acc_b', 'org', 2), ('seat_c', 'acc_c', 'org', 3)`)
	if got := seatIDs(h, owner); !sameIDs(got, []string{"seat_a", "seat_b", "seat_c"}) {
		t.Fatalf("default order %v", got)
	}

	put := func(tok, body string) (int, []byte) { return h.do("PUT", "/v1/seats/order", tok, []byte(body)) }
	for _, c := range []struct {
		name, tok, body string
		want            int
	}{
		{"read token", h.token("phone"), `{"ids":["seat_c"]}`, 403},
		{"desktop token", h.token("desktop"), `{"ids":["seat_c"]}`, 403},
		{"no token", "", `{"ids":["seat_c"]}`, 401},
		{"unknown id", owner, `{"ids":["seat_c","seat_zzz"]}`, 400},
		{"duplicate id", owner, `{"ids":["seat_c","seat_c"]}`, 400},
		{"no ids key", owner, `{}`, 400},
		{"ids null", owner, `{"ids":null}`, 400},
		{"unknown key", owner, `{"ids":[],"x":1}`, 400},
		{"not an object", owner, `[]`, 400},
	} {
		if code, b := put(c.tok, c.body); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, b, c.want)
		}
	}
	if got := seatIDs(h, owner); !sameIDs(got, []string{"seat_a", "seat_b", "seat_c"}) {
		t.Fatalf("a refused order changed the seats: %v", got)
	}

	ch, cancel := openStream(t, h, "?types=seats", h.token("phone"), "")
	defer cancel()
	next(t, ch, "hello", 2*time.Second)

	// A full list.
	code, b := put(owner, `{"ids":["seat_c","seat_a","seat_b"]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var reply struct {
		Accounts []seatJSON `json:"accounts"`
	}
	if err := json.Unmarshal(b, &reply); err != nil || len(reply.Accounts) != 3 || reply.Accounts[0].ID != "seat_c" || reply.Accounts[0].Order != 0 || reply.Accounts[2].Order != 2 {
		t.Fatalf("reply %s (%v)", b, err)
	}
	e := next(t, ch, "seats", 2*time.Second)
	var ev struct {
		Order []string `json:"order"`
	}
	if err := json.Unmarshal([]byte(e.data), &ev); err != nil || !sameIDs(ev.Order, []string{"seat_c", "seat_a", "seat_b"}) {
		t.Fatalf("seats event %s (%v)", e.data, err)
	}
	if got := seatIDs(h, owner); !sameIDs(got, []string{"seat_c", "seat_a", "seat_b"}) {
		t.Fatalf("after full put: %v", got)
	}
	var ov struct {
		Seats []seatJSON `json:"seats"`
	}
	h.get("/v1/overview", h.token("phone"), &ov)
	if len(ov.Seats) != 3 || ov.Seats[0].ID != "seat_c" || ov.Seats[2].ID != "seat_b" {
		t.Fatalf("overview order: %+v", ov.Seats)
	}

	// A partial list: its seats first, the rest after them in their old order.
	if code, b := put(owner, `{"ids":["seat_b"]}`); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if got := seatIDs(h, owner); !sameIDs(got, []string{"seat_b", "seat_c", "seat_a"}) {
		t.Fatalf("after partial put: %v", got)
	}
	// An empty list changes nothing but renumbers.
	if code, b := put(owner, `{"ids":[]}`); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if got := seatIDs(h, owner); !sameIDs(got, []string{"seat_b", "seat_c", "seat_a"}) {
		t.Fatalf("after empty put: %v", got)
	}
}
