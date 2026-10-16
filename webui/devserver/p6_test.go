package devserver

import "testing"

func TestPatchSeatMock(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)
	plain := newClient(t, ts)
	plain.cookie("sy_b_whatever")

	phone.want(403, "PATCH", "/v1/accounts/seat_work", M{"label": "x"})
	owner.want(404, "PATCH", "/v1/accounts/seat_nope", M{"label": "x"})
	owner.want(400, "PATCH", "/v1/accounts/seat_work", M{})
	owner.want(400, "PATCH", "/v1/accounts/seat_work", M{"label": "Bad Label"})
	owner.want(400, "PATCH", "/v1/accounts/seat_work", M{"colour": "#ABCDEF"})
	owner.want(400, "PATCH", "/v1/accounts/seat_work", M{"nope": 1})
	got := owner.want(200, "PATCH", "/v1/accounts/seat_work", M{"label": "day-job", "colour": "#14b8a6"})
	if got["label"] != "day-job" || got["colour"] != "#14b8a6" || got["look_src"] != "ui" {
		t.Fatalf("patched seat: %v", got)
	}
	got = owner.want(200, "PATCH", "/v1/accounts/seat_work", M{"colour": nil})
	if got["label"] != "day-job" || got["colour"] != nil {
		t.Fatalf("null clears colour only: %v", got)
	}
}

func TestPutSeatOrderMock(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)

	phone.want(403, "PUT", "/v1/seats/order", M{"ids": []string{"seat_work"}})
	owner.want(400, "PUT", "/v1/seats/order", M{"ids": []string{"seat_nope"}})
	owner.want(400, "PUT", "/v1/seats/order", M{"ids": []string{"seat_work", "seat_work"}})
	owner.want(400, "PUT", "/v1/seats/order", M{})
	got := owner.want(200, "PUT", "/v1/seats/order", M{"ids": []string{"seat_work"}})
	list, _ := got["accounts"].([]any)
	if len(list) < 2 || list[0].(map[string]any)["id"] != "seat_work" || list[0].(map[string]any)["order"] != float64(0) || list[1].(map[string]any)["order"] != float64(1) {
		t.Fatalf("order after put: %v", got)
	}
	ov := owner.want(200, "GET", "/v1/overview", nil)
	if seats, _ := ov["seats"].([]any); len(seats) == 0 || seats[0].(map[string]any)["id"] != "seat_work" {
		t.Fatalf("overview order: %v", ov["seats"])
	}
}

func TestDesktopCodeMock(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	owner.want(201, "POST", "/v1/pair/codes", M{"kind": "desktop"})

	c := newClient(t, ts)
	c.want(409, "POST", "/v1/pair/claim", M{"secret": PairSecret, "expect_kind": "phone", "machine": "m_x"})
	c.want(400, "POST", "/v1/pair/claim", M{"secret": PairSecret, "expect_kind": "desktop"})
	got := c.want(200, "POST", "/v1/pair/claim", M{"secret": PairSecret, "expect_kind": "desktop", "machine": "m_x", "name": "laptop"})
	if got["kind"] != "desktop" || got["token"] != DesktopToken || got["machine"] != "m_x" {
		t.Fatalf("claim reply: %v", got)
	}

	// A phone code still refuses expect_kind desktop.
	owner.want(201, "POST", "/v1/pair/codes", M{"kind": "phone"})
	c.want(409, "POST", "/v1/pair/claim", M{"secret": PairSecret, "expect_kind": "desktop", "machine": "m_x"})
}
