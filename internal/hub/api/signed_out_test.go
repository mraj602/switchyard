package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/record"
)

// slotLoginRec is a slot_info for `slot`: logged in to `acct` (with orgUUID), or logged out when acct is "".
func slotLoginRec(t *testing.T, machine, slot, acct string, at time.Time) []byte {
	t.Helper()
	p := record.SlotInfoPayload{LoggedIn: acct != ""}
	m := record.Meta{Machine: machine, Slot: slot, Client: "sy/0.6.0", Now: at}
	if acct != "" {
		p.OAuthAccount, _ = json.Marshal(map[string]string{"accountUuid": acct, "organizationUuid": orgUUID})
		m.Seat = &record.Seat{Account: acct, Org: orgUUID, Src: "oauth_account"}
	}
	r, err := record.NewSlotInfo(m, p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

// Seat.signed_out (phone.md P17.6): a folder that logged out of a seat is listed under that
// seat until it logs in again; a folder that moved to another login is not; a read token sees it.
func TestSeatSignedOut(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	phone := h.token("phone")
	const other = "3f1c0000-0000-4000-8000-000000000009"
	type place struct {
		Machine     string  `json:"machine"`
		MachineName string  `json:"machine_name"`
		Slot        *string `json:"slot"`
		Since       *string `json:"since"`
	}
	type seat struct {
		Account   string  `json:"account"`
		Places    []place `json:"places"`
		SignedOut []place `json:"signed_out"`
	}
	seats := func() map[string]seat {
		t.Helper()
		var list struct {
			Accounts []seat `json:"accounts"`
		}
		h.get("/v1/accounts", phone, &list)
		out := map[string]seat{}
		for _, s := range list.Accounts {
			if s.SignedOut == nil {
				t.Fatalf("signed_out missing (want [] when empty): %+v", s)
			}
			out[s.Account] = s
		}
		return out
	}
	slots := func(ps []place) (out []string) {
		for _, p := range ps {
			if p.Slot != nil {
				out = append(out, *p.Slot)
			}
		}
		return out
	}

	h.ingest(machine,
		slotLoginRec(t, h.machine, "~/.claude", acctUUID, t0),
		slotLoginRec(t, h.machine, "~/.claude-2", acctUUID, t0),
		slotLoginRec(t, h.machine, "~/.claude-3", other, t0))
	s := seats()
	if got := slots(s[acctUUID].Places); len(got) != 2 || len(s[acctUUID].SignedOut) != 0 {
		t.Fatalf("both in: %+v", s[acctUUID])
	}

	// ~/.claude logs out; ~/.claude-2 moves to the other login.
	h.ingest(machine,
		slotLoginRec(t, h.machine, "~/.claude", "", t0.Add(time.Minute)),
		slotLoginRec(t, h.machine, "~/.claude-2", other, t0.Add(time.Minute)))
	s = seats()
	a := s[acctUUID]
	if len(slots(a.Places)) != 0 {
		t.Fatalf("places after logout: %+v", a.Places)
	}
	if len(a.SignedOut) != 1 || a.SignedOut[0].Slot == nil || *a.SignedOut[0].Slot != "~/.claude" ||
		a.SignedOut[0].Machine != h.machine || a.SignedOut[0].MachineName == "" || a.SignedOut[0].Since == nil {
		t.Fatalf("signed_out: %+v", a.SignedOut)
	}
	if got := slots(s[other].Places); len(got) != 2 || len(s[other].SignedOut) != 0 {
		t.Fatalf("other seat: %+v", s[other])
	}

	// ~/.claude-2 then logs out of the other login: it is the other seat's, not this one's.
	h.ingest(machine, slotLoginRec(t, h.machine, "~/.claude-2", "", t0.Add(2*time.Minute)))
	s = seats()
	if len(s[acctUUID].SignedOut) != 1 || len(s[other].SignedOut) != 1 || *s[other].SignedOut[0].Slot != "~/.claude-2" {
		t.Fatalf("after second logout: %+v / %+v", s[acctUUID].SignedOut, s[other].SignedOut)
	}

	// Logging back in clears it.
	h.ingest(machine, slotLoginRec(t, h.machine, "~/.claude", acctUUID, t0.Add(3*time.Minute)))
	if s = seats(); len(s[acctUUID].SignedOut) != 0 || len(slots(s[acctUUID].Places)) != 1 {
		t.Fatalf("after login again: %+v", s[acctUUID])
	}
}
