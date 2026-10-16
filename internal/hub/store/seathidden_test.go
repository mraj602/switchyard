package store_test

import (
	"testing"
	"time"
)

// A free org beside a Team org of the same account is a stray seat (the desktop app used to
// report every org of a claude.ai account): hidden until it is labelled or a slot logs in to it.
func TestStrayFreeSeatIsHidden(t *testing.T) {
	st, ctx := openTargets(t)
	execT(t, st, ctx, `INSERT INTO account (uuid, first_seen, last_seen) VALUES ('acc', 1, 1), ('solo', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO org (uuid, plan, first_seen, last_seen) VALUES
		('team', 'team', 1, 1), ('free1', 'free', 1, 1), ('free2', 'free', 1, 1), ('free3', 'free', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO seat (id, account_uuid, org_uuid, created_at) VALUES
		('seat_team', 'acc', 'team', 1), ('seat_free', 'acc', 'free1', 2),
		('seat_a', 'solo', 'free2', 3), ('seat_b', 'solo', 'free3', 4)`)
	ids := func(include bool) map[string]bool {
		t.Helper()
		rows, err := st.Seats(ctx, include, time.Unix(10, 0))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.ID] = r.Hidden
		}
		return out
	}
	shown := ids(false)
	if _, ok := shown["seat_free"]; ok || len(shown) != 3 {
		t.Fatalf("the stray free seat is listed: %v", shown)
	}
	// two free seats of an account with no paid one are both real
	if _, ok := shown["seat_a"]; !ok {
		t.Fatalf("%v", shown)
	}
	if _, ok := shown["seat_b"]; !ok {
		t.Fatalf("%v", shown)
	}
	if all := ids(true); !all["seat_free"] || all["seat_team"] {
		t.Fatalf("include_hidden: %v", all)
	}

	// a label brings it back
	execT(t, st, ctx, `UPDATE seat SET label = 'personal' WHERE id = 'seat_free'`)
	if _, ok := ids(false)["seat_free"]; !ok {
		t.Fatal("a labelled seat is shown")
	}
	execT(t, st, ctx, `UPDATE seat SET label = NULL WHERE id = 'seat_free'`)
	if _, ok := ids(false)["seat_free"]; ok {
		t.Fatal("hidden again")
	}

	// so does a slot that was logged in to it (the person really uses that org in Claude Code)
	execT(t, st, ctx, `INSERT INTO machine (id, name, first_seen) VALUES ('m_1', 'laptop', 1)`)
	execT(t, st, ctx, `INSERT INTO slot (id, machine_id, folder, first_seen, last_seen) VALUES (1, 'm_1', '~/.claude', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO slot_login (slot_id, since_at, seat_id, until_at, source_record) VALUES (1, 5, 'seat_free', 6, 'r1')`)
	if _, ok := ids(false)["seat_free"]; !ok {
		t.Fatal("a seat a slot was logged in to is shown, even after the logout")
	}
}
