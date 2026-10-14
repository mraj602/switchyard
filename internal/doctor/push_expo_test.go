package doctor

import (
	"strings"
	"testing"
	"time"
)

func TestPushExpo(t *testing.T) {
	e := newFakeHome(t, `{}`)
	raw := seedConfigured(t, e, "https://box.ts.net")
	ms := pushNow.UnixMilli()

	// v6 seeds the expo channel on, with no app target and no token.
	if c := pcheck(t, runPush(t, e), "push expo"); c.Level != OK || c.Detail != "on, 0 app target(s), no access token" {
		t.Fatalf("fresh: %+v", c)
	}
	// One live app target (and one on a revoked device), a token.
	mustExec(t, raw, `INSERT INTO push_target (id, kind, device_id, endpoint, meta, created_at, updated_at) VALUES ('pt_e1', 'expo', 'dev_p', 'ExponentPushToken[aaaaaaaaaaaa]', '{"platform":"ios"}', ?, ?)`, ms, ms)
	mustExec(t, raw, `INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at) VALUES ('pt_e2', 'expo', 'dev_r', 'ExponentPushToken[bbbbbbbbbbbb]', ?, ?)`, ms, ms)
	mustExec(t, raw, `INSERT INTO hub_secret (key, value, created_at) VALUES ('expo_access_token', x'00', 1)`)
	c := pcheck(t, runPush(t, e), "push expo")
	if c.Level != OK || c.Detail != "on, 1 app target(s), access token set" {
		t.Fatalf("configured: %+v", c)
	}
	if strings.Contains(c.Detail, "ExponentPushToken") {
		t.Fatal("doctor shows a push token")
	}
	if d := pcheck(t, runPush(t, e), "push targets").Detail; !strings.Contains(d, "expo 1") {
		t.Errorf("targets detail %q", d)
	}

	// A recent receipt error on the app target warns, with a fix.
	mustExec(t, raw, `UPDATE push_target SET last_error = 'InvalidCredentials', last_error_at = ? WHERE id = 'pt_e1'`, ms-(10*time.Minute).Milliseconds())
	if c := pcheck(t, runPush(t, e), "push expo"); c.Level != Warn || !strings.Contains(c.Detail, "InvalidCredentials") || c.Fix == "" {
		t.Fatalf("receipt error: %+v", c)
	}
	mustExec(t, raw, `UPDATE push_target SET last_error_at = ? WHERE id = 'pt_e1'`, ms-(48*time.Hour).Milliseconds())
	if c := pcheck(t, runPush(t, e), "push expo"); c.Level != OK {
		t.Fatalf("old receipt error: %+v", c)
	}

	// A ticket sent 2 h ago with no receipt: the poller is not reading receipts.
	mustExec(t, raw, `INSERT INTO alert (id, episode, trigger, status, created_at, updated_at) VALUES ('al_e', 'ep_e', 'asked', 'fired', ?, ?)`, ms, ms)
	two := ms - (2 * time.Hour).Milliseconds()
	mustExec(t, raw, `INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, ticket, created_at, updated_at) VALUES ('al_e', 'pt_e1', 'expo', 1, 'sent', 'tk-1', ?, ?)`, two, two)
	if c := pcheck(t, runPush(t, e), "push expo"); c.Level != Warn || !strings.Contains(c.Detail, "1 push ticket(s) older than 1 h") {
		t.Fatalf("stale ticket: %+v", c)
	}
	mustExec(t, raw, `UPDATE delivery SET receipt_at = ?`, ms)
	if c := pcheck(t, runPush(t, e), "push expo"); c.Level != OK {
		t.Fatalf("receipt read: %+v", c)
	}

	// The channel off: fine with no app target, a warning with one.
	mustExec(t, raw, `UPDATE channel SET enabled = 0 WHERE kind = 'expo'`)
	if c := pcheck(t, runPush(t, e), "push expo"); c.Level != Warn || !strings.Contains(c.Detail, "off, with 1") {
		t.Fatalf("off: %+v", c)
	}
	mustExec(t, raw, `UPDATE push_target SET gone_at = 1 WHERE kind = 'expo'`)
	if c := pcheck(t, runPush(t, e), "push expo"); c.Level != OK || !strings.HasPrefix(c.Detail, "off") {
		t.Fatalf("off, no target: %+v", c)
	}
}
