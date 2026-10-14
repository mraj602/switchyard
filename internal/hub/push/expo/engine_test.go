package expo_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/events"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/push/clock"
	"github.com/mraj602/switchyard/internal/hub/push/engine"
	"github.com/mraj602/switchyard/internal/hub/push/expo"
	"github.com/mraj602/switchyard/internal/hub/push/expotest"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

// Engine scenarios 1, 19, 22 and 36 of push.md 11.1 with an Expo target, sent through the
// real expo sender to the fake Expo service (phases/8.md WP-H).

var eT0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

type engRig struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	clk     *clock.Fake
	eng     *engine.Engine
	ing     *ingest.Ingester
	dev     store.Device
	machine string
	f       *expotest.Server
	target  string
	phone   string
}

func newEngRig(t *testing.T) *engRig {
	t.Helper()
	clk := clock.NewFake(eT0)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	h := &engRig{t: t, ctx: context.Background(), st: st, clk: clk, f: expotest.New()}
	h.machine = "m_" + record.NewID(eT0)
	c, err := auth.Create(h.ctx, st, auth.CreateRequest{Kind: "machine", Name: "gpu-box", Machine: h.machine})
	if err != nil {
		t.Fatal(err)
	}
	h.dev = c.Device
	p, err := auth.Create(h.ctx, st, auth.CreateRequest{Kind: "phone", Name: "iPhone · app"})
	if err != nil {
		t.Fatal(err)
	}
	h.phone = p.Device.ID
	tg, err := st.UpsertTarget(h.ctx, model.Target{Kind: model.KindExpo, DeviceID: h.phone, Name: "iPhone · app",
		Endpoint: "ExponentPushToken[engineengine01]", Meta: &model.TargetMeta{Platform: "ios", AppVersion: "0.8.0"}})
	if err != nil {
		t.Fatal(err)
	}
	h.target = tg.ID
	sender := expo.New(expo.Options{BaseURL: h.f.URL(), Client: h.f.Client(), Now: clk.Now})
	h.eng = engine.New(engine.Config{Store: st, Clock: clk, Senders: []model.Sender{sender}, Broker: events.New(clk.Now),
		SweepEvery: time.Hour, PublicURL: "https://hub.example.ts.net"})
	h.ing = &ingest.Ingester{Store: st, Now: clk.Now, OnChange: func(ch ingest.Changes) { h.eng.Notify(ch) }}
	if err := h.eng.Start(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.eng.Sync()
	t.Cleanup(func() { h.eng.Stop(); h.f.Close(); st.Close() })
	return h
}

func (h *engRig) advanceTo(d time.Duration) {
	h.t.Helper()
	target := eT0.Add(d)
	for {
		next, ok := h.clk.NextTimer()
		if !ok || h.clk.Now().Add(next).After(target) {
			break
		}
		h.clk.Advance(next)
		h.eng.Sync()
	}
	if rest := target.Sub(h.clk.Now()); rest > 0 {
		h.clk.Advance(rest)
	}
	h.eng.Sync()
}

func (h *engRig) hook(sid, event string, ts time.Duration, fields map[string]any, opts ...func(*record.Record)) {
	h.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + sid + ".jsonl"}
	for k, v := range fields {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	seat := &record.Seat{Account: "acc-client", Org: "org-client", Email: "client@example.com"}
	r, err := record.NewHookEvent(record.Meta{Machine: h.machine, Slot: "~/.claude-1", Seat: seat, Client: "sy/0.0.0-test", Now: eT0.Add(ts)},
		raw, record.HookOptions{LastMessages: true})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, o := range opts {
		o(r)
	}
	b, err := r.Encode()
	if err != nil {
		h.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "sy/0.0.0-test", "records": []json.RawMessage{b}})
	rep, err := h.ing.Ingest(h.ctx, h.dev, body, nil)
	if err != nil || len(rep.Rejected) > 0 {
		h.t.Fatalf("%v %+v", err, rep.Rejected)
	}
	h.eng.Sync()
}

func (h *engRig) askQ(sid string, ts time.Duration) {
	h.hook(sid, "Stop", ts, map[string]any{"last_assistant_message": "Done with the schema.\n\nShould I also update the migration?"})
}

func (h *engRig) alertOf(sid string, tr model.Trigger) model.Alert {
	h.t.Helper()
	as, _, err := h.st.AlertList(h.ctx, store.AlertFilter{Limit: 100})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, a := range as {
		if a.Session != nil && *a.Session == sid && a.Trigger == tr {
			return a
		}
	}
	h.t.Fatalf("no %s alert for %s", tr, sid)
	return model.Alert{}
}

// sends returns the fake's requests, failing on any the fake found invalid.
func (h *engRig) sends(n int) []expotest.Received {
	h.t.Helper()
	got := h.f.Requests()
	if len(got) != n {
		h.t.Fatalf("%d Expo messages, want %d", len(got), n)
	}
	for _, g := range got {
		if g.Err != nil {
			h.t.Fatalf("invalid message: %v\n%s", g.Err, g.Raw)
		}
	}
	return got
}

func TestEngineRow01WithExpo(t *testing.T) {
	h := newEngRig(t)
	h.advanceTo(500 * time.Millisecond)
	h.askQ("s1", 0)
	h.advanceTo(59 * time.Second)
	h.sends(0)
	h.advanceTo(60 * time.Second)
	m := h.sends(1)[0].Msg
	a := h.alertOf("s1", model.TriggerAsked)
	if m.To != "ExponentPushToken[engineengine01]" || *m.Title != "client · api · asked a question" ||
		*m.Body != "Should I also update the migration?" || *m.CategoryID != "sy_alert" || *m.ChannelID != "sy_needs_you" ||
		m.Priority != "high" || *m.TTL != 21600 || *m.Badge != 1 || m.Data["alert"] != a.ID || m.Data["type"] != "alert" {
		t.Fatalf("%+v %v", m, m.Data)
	}
	if a.Status != model.StatusFired {
		t.Fatal(a.Status)
	}
	// The ticket is stored on the delivery for the receipts poller.
	var ticket string
	_ = h.st.R().QueryRow(`SELECT coalesce(ticket, '') FROM delivery WHERE alert_id = ?`, a.ID).Scan(&ticket)
	if ticket == "" || ticket != h.f.Requests()[0].Ticket {
		t.Fatalf("ticket %q", ticket)
	}
}

func TestEngineRow19SnoozeWithExpo(t *testing.T) {
	h := newEngRig(t)
	h.askQ("s1", 0)
	h.advanceTo(70 * time.Second)
	a := h.alertOf("s1", model.TriggerAsked)
	if _, err := h.eng.Snooze(h.ctx, a.ID, 0, h.phone); err != nil {
		t.Fatal(err)
	}
	h.eng.Sync()
	h.advanceTo(16*time.Minute + 9*time.Second)
	h.sends(1)
	h.advanceTo(16*time.Minute + 10*time.Second)
	got := h.sends(2)
	if !strings.HasPrefix(*got[1].Msg.Title, "Still waiting · 16 min · ") || got[1].Msg.Data["title"] != *got[1].Msg.Title {
		t.Fatalf("%q", *got[1].Msg.Title)
	}
	b, _ := h.st.AlertByID(h.ctx, a.ID)
	if b.FireCount != 2 || b.Status != model.StatusFired {
		t.Fatalf("%+v", b.AlertBrief)
	}
}

func TestEngineRow22ActionTokenReplayWithExpo(t *testing.T) {
	h := newEngRig(t)
	h.askQ("s1", 0)
	h.advanceTo(60 * time.Second)
	acts := h.sends(1)[0].Msg.Data["actions"].(map[string]any)
	snooze, seen := acts["snooze"].(string), acts["seen"].(string)
	h.advanceTo(2 * time.Minute)
	b, applied, err := h.eng.Act(h.ctx, snooze)
	if err != nil || !applied || b.Status != model.StatusSnoozed {
		t.Fatal(b, applied, err)
	}
	if _, applied, err = h.eng.Act(h.ctx, snooze); err != nil || applied {
		t.Fatal("replay applied", err)
	}
	if _, applied, err = h.eng.Act(h.ctx, seen); err != nil || !applied {
		t.Fatal(applied, err)
	}
	if _, applied, _ = h.eng.Act(h.ctx, seen); applied {
		t.Fatal("seen applied twice")
	}
}

func TestEngineRow36GoneTargetWithExpo(t *testing.T) {
	h := newEngRig(t)
	h.f.Script(expotest.Reply{TicketError: "DeviceNotRegistered"})
	h.hook("s1", "StopFailure", 0, map[string]any{"error": "rate_limit"})
	a := h.alertOf("s1", model.TriggerFailed)
	h.sends(1)
	b, _ := h.st.AlertByID(h.ctx, a.ID)
	if len(b.Deliveries) != 1 || b.Deliveries[0].Status != "gone" || b.Deliveries[0].Kind != "expo" {
		t.Fatalf("%+v", b.Deliveries)
	}
	tg, _ := h.st.TargetByID(h.ctx, h.target)
	if tg.Enabled || tg.GoneAt == 0 {
		t.Fatalf("%+v", tg)
	}
	h.hook("s2", "StopFailure", 0, map[string]any{"error": "rate_limit"})
	h.sends(1)
	if c, _ := h.st.AlertByID(h.ctx, h.alertOf("s2", model.TriggerFailed).ID); len(c.Deliveries) != 0 {
		t.Fatalf("%+v", c.Deliveries)
	}
}

// TestEnginePrivateExpo: a private device's push carries no remote_url (push.md 12.2),
// while the alert keeps it.
func TestEnginePrivateExpo(t *testing.T) {
	h := newEngRig(t)
	if err := h.st.UpdateDevice(h.ctx, h.phone, nil, ptr(`{"private": true}`)); err != nil {
		t.Fatal(err)
	}
	remote := func(r *record.Record) {
		if r.Derived == nil {
			r.Derived = record.Derived{}
		}
		r.Derived.Set("remote_url", "https://claude.ai/code/session_x")
	}
	h.hook("s1", "Stop", 0, map[string]any{"last_assistant_message": "Should I?"}, remote)
	h.advanceTo(2 * time.Minute)
	a := h.alertOf("s1", model.TriggerAsked)
	if a.Facts.RemoteURL == "" {
		full, _ := h.st.AlertByID(h.ctx, a.ID)
		t.Fatalf("the scenario has no remote_url to drop: %+v", full)
	}
	m := h.sends(1)[0].Msg
	if *m.Title != "Switchyard" || m.Data["private"] != true || m.Data["remote_url"] != nil ||
		!strings.HasPrefix(m.Data["url"].(string), "/#/alerts/") {
		t.Fatalf("%+v %v", m, m.Data)
	}
}

func ptr(s string) *string { return &s }
