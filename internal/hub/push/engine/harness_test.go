package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/events"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/push/clock"
	"github.com/mraj602/switchyard/internal/hub/push/engine"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

// T0 is the start of every scenario (push.md 11.1: "an empty hub at T = 0").
var T0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// sent is one push a fake sender received.
type sent struct {
	At     time.Time
	Target string
	Msg    model.Message
}

// fakeSender records sends and answers scripted results per target (default 201 sent).
type fakeSender struct {
	kind string
	clk  clock.Clock
	mu   sync.Mutex
	got  []sent
	plan map[string][]model.Result
}

func newFake(kind string, clk clock.Clock) *fakeSender {
	return &fakeSender{kind: kind, clk: clk, plan: map[string][]model.Result{}}
}

func (f *fakeSender) Kind() string { return f.kind }

func (f *fakeSender) Send(ctx context.Context, t model.Target, m model.Message) model.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, sent{At: f.clk.Now(), Target: t.ID, Msg: m})
	if q := f.plan[t.ID]; len(q) > 0 {
		f.plan[t.ID] = q[1:]
		return q[0]
	}
	return model.Result{Outcome: model.OutcomeSent, HTTPStatus: 201}
}

func (f *fakeSender) script(target string, rs ...model.Result) {
	f.mu.Lock()
	f.plan[target] = append(f.plan[target], rs...)
	f.mu.Unlock()
}

func (f *fakeSender) sends() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.got...)
}

func (f *fakeSender) to(target string) []sent {
	var out []sent
	for _, s := range f.sends() {
		if s.Target == target {
			out = append(out, s)
		}
	}
	return out
}

type harness struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	clk     *clock.Fake
	eng     *engine.Engine
	ing     *ingest.Ingester
	broker  *events.Broker
	dev     store.Device
	machine string
	wp      *fakeSender
	nf      *fakeSender
	target  string // the default webpush target
	phone   string // its device
	sweep   time.Duration
	slots   map[string]string // seat email -> slot folder
}

type opt func(h *harness)

// withSweep sets a longer silence sweep for scenarios that run for hours.
func withSweep(d time.Duration) opt { return func(h *harness) { h.sweep = d } }

// noTarget leaves the hub without the default webpush target.
func noTarget() opt { return func(h *harness) { h.target = "-" } }

// noStart opens the hub without starting the engine.
func noStart() opt { return func(h *harness) { h.eng = nil } }

func newHarness(t *testing.T, opts ...opt) *harness {
	t.Helper()
	clk := clock.NewFake(T0)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: context.Background(), st: st, clk: clk, sweep: time.Minute, slots: map[string]string{},
		wp: newFake(model.KindWebPush, clk), nf: newFake(model.KindNtfy, clk), broker: events.New(clk.Now)}
	h.eng = &engine.Engine{} // marker: start unless noStart
	for _, o := range opts {
		o(h)
	}
	h.machine = "m_" + record.NewID(T0)
	c, err := auth.Create(h.ctx, st, auth.CreateRequest{Kind: "machine", Name: "gpu-box", Machine: h.machine})
	if err != nil {
		t.Fatal(err)
	}
	h.dev = c.Device
	if h.target == "" {
		h.phone = h.addDevice("iPhone", "")
		h.target = h.addTarget(h.phone, model.KindWebPush, "")
	}
	h.ing = &ingest.Ingester{Store: st, Now: clk.Now, OnChange: func(ch ingest.Changes) {
		if h.eng != nil {
			h.eng.Notify(ch)
		}
	}}
	start := h.eng != nil
	h.eng = nil
	if start {
		h.start()
	}
	t.Cleanup(func() {
		if h.eng != nil {
			h.eng.Stop()
		}
		st.Close()
	})
	return h
}

func (h *harness) start() {
	h.t.Helper()
	h.eng = engine.New(engine.Config{Store: h.st, Clock: h.clk, Senders: []model.Sender{h.wp, h.nf}, Broker: h.broker,
		SweepEvery: h.sweep, PublicURL: "https://hub.example.ts.net"})
	if err := h.eng.Start(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	h.eng.Sync()
}

func (h *harness) stop() {
	h.eng.Stop()
	h.eng = nil
}

// tamperToken returns tok with one character in the middle of its MAC replaced by a different
// one. Rewriting the trailing characters is not reliable: they may already hold the replacement,
// or carry only base64 padding bits that the decoder ignores, which leaves the token valid.
func tamperToken(tok string) string {
	bad := []byte(tok)
	i := len(bad) - 10
	if bad[i] == 'A' {
		bad[i] = 'B'
	} else {
		bad[i] = 'A'
	}
	return string(bad)
}

// at is T0 + d.
func at(d time.Duration) time.Time { return T0.Add(d) }

// advanceTo moves the clock to T0 + d, stepping through every scheduler timer on the way so
// the engine sees each wake-up at its own time.
func (h *harness) advanceTo(d time.Duration) {
	h.t.Helper()
	target := at(d)
	for h.eng != nil {
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
	if h.eng != nil {
		h.eng.Sync()
	}
}

func (h *harness) write(fn func(tx *store.Tx) error) {
	h.t.Helper()
	if err := h.st.Write(h.ctx, fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) exec(q string, args ...any) {
	h.t.Helper()
	h.write(func(tx *store.Tx) error { _, err := tx.ExecContext(tx.Ctx(), q, args...); return err })
}

// addDevice inserts a phone device with prefs (JSON or "").
func (h *harness) addDevice(name, prefs string) string {
	id := store.NewID("dev_", T0)
	sum := sha256.Sum256([]byte(id))
	var p any
	if prefs != "" {
		p = prefs
	}
	h.exec(`INSERT INTO device (id, kind, name, token_hash, token_hint, scopes, created_at, created_by, prefs)
		VALUES (?, 'phone', ?, ?, 'sy_p_…x', '["read","push"]', ?, 'test', ?)`, id, name, sum[:], T0.UnixMilli(), p)
	return id
}

// addTarget inserts a push target for device (or a hub ntfy topic when device is "").
func (h *harness) addTarget(device, kind, prefs string) string {
	id := store.NewID("pt_", T0)
	endpoint := "https://fcm.googleapis.com/fcm/send/" + id
	if kind == model.KindNtfy {
		endpoint = "https://ntfy.example.net/sy-" + id
	}
	var dev, p any
	if device != "" {
		dev = device
	}
	if prefs != "" {
		p = prefs
	}
	h.exec(`INSERT INTO push_target (id, kind, device_id, name, endpoint, keys, prefs, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '{"p256dh":"x","auth":"y"}', ?, 1, ?, ?)`, id, kind, dev, kind+" "+id[len(id)-4:], endpoint, p,
		T0.UnixMilli(), T0.UnixMilli())
	return id
}

func (h *harness) settings(js string) {
	h.write(func(tx *store.Tx) error {
		return tx.PutAlertSetting(store.AlertKeyPushSettings, []byte(js), T0.UnixMilli())
	})
}

// ---------------------------------------------------------------- records

type hookOpt func(r *record.Record, p map[string]any)

func cwd(dir string) hookOpt { return func(_ *record.Record, p map[string]any) { p["cwd"] = dir } }

func remote(u string) hookOpt {
	return func(r *record.Record, _ map[string]any) {
		if r == nil {
			return
		}
		if r.Derived == nil {
			r.Derived = record.Derived{}
		}
		r.Derived.Set("remote_url", u)
	}
}

// seatOf puts the record on a seat (account, org, email) and its own slot.
func (h *harness) seatMeta(email string) (*record.Seat, string) {
	if email == "" {
		email = "client@example.com"
	}
	slot, ok := h.slots[email]
	if !ok {
		slot = fmt.Sprintf("~/.claude-%d", len(h.slots)+1)
		h.slots[email] = slot
	}
	return &record.Seat{Account: "acc-" + email, Org: "org-" + email, Email: email}, slot
}

// hook ingests one hook event of session sid with record ts T0+ts, at the current clock.
func (h *harness) hook(sid, event string, ts time.Duration, fields map[string]any, opts ...hookOpt) {
	h.t.Helper()
	h.hookOn(sid, "", event, ts, fields, opts...)
}

// hookOn is hook for a session on the seat of email.
func (h *harness) hookOn(sid, email, event string, ts time.Duration, fields map[string]any, opts ...hookOpt) {
	h.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + sid + ".jsonl"}
	for k, v := range fields {
		p[k] = v
	}
	seat, slot := h.seatMeta(email)
	// options that edit the payload run first; derived ones need the record
	for _, o := range opts {
		o(nil, p)
	}
	raw, _ := json.Marshal(p)
	r, err := record.NewHookEvent(record.Meta{Machine: h.machine, Slot: slot, Seat: seat, Client: "sy/0.0.0-test", Now: at(ts)},
		raw, record.HookOptions{LastMessages: true})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, o := range opts {
		o(r, map[string]any{})
	}
	h.ingest(r)
}

func (h *harness) ingest(r *record.Record) {
	h.t.Helper()
	b, err := r.Encode()
	if err != nil {
		h.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "sy/0.0.0-test", "records": []json.RawMessage{b}})
	rep, err := h.ing.Ingest(h.ctx, h.dev, body, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if len(rep.Rejected) > 0 {
		h.t.Fatalf("rejected: %+v", rep.Rejected)
	}
	if h.eng != nil {
		h.eng.Sync()
	}
}

// Shorthands for the events of push.md 11.1.
func (h *harness) askQ(sid string, ts time.Duration, opts ...hookOpt) {
	h.hook(sid, "Stop", ts, map[string]any{"last_assistant_message": "Done with the schema.\n\nShould I also update the migration?"}, opts...)
}

func (h *harness) answer(sid string, ts time.Duration) {
	h.hook(sid, "UserPromptSubmit", ts, map[string]any{"prompt": "yes"})
}

func (h *harness) stopDone(sid string, ts time.Duration) {
	h.hook(sid, "Stop", ts, map[string]any{"last_assistant_message": "All done."})
}

func (h *harness) permission(sid string, ts time.Duration) {
	h.hook(sid, "PermissionRequest", ts, map[string]any{"tool_name": "Bash"})
}

func (h *harness) failure(sid, errCode string, ts time.Duration) {
	h.hook(sid, "StopFailure", ts, map[string]any{"error": errCode})
}

// sample ingests a status-line limit sample for the seat of email: five_hour (and
// seven_day when sevenDay >= 0) with the given used % and reset times.
func (h *harness) sample(email string, ts time.Duration, pct float64, resets time.Time, sevenDay float64, resets7 time.Time) {
	h.t.Helper()
	seat, slot := h.seatMeta(email)
	rl := map[string]any{"five_hour": map[string]any{"used_percentage": pct, "resets_at": resets.Unix()}}
	if sevenDay >= 0 {
		rl["seven_day"] = map[string]any{"used_percentage": sevenDay, "resets_at": resets7.Unix()}
	}
	raw, _ := json.Marshal(map[string]any{"session_id": "", "rate_limits": rl})
	r, err := record.NewLimitSample(record.Meta{Machine: h.machine, Slot: slot, Seat: seat, Client: "sy/0.0.0-test", Now: at(ts)}, raw)
	if err != nil {
		h.t.Fatal(err)
	}
	h.ingest(r)
}

// webIdle ingests a claude.ai reading of the seat of email whose 5-hour window has not
// started (0 %, resets_at null), as claude.ai reports an idle account.
func (h *harness) webIdle(email string, ts time.Duration) {
	h.t.Helper()
	seat, _ := h.seatMeta(email)
	seat.Src = "claude_ai"
	r, err := record.NewWebUsage(record.Meta{Machine: h.machine, Seat: seat, Client: "switchyard-desktop/0.0.0-test", Now: at(ts)},
		record.WebUsagePayload{TabID: "t_idle", Usage: json.RawMessage(`{"five_hour":{"utilization":0,"resets_at":null},"seven_day":null,
			"limits":[{"kind":"session","group":"session","percent":0,"resets_at":null,"is_active":true}]}`)})
	if err != nil {
		h.t.Fatal(err)
	}
	h.ingest(r)
}

// ---------------------------------------------------------------- reading

func (h *harness) alerts() []model.Alert {
	h.t.Helper()
	as, _, err := h.st.AlertList(h.ctx, store.AlertFilter{Limit: 200, Reason: ""})
	if err != nil {
		h.t.Fatal(err)
	}
	sort.Slice(as, func(i, j int) bool { return as[i].ID < as[j].ID })
	return as
}

func (h *harness) allAlerts() []model.Alert {
	h.t.Helper()
	var out []model.Alert
	h.write(func(tx *store.Tx) error {
		rows, err := tx.QueryContext(tx.Ctx(), `SELECT id FROM alert ORDER BY id`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			a, err := tx.AlertGet(id)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	return out
}

// alertOf returns the only alert of session sid with trigger t.
func (h *harness) alertOf(sid string, t model.Trigger) model.Alert {
	h.t.Helper()
	var found []model.Alert
	for _, a := range h.allAlerts() {
		if a.Session != nil && *a.Session == sid && a.Trigger == t {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		h.t.Fatalf("alerts of %s %s: %d (%s)", sid, t, len(found), h.dump())
	}
	return found[0]
}

func (h *harness) byTrigger(t model.Trigger) []model.Alert {
	var out []model.Alert
	for _, a := range h.allAlerts() {
		if a.Trigger == t {
			out = append(out, a)
		}
	}
	return out
}

func (h *harness) dump() string {
	var b strings.Builder
	for _, a := range h.allAlerts() {
		fmt.Fprintf(&b, "\n  %s %s %s/%s due=%v fires=%d ep=%s", a.ID, a.Trigger, a.Status, deref(a.Reason), a.DueAt.Std(), a.FireCount, a.Episode)
	}
	return b.String()
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (h *harness) deliveries(alertID string) []model.Delivery {
	h.t.Helper()
	a, err := h.st.AlertByID(h.ctx, alertID)
	if err != nil {
		h.t.Fatal(err)
	}
	return a.Deliveries
}

func (h *harness) expectStatus(a model.Alert, st model.Status, reason string) {
	h.t.Helper()
	got, err := h.st.AlertByID(h.ctx, a.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	if got.Status != st || deref(got.Reason) != reason {
		h.t.Fatalf("alert %s: %s/%s, want %s/%s%s", a.ID, got.Status, deref(got.Reason), st, reason, h.dump())
	}
}

func (h *harness) expectSends(n int) []sent {
	h.t.Helper()
	got := h.wp.sends()
	if len(got) != n {
		var b strings.Builder
		for _, s := range got {
			fmt.Fprintf(&b, "\n  %s %s %q", s.At.Format(time.TimeOnly), s.Target, s.Msg.Payload.Title)
		}
		h.t.Fatalf("%d webpush sends, want %d:%s%s", len(got), n, b.String(), h.dump())
	}
	return got
}
