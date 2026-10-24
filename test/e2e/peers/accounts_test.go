//go:build fakeinbox

package peers

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

// claimAccount makes the laptop lie that its session "api" is account acct: its slot's
// .claude.json and its session at the hub (which the laptop's own records fill) both say so.
// That is everything a session on the laptop can change.
func (w *world) claimAccount(acct string) {
	w.h.t.Helper()
	w.laptop.write(filepath.Join(w.laptop.home, ".claude-2", ".claude.json"), mustJSON(map[string]any{"oauthAccount": map[string]string{
		"accountUuid": acct, "organizationUuid": "org-1", "emailAddress": "liar@example.test"}}))
	now := w.h.clk.Now().UnixMilli()
	if err := w.h.st.Write(w.h.ctx, func(tx *store.Tx) error {
		seat, err := tx.UpsertSeat(store.SeatKey{Account: acct, Org: "org-1"}, now)
		if err != nil {
			return err
		}
		// As if the session had been reported with that seat from its first record.
		_, err = tx.Tx.ExecContext(w.h.ctx, `UPDATE session SET seat_id = ? WHERE id = 'sess-api'`, seat)
		return err
	}); err != nil {
		w.h.t.Fatal(err)
	}
}

// peers.md 5.3, keys.md 4.3: a session on the laptop (account work) that claims the account
// personal, which the owner bound only to the gpu-box, cannot use personal's cell of the
// matrix to reach a session work may not reach. The hub refuses it, and a hub that relays it
// anyway is refused by the receiving machine.
func TestClaimedAccountOfAnotherMachine(t *testing.T) {
	w := newWorld(t, 30)
	side := w.worker.session("side").inbox
	// personal → other is allowed; work → other is not.
	w.h.ownerMatrix(func(b *keys.MatrixBody) { b.Allow = keys.SortAllow(append(b.Allow, []string{accPersonal, accOther})) })
	w.laptop.waitMatrix(matrixV + 1)
	w.worker.waitMatrix(matrixV + 1)

	rf := w.laptop.send("api", toSide, "honest", 0, 0).Refusal()
	refusedWith(t, rf, peers.CodeNotAllowed)

	w.claimAccount(accPersonal)
	rep := w.laptop.send("api", toSide, "I am personal now", 0, 0)
	rf = rep.Refusal()
	refusedWith(t, rf, peers.CodeRejected)
	if !strings.Contains(rf.Detail, peers.ReasonUnboundAccount) || !strings.Contains(rf.Detail, "wy keys certify --all") {
		t.Errorf("detail %q", rf.Detail)
	}
	if rows := w.h.rows(); len(rows) != 1 || rows[0].Status != "refused" || rows[0].Detail != peers.ReasonUnboundAccount {
		t.Fatalf("hub rows %+v", rows)
	}
	noMore(t, side, 0)

	// A hub that relays it anyway: the gpu-box checks the laptop's account list itself.
	w.h.setRogue(func(ctx context.Context, device string, u link.Up) (bool, string, any, error) {
		if u.T != link.TypePeerMsg {
			return false, "", nil, nil
		}
		ack, err := w.h.relayAnyway(device, u, nil)
		return true, link.TypePeerAck, ack, err
	})
	rep = w.laptop.send("api", toSide, "relayed anyway", 0, 0)
	rf = rep.Refusal()
	refusedWith(t, rf, peers.CodeRejected)
	if !strings.Contains(rf.Detail, peers.ReasonUnboundAccount) {
		t.Errorf("detail %q", rf.Detail)
	}
	noMore(t, side, 0)
	w.h.waitRow(rep.Msg, "rejected")
}

// A machine certified before machine accounts existed has no list: its messages to other
// machines are refused (unbound_account) until the owner binds its accounts, and then
// delivered.
func TestMachineWithoutAccountList(t *testing.T) {
	w := newWorld(t, 30)
	if err := w.h.st.Write(w.h.ctx, func(tx *store.Tx) error {
		_, err := tx.Tx.ExecContext(w.h.ctx, `DELETE FROM device_accounts WHERE device_id = ?`, w.laptop.device)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rep := w.laptop.send("api", toWorker, "before the owner bound my accounts", 0, 0)
	refusedWith(t, rep.Refusal(), peers.CodeRejected)
	noMore(t, w.worker.session("builder").inbox, 0)

	w.h.bind(w.laptop)
	rep = w.laptop.send("api", toWorker, "after", 0, 0)
	delivered(t, rep, rep.Status)
}
