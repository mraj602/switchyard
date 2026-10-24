package engine_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/pair"
	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// The alert and rule endpoints against a real server, store and engine on the fake clock.

type apiRig struct {
	h            *harness
	srv          *httptest.Server
	owner, phone string
	phoneID      string
}

func newAPIRig(t *testing.T, opts ...opt) *apiRig {
	t.Helper()
	h := newHarness(t, opts...)
	cfg := api.Config{Store: h.st, Now: h.clk.Now, PingEvery: time.Hour}
	if h.eng != nil { // never a typed nil in the interface
		cfg.Engine = h.eng
	}
	s := api.New(cfg)
	r := &apiRig{h: h, srv: httptest.NewServer(s.Handler())}
	t.Cleanup(func() { s.Close(); r.srv.Close() })
	// An owner session's token hash is keyed with the pairing pepper: a temp one here.
	old := api.PairPepperPath
	api.PairPepperPath = filepath.Join(t.TempDir(), "pair_pepper")
	t.Cleanup(func() { api.PairPepperPath = old })
	pepper, err := pair.NewService(h.st, api.PairPepperPath).Pepper()
	if err != nil {
		t.Fatal(err)
	}
	o, err := auth.Create(h.ctx, h.st, auth.CreateRequest{Kind: "browser", Name: "owner", Owner: true, CreatedBy: "owner-link:pc_test", Pepper: pepper})
	if err != nil {
		t.Fatal(err)
	}
	p, err := auth.Create(h.ctx, h.st, auth.CreateRequest{Kind: "phone", Name: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	r.owner, r.phone, r.phoneID = o.Token, p.Token, p.Device.ID
	return r
}

func (r *apiRig) call(method, path, token string, body any) (int, []byte) {
	r.h.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		js, _ := json.Marshal(b)
		rd = bytes.NewReader(js)
	}
	req, _ := http.NewRequest(method, r.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	// Engine actions poke the scheduler; let it settle so the fake clock stays in step.
	if r.h.eng != nil {
		r.h.eng.Sync()
	}
	return resp.StatusCode, out
}

func (r *apiRig) must(method, path, token string, body any, want int, into any) {
	r.h.t.Helper()
	st, b := r.call(method, path, token, body)
	if st != want {
		r.h.t.Fatalf("%s %s = %d, want %d: %s", method, path, st, want, b)
	}
	if into != nil {
		if err := json.Unmarshal(b, into); err != nil {
			r.h.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

type alertJSON struct {
	ID           string  `json:"id"`
	Trigger      string  `json:"trigger"`
	Status       string  `json:"status"`
	Reason       *string `json:"reason"`
	Title        string  `json:"title"`
	Body         *string `json:"body"`
	URL          string  `json:"url"`
	SnoozedUntil *string `json:"snoozed_until"`
	SeenAt       *string `json:"seen_at"`
	FireCount    int     `json:"fire_count"`
	Deliveries   []struct {
		Target string `json:"target"`
		Status string `json:"status"`
	} `json:"deliveries"`
}

func TestAPISessionAlertSeenAndAnswered(t *testing.T) {
	r := newAPIRig(t)
	h := r.h
	h.askQ(s1, 0)
	var sess struct {
		Alert *alertJSON `json:"alert"`
	}
	r.must("GET", "/v1/sessions/"+s1, r.owner, nil, 200, &sess)
	if sess.Alert == nil || sess.Alert.Status != "pending" || sess.Alert.Trigger != "asked" {
		t.Fatalf("pending alert brief: %+v", sess.Alert)
	}
	h.advanceTo(60 * time.Second)
	h.expectSends(1)
	r.must("GET", "/v1/sessions/"+s1, r.owner, nil, 200, &sess)
	if sess.Alert == nil || sess.Alert.Status != "fired" || sess.Alert.FireCount != 1 {
		t.Fatalf("fired alert brief: %+v", sess.Alert)
	}
	id := sess.Alert.ID

	var a alertJSON
	r.must("GET", "/v1/alerts/"+id, r.phone, nil, 200, &a)
	if a.Title != "client · api · asked a question" || a.Body == nil || *a.Body != "Should I also update the migration?" ||
		a.URL != "/#/sessions/"+s1+"?alert="+id || len(a.Deliveries) != 1 || a.Deliveries[0].Status != "sent" {
		t.Fatalf("alert: %+v", a)
	}
	r.must("POST", "/v1/alerts/"+id+"/seen", r.phone, nil, 200, &a)
	if a.Status != "seen" || a.SeenAt == nil {
		t.Fatalf("seen: %+v", a)
	}
	var seenBy string
	if err := h.st.R().QueryRow(`SELECT seen_by FROM alert WHERE id = ?`, id).Scan(&seenBy); err != nil || seenBy != r.phoneID {
		t.Fatalf("seen_by %q (%v), want %q", seenBy, err, r.phoneID)
	}
	// Seen again: idempotent.
	r.must("POST", "/v1/alerts/"+id+"/seen", r.phone, nil, 200, &a)

	h.advanceTo(2 * time.Minute)
	h.answer(s1, 2*time.Minute)
	h.advanceTo(2*time.Minute + time.Second)
	r.must("GET", "/v1/sessions/"+s1, r.owner, nil, 200, &sess)
	if sess.Alert != nil {
		t.Fatalf("answered: alert should be null, got %+v", sess.Alert)
	}
	r.must("GET", "/v1/alerts/"+id, r.owner, nil, 200, &a)
	if a.Status != "closed" && a.Status != "cancelled" {
		t.Fatalf("answered alert status %q", a.Status)
	}
	if st, _ := r.call("POST", "/v1/alerts/"+id+"/snooze", r.phone, nil); st != 409 {
		t.Fatalf("snooze a closed alert = %d, want 409", st)
	}
	if st, _ := r.call("POST", "/v1/alerts/al_nope/seen", r.phone, nil); st != 404 {
		t.Fatalf("seen unknown = %d", st)
	}
	if st, _ := r.call("GET", "/v1/alerts/al_nope", r.phone, nil); st != 404 {
		t.Fatalf("get unknown = %d", st)
	}
}

func TestAPISnoozeAndList(t *testing.T) {
	r := newAPIRig(t)
	h := r.h
	h.askQ(s1, 0)
	h.advanceTo(60 * time.Second)
	h.expectSends(1)
	id := h.alertOf(s1, model.TriggerAsked).ID
	for _, bad := range []string{`{"minutes":3}`, `{"minutes":241}`, `[1]`, `{"minutes":"x"}`} {
		if st, b := r.call("POST", "/v1/alerts/"+id+"/snooze", r.phone, bad); st != 400 {
			t.Fatalf("snooze %s = %d: %s", bad, st, b)
		}
	}
	var a alertJSON
	r.must("POST", "/v1/alerts/"+id+"/snooze", r.phone, `{"minutes":10}`, 200, &a)
	want := at(70 * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	if a.Status != "snoozed" || a.SnoozedUntil == nil || *a.SnoozedUntil != at(60*time.Second+10*time.Minute).Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("snoozed: %+v (now %s)", a, want)
	}
	// Default minutes = snooze_min (15).
	r.must("POST", "/v1/alerts/"+id+"/snooze", r.phone, nil, 200, &a)
	if *a.SnoozedUntil != at(60*time.Second+15*time.Minute).Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("default snooze: %+v", a)
	}

	// A second alert, then the list.
	h.permission(sid(2), 61*time.Second)
	h.advanceTo(2 * time.Minute)
	var page struct {
		Alerts []alertJSON `json:"alerts"`
		Next   *string     `json:"next"`
	}
	r.must("GET", "/v1/alerts", r.phone, nil, 200, &page)
	if len(page.Alerts) != 2 || page.Next != nil || page.Alerts[0].Trigger != "needs_permission" {
		t.Fatalf("list: %+v", page)
	}
	r.must("GET", "/v1/alerts?limit=1", r.phone, nil, 200, &page)
	if len(page.Alerts) != 1 || page.Next == nil {
		t.Fatalf("page 1: %+v", page)
	}
	first := page.Alerts[0].ID
	r.must("GET", "/v1/alerts?limit=1&cursor="+url.QueryEscape(*page.Next), r.phone, nil, 200, &page)
	if len(page.Alerts) != 1 || page.Alerts[0].ID == first || page.Alerts[0].ID != id {
		t.Fatalf("page 2: %+v", page)
	}
	r.must("GET", "/v1/alerts?status=snoozed", r.phone, nil, 200, &page)
	if len(page.Alerts) != 1 || page.Alerts[0].ID != id {
		t.Fatalf("status filter: %+v", page)
	}
	r.must("GET", "/v1/alerts?session="+sid(2), r.phone, nil, 200, &page)
	if len(page.Alerts) != 1 || page.Alerts[0].Trigger != "needs_permission" {
		t.Fatalf("session filter: %+v", page)
	}
	for _, q := range []string{"limit=0", "limit=201", "limit=x", "status=bogus", "cursor=%25%25"} {
		if st, b := r.call("GET", "/v1/alerts?"+q, r.phone, nil); st != 400 {
			t.Fatalf("?%s = %d: %s", q, st, b)
		}
	}
	if st, _ := r.call("GET", "/v1/alerts", "", nil); st != 401 {
		t.Fatalf("no token = %d", st)
	}
}

func TestAPIActionTokens(t *testing.T) {
	r := newAPIRig(t)
	h := r.h
	h.askQ(s1, 0)
	h.advanceTo(60 * time.Second)
	got := h.expectSends(1)
	acts := got[0].Msg.Payload.Actions
	if acts == nil || acts.Snooze == "" || acts.Seen == "" {
		t.Fatalf("no actions: %+v", got[0].Msg.Payload)
	}
	var res struct {
		Alert   alertJSON `json:"alert"`
		Applied bool      `json:"applied"`
	}
	// No bearer token: the action token is the credential.
	r.must("POST", "/v1/actions/"+acts.Snooze, "", `{}`, 200, &res)
	if !res.Applied || res.Alert.Status != "snoozed" {
		t.Fatalf("snooze action: %+v", res)
	}
	// Used again: nothing to do (fire_count moved on? no: status is no longer fired).
	r.must("POST", "/v1/actions/"+acts.Snooze, "", nil, 200, &res)
	if res.Applied {
		t.Fatalf("second snooze applied: %+v", res)
	}
	r.must("POST", "/v1/actions/"+acts.Seen, "", nil, 200, &res)
	if !res.Applied || res.Alert.Status != "seen" {
		t.Fatalf("seen action: %+v", res)
	}
	for tok, want := range map[string]int{
		"garbage":              400,
		tamperToken(acts.Seen): 401,
	} {
		if st, b := r.call("POST", "/v1/actions/"+tok, "", nil); st != want {
			t.Fatalf("token %q = %d, want %d: %s", tok, st, want, b)
		}
	}
	h.advanceTo(25 * time.Hour)
	if st, b := r.call("POST", "/v1/actions/"+acts.Seen, "", nil); st != 410 {
		t.Fatalf("expired = %d: %s", st, b)
	}
	if st, _ := r.call("GET", "/v1/actions/"+acts.Seen, "", nil); st == 200 {
		t.Fatal("GET must not apply an action")
	}
}

func TestAPIRulesAndSettings(t *testing.T) {
	t.Setenv("TZ", "UTC") // the hub machine's zone, the default quiet_hours.tz (push.md 2)
	r := newAPIRig(t)
	var list struct {
		Rules    []model.Rule   `json:"rules"`
		Settings model.Settings `json:"settings"`
	}
	r.must("GET", "/v1/rules", r.phone, nil, 200, &list)
	if len(list.Rules) == 0 || !list.Rules[0].Base || list.Settings.SnoozeMin != 15 || list.Settings.QuietHours.TZ == "" {
		t.Fatalf("rules: %+v", list)
	}
	var askedBase model.Rule
	for _, rl := range list.Rules {
		if rl.Base && rl.Trigger == model.TriggerAsked {
			askedBase = rl
		}
	}
	if askedBase.ID == "" {
		t.Fatal("no base rule for asked")
	}

	// Owner only for writes.
	if st, _ := r.call("POST", "/v1/rules", r.phone, `{"trigger":"asked"}`); st != 403 {
		t.Fatalf("phone POST /v1/rules = %d", st)
	}
	for _, bad := range []string{`{}`, `{"trigger":"nope"}`, `{"trigger":"asked","delay_s":-1}`, `{"trigger":"asked","delay_s":86401}`,
		`{"trigger":"asked","scope":{"seats":["seat_missing"]}}`, `{"trigger":"asked","scope":{"projects":[""]}}`,
		`{"trigger":"asked","colour":"red"}`} {
		if st, b := r.call("POST", "/v1/rules", r.owner, bad); st != 400 {
			t.Fatalf("POST %s = %d: %s", bad, st, b)
		}
	}
	var o model.Rule
	r.must("POST", "/v1/rules", r.owner, `{"trigger":"asked","scope":{"projects":["api"]},"enabled":false}`, 201, &o)
	if o.Base || o.Enabled || o.DelayS != askedBase.DelayS || o.BypassQuiet != askedBase.BypassQuiet || len(o.ID) < 6 || o.ID[:5] != "rule_" {
		t.Fatalf("override: %+v", o)
	}
	r.must("PUT", "/v1/rules/"+o.ID, r.owner, `{"delay_s":5,"scope":{"projects":["api","web"]}}`, 200, &o)
	if o.DelayS != 5 || len(o.Scope.Projects) != 2 || o.Enabled {
		t.Fatalf("put override: %+v", o)
	}
	// Base rules: no trigger change, only windows in scope, never deleted.
	if st, _ := r.call("PUT", "/v1/rules/"+askedBase.ID, r.owner, `{"trigger":"failed"}`); st != 400 {
		t.Fatalf("trigger change = %d", st)
	}
	if st, _ := r.call("PUT", "/v1/rules/"+askedBase.ID, r.owner, `{"scope":{"projects":["api"]}}`); st != 400 {
		t.Fatalf("base scope = %d", st)
	}
	var b model.Rule
	r.must("PUT", "/v1/rules/"+askedBase.ID, r.owner, `{"delay_s":120,"bypass_quiet":true}`, 200, &b)
	if b.DelayS != 120 || !b.BypassQuiet || !b.Base || b.Trigger != model.TriggerAsked {
		t.Fatalf("put base: %+v", b)
	}
	if st, _ := r.call("DELETE", "/v1/rules/"+askedBase.ID, r.owner, nil); st != 409 {
		t.Fatalf("delete base = %d", st)
	}
	if st, _ := r.call("PUT", "/v1/rules/rule_nope", r.owner, `{"enabled":true}`); st != 404 {
		t.Fatalf("put missing = %d", st)
	}
	if st, _ := r.call("DELETE", "/v1/rules/"+o.ID, r.owner, nil); st != 204 {
		t.Fatalf("delete = %d", st)
	}
	if st, _ := r.call("DELETE", "/v1/rules/"+o.ID, r.owner, nil); st != 404 {
		t.Fatalf("delete again = %d", st)
	}

	// The new base delay is used by the engine at once.
	r.h.askQ(s1, 0)
	r.h.advanceTo(61 * time.Second)
	r.h.expectSends(0)
	r.h.advanceTo(120 * time.Second)
	r.h.expectSends(1)

	// Settings.
	for _, bad := range []string{`[]`, `{"snooze_min":1}`, `{"quiet_hours":{"enabled":true,"start":"25:00","end":"08:00"}}`,
		`{"quiet_hours":{"enabled":true,"start":"23:00","end":"08:00","tz":"Nowhere/Land"}}`, `{"flood":{"max":0,"per_s":600}}`} {
		if st, b := r.call("PUT", "/v1/push/settings", r.owner, bad); st != 400 {
			t.Fatalf("settings %s = %d: %s", bad, st, b)
		}
	}
	if st, _ := r.call("PUT", "/v1/push/settings", r.phone, `{}`); st != 403 {
		t.Fatalf("phone settings = %d", st)
	}
	var st model.Settings
	r.must("PUT", "/v1/push/settings", r.owner, `{"snooze_min":30,"quiet_hours":{"enabled":true,"start":"22:00","end":"07:00"}}`, 200, &st)
	if st.SnoozeMin != 30 || st.MaxAgeS != 900 || !st.QuietHours.Enabled || st.QuietHours.TZ != "UTC" {
		t.Fatalf("settings: %+v", st)
	}
	r.must("GET", "/v1/rules", r.owner, nil, 200, &list)
	if list.Settings.SnoozeMin != 30 || list.Settings.QuietHours.Start != "22:00" {
		t.Fatalf("settings after put: %+v", list.Settings)
	}
	var raw string
	if err := r.h.st.R().QueryRow(`SELECT value FROM setting WHERE key = 'push.settings'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(raw), []byte(`"tz"`)) {
		t.Fatalf("stored settings froze the hub tz: %s", raw)
	}
}

func TestAPIWithoutEngine(t *testing.T) {
	r := newAPIRig(t, noStart())
	if st, _ := r.call("POST", "/v1/alerts/al_x/seen", r.phone, nil); st != 503 {
		t.Fatalf("seen without engine = %d", st)
	}
	if st, _ := r.call("POST", "/v1/actions/a1.x.y", "", nil); st != 503 {
		t.Fatalf("action without engine = %d", st)
	}
	var page struct {
		Alerts []alertJSON `json:"alerts"`
	}
	r.must("GET", "/v1/alerts", r.phone, nil, 200, &page)
}
