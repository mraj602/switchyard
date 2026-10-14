package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
)

const expoTok = "ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]"

func execT(t *testing.T, st *store.Store, ctx context.Context, q string, args ...any) {
	t.Helper()
	if err := st.Write(ctx, func(tx *store.Tx) error { _, err := tx.ExecContext(ctx, q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

func TestExpoTargetUpsertWithMeta(t *testing.T) {
	st, ctx := openTargets(t)
	addDevice(t, st, ctx, "dev_1")
	addDevice(t, st, ctx, "dev_22")
	a, err := st.UpsertTarget(ctx, model.Target{Kind: model.KindExpo, DeviceID: "dev_1", Name: "iPhone · app", Endpoint: expoTok,
		Meta: &model.TargetMeta{Platform: "ios", AppVersion: "0.8.0", AppBuild: "12"}})
	if err != nil || a.Kind != "expo" || a.Endpoint != expoTok || a.Meta == nil || a.Meta.Platform != "ios" || a.Meta.AppBuild != "12" ||
		a.Keys != nil || a.Secret != "" {
		t.Fatalf("%+v %v", a, err)
	}
	// Re-registered from another device after it went gone: same row, re-bound, revived, new meta.
	_ = st.ApplyTargetResult(ctx, a.ID, model.Result{Outcome: model.OutcomeGone, Err: "DeviceNotRegistered"})
	b, err := st.UpsertTarget(ctx, model.Target{Kind: model.KindExpo, DeviceID: "dev_22", Endpoint: expoTok,
		Meta: &model.TargetMeta{Platform: "android", AppVersion: "0.8.1"}})
	if err != nil || b.ID != a.ID || b.DeviceID != "dev_22" || b.GoneAt != 0 || !b.Enabled || b.Meta.Platform != "android" ||
		b.Meta.AppBuild != "" || b.Name != "iPhone · app" {
		t.Fatalf("%+v %v", b, err)
	}
	// Other kinds never get meta, and read back without it.
	w, _ := st.UpsertTarget(ctx, model.Target{Kind: model.KindWebPush, DeviceID: "dev_1", Endpoint: "https://fcm.googleapis.com/x",
		Keys: &model.Keys{P256dh: "p", Auth: "a"}, Meta: &model.TargetMeta{Platform: "ios"}})
	if w.Meta != nil {
		t.Fatalf("%+v", w.Meta)
	}
	all, _ := st.ListTargets(ctx, "")
	if len(all) != 2 || all[0].Meta == nil || all[1].Meta != nil {
		t.Fatalf("%+v", all)
	}
}

func TestExpoChannelSettingsAndToken(t *testing.T) {
	st, ctx := openTargets(t)
	cfg, err := st.ExpoConfig(ctx)
	if err != nil || cfg != (store.ExpoConfig{}) {
		t.Fatalf("%+v %v", cfg, err)
	}
	if set, _ := st.ExpoTokenSet(ctx); set {
		t.Fatal("token set on a new hub")
	}
	base, tok := "https://relay.example", "tok_1"
	c, err := st.SetExpoChannel(ctx, false, &base, &tok)
	if err != nil || c.Enabled || store.ExpoBaseURL(c.Settings) != base {
		t.Fatalf("%+v %v", c, err)
	}
	// The token is in hub_secret, never in the channel's settings.
	var raw map[string]any
	_ = json.Unmarshal(c.Settings, &raw)
	if len(raw) != 1 {
		t.Fatalf("%s", c.Settings)
	}
	cfg, _ = st.ExpoConfig(ctx)
	if cfg.BaseURL != base || cfg.AccessToken != tok {
		t.Fatalf("%+v", cfg)
	}
	// nil keeps both; "" removes both.
	if c, _ = st.SetExpoChannel(ctx, true, nil, nil); !c.Enabled || store.ExpoBaseURL(c.Settings) != base {
		t.Fatalf("%+v", c)
	}
	if set, _ := st.ExpoTokenSet(ctx); !set {
		t.Fatal("token dropped")
	}
	tok2 := "tok_2"
	_, _ = st.SetExpoChannel(ctx, true, nil, &tok2)
	if cfg, _ = st.ExpoConfig(ctx); cfg.AccessToken != "tok_2" {
		t.Fatal("token not replaced")
	}
	empty := ""
	c, _ = st.SetExpoChannel(ctx, true, &empty, &empty)
	if string(c.Settings) != "{}" {
		t.Fatalf("%s", c.Settings)
	}
	if cfg, _ = st.ExpoConfig(ctx); cfg != (store.ExpoConfig{}) {
		t.Fatalf("%+v", cfg)
	}
}

// The stored access token is dropped when the base host changes without a token of its own.
func TestExpoTokenClearedOnHostChange(t *testing.T) {
	st, ctx := openTargets(t)
	tok := func() string { c, _ := st.ExpoConfig(ctx); return c.AccessToken }
	set := func(base, token *string) {
		t.Helper()
		if _, err := st.SetExpoChannel(ctx, true, base, token); err != nil {
			t.Fatal(err)
		}
	}
	s := func(v string) *string { return &v }
	set(s("https://a.example"), s("tok_1"))
	set(s("https://a.example/v2"), nil) // same host
	set(nil, nil)
	if tok() != "tok_1" {
		t.Fatal("token dropped without a host change")
	}
	set(s("https://b.example"), nil)
	if tok() != "" {
		t.Fatal("token followed the new host")
	}
	set(s("https://c.example"), s("tok_2")) // a new token with the new host stays
	if tok() != "tok_2" {
		t.Fatal("token sent with the new host was dropped")
	}
	set(s(""), nil) // back to the default host
	if tok() != "" {
		t.Fatal("token followed the move to the default host")
	}
	// From the default host (no base_url stored) to another one.
	set(nil, s("tok_3"))
	set(s("https://d.example"), nil)
	if tok() != "" {
		t.Fatal("token followed the move away from the default host")
	}
}

// addDelivery inserts an alert (once) and a delivery to target with the given ticket.
func addDelivery(t *testing.T, st *store.Store, ctx context.Context, alertID, target, ticket string, fire int, sentAt int64) int64 {
	t.Helper()
	execT(t, st, ctx, `INSERT OR IGNORE INTO alert (id, episode, trigger, status, created_at, updated_at) VALUES (?, ?, 'asked', 'fired', 1, 1)`,
		alertID, "ep-"+alertID)
	var id int64
	err := st.Write(ctx, func(tx *store.Tx) error {
		d := store.DeliveryRow{AlertID: alertID, TargetID: target, TargetKind: "expo", Fire: fire, Status: "sent", Tries: 1,
			CreatedAt: sentAt, UpdatedAt: sentAt}
		if _, err := tx.DeliveryInsert(&d); err != nil {
			return err
		}
		d.Ticket = ticket
		id = d.ID
		return tx.DeliverySave(d)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestExpoTicketsAndReceipts(t *testing.T) {
	st, ctx := openTargets(t)
	addDevice(t, st, ctx, "dev_1")
	a, _ := st.UpsertTarget(ctx, model.Target{Kind: model.KindExpo, DeviceID: "dev_1", Endpoint: expoTok})
	b, _ := st.UpsertTarget(ctx, model.Target{Kind: model.KindExpo, DeviceID: "dev_1", Endpoint: "ExpoPushToken[yyyyyyyyyyyy]"})
	d1 := addDelivery(t, st, ctx, "al_1", a.ID, "tk-1", 1, 1000)
	d2 := addDelivery(t, st, ctx, "al_1", b.ID, "tk-2", 1, 2000)
	d3 := addDelivery(t, st, ctx, "al_1", a.ID, "tk-3", 2, 5000)
	addDelivery(t, st, ctx, "al_2", a.ID, "", 1, 1500) // no ticket (another kind's row, or a failure)

	open, err := st.ExpoOpenTickets(ctx, 3000, 10)
	if err != nil || len(open) != 2 || open[0].DeliveryID != d1 || open[0].Ticket != "tk-1" || open[0].SentAt != 1000 ||
		open[1].DeliveryID != d2 || open[1].TargetID != b.ID {
		t.Fatalf("%+v %v", open, err)
	}
	if lim, _ := st.ExpoOpenTickets(ctx, 9000, 1); len(lim) != 1 {
		t.Fatalf("limit: %+v", lim)
	}
	err = st.MarkReceipts(ctx, []store.ExpoReceipt{
		{DeliveryID: d1, TargetID: a.ID},
		{DeliveryID: d2, TargetID: b.ID, Error: "DeviceNotRegistered"},
		{DeliveryID: d3, TargetID: a.ID, Error: "InvalidCredentials"},
	}, 9000)
	if err != nil {
		t.Fatal(err)
	}
	if open, _ := st.ExpoOpenTickets(ctx, 9999, 10); len(open) != 0 {
		t.Fatalf("still open: %+v", open)
	}
	ga, _ := st.TargetByID(ctx, a.ID)
	gb, _ := st.TargetByID(ctx, b.ID)
	if ga.GoneAt != 0 || !ga.Enabled || ga.LastError != "InvalidCredentials" || ga.LastErrorAt != 9000 {
		t.Fatalf("a %+v", ga)
	}
	if gb.GoneAt != 9000 || gb.Enabled || gb.LastError != "DeviceNotRegistered" {
		t.Fatalf("b %+v", gb)
	}
	var receipt int64
	var lastErr string
	_ = st.R().QueryRow(`SELECT receipt_at, coalesce(last_error, '') FROM delivery WHERE id = ?`, d2).Scan(&receipt, &lastErr)
	if receipt != 9000 || lastErr != "DeviceNotRegistered" {
		t.Fatal(receipt, lastErr)
	}
	_ = st.R().QueryRow(`SELECT receipt_at, coalesce(last_error, '') FROM delivery WHERE id = ?`, d1).Scan(&receipt, &lastErr)
	if receipt != 9000 || lastErr != "" {
		t.Fatal(receipt, lastErr)
	}
}

func TestExpoRefreshTargets(t *testing.T) {
	st, ctx := openTargets(t)
	for _, id := range []string{"dev_1", "dev_2", "dev_3", "dev_4", "dev_5"} {
		addDevice(t, st, ctx, id)
	}
	on := `{"widgets": true, "private": true}`
	execT(t, st, ctx, `UPDATE device SET prefs = ? WHERE id IN ('dev_1', 'dev_3', 'dev_4', 'dev_5')`, on)
	execT(t, st, ctx, `UPDATE device SET prefs = '{"widgets": false}' WHERE id = 'dev_2'`)
	execT(t, st, ctx, `UPDATE device SET revoked_at = 5 WHERE id = 'dev_3'`)
	execT(t, st, ctx, `UPDATE device SET expires_at = 50 WHERE id = 'dev_4'`)
	mk := func(dev, tok string) model.Target {
		tg, err := st.UpsertTarget(ctx, model.Target{Kind: model.KindExpo, DeviceID: dev, Endpoint: "ExpoPushToken[" + tok + "]"})
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}
	t1 := mk("dev_1", "aaaaaaaaaa")
	mk("dev_2", "bbbbbbbbbb") // widgets off
	mk("dev_3", "cccccccccc") // revoked
	mk("dev_4", "dddddddddd") // expired at 50
	t5 := mk("dev_5", "eeeeeeeeee")
	_ = st.ApplyTargetResult(ctx, t5.ID, model.Result{Outcome: model.OutcomeGone})
	// A webpush target of a widgets device is not an expo target.
	_, _ = st.UpsertTarget(ctx, model.Target{Kind: model.KindWebPush, DeviceID: "dev_1", Endpoint: "https://fcm.googleapis.com/y"})

	got, err := st.ExpoRefreshTargets(ctx, 100)
	if err != nil || len(got) != 1 || got[0].ID != t1.ID || got[0].Prefs == nil || !got[0].Prefs.WidgetsOn() {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := st.ExpoRefreshTargets(ctx, 10); len(got) != 2 { // dev_4 not yet expired
		t.Fatalf("%+v", got)
	}
	_, _ = st.SetChannel(ctx, model.KindExpo, false, nil)
	if got, _ := st.ExpoRefreshTargets(ctx, 100); len(got) != 0 {
		t.Fatalf("channel off: %+v", got)
	}
}

func TestExpoWidgetState(t *testing.T) {
	st, ctx := openTargets(t)
	execT(t, st, ctx, `INSERT INTO account (uuid, first_seen, last_seen) VALUES ('acc', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org1', 1, 1), ('org2', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO seat (id, account_uuid, org_uuid, created_at) VALUES ('seat_a', 'acc', 'org1', 1)`)
	execT(t, st, ctx, `INSERT INTO seat (id, account_uuid, org_uuid, created_at, hidden) VALUES ('seat_h', 'acc', 'org2', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO usage_window (seat_id, window_key, resets_at, final_pct, peak_pct, updated_at) VALUES
		('seat_a', 'five_hour', 5000, 62.5, 70, 1), ('seat_a', 'seven_day', 90000, NULL, 41, 1),
		('seat_a', 'five_hour', 500, 99, 99, 1), ('seat_a', 'opus', 5000, 10, 10, 1), ('seat_h', 'five_hour', 5000, 80, 80, 1)`)
	execT(t, st, ctx, `INSERT INTO session (id) VALUES ('s1'), ('s2'), ('s3')`)
	execT(t, st, ctx, `INSERT INTO session_state (session_id, state, since_at, needs_you, updated_at) VALUES
		('s1', 'asked', 1, 1, 1), ('s2', 'working', 1, 0, 1), ('s3', 'gone', 1, 1, 1)`)
	s, err := st.ExpoWidgetState(ctx, 1000)
	if err != nil || s.NeedsYou != 1 || len(s.Windows) != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	if w := s.Windows[0]; w.Seat != "seat_a" || w.Key != "five_hour" || w.Pct != 62.5 || w.ResetsAt != 5000 {
		t.Fatalf("%+v", w)
	}
	if w := s.Windows[1]; w.Key != "seven_day" || w.Pct != 41 {
		t.Fatalf("%+v", w)
	}
}
