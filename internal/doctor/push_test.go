package doctor

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/hub/store"
)

var pushNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// hubDB makes a migrated hub database in the data dir of e and returns a raw handle.
func hubDB(t *testing.T, e fakeEnv) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(e.dirs.Data.Path, "hub.db")
	st, err := store.Open(context.Background(), path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close(); st.Close() })
	return st, raw
}

func runPush(t *testing.T, e fakeEnv) Report {
	t.Helper()
	var rep Report
	checkPush(context.Background(), &rep, e.opts(nil), config.Config{})
	return rep
}

func pcheck(t *testing.T, rep Report, name string) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", name, rep.Checks)
	return Check{}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestPushSilentWithoutHubDB(t *testing.T) {
	e := newFakeHome(t, `{}`)
	if rep := runPush(t, e); len(rep.Checks) != 0 {
		t.Fatalf("checks on a non-hub machine: %+v", rep.Checks)
	}
}

func TestPushEmptyDB(t *testing.T) {
	e := newFakeHome(t, `{}`)
	hubDB(t, e)
	rep := runPush(t, e)
	for _, n := range []string{"push url", "push keys", "push targets"} {
		if c := pcheck(t, rep, n); c.Level != Warn || c.Fix == "" {
			t.Errorf("%s: %+v", n, c)
		}
	}
	if c := pcheck(t, rep, "push delivery"); c.Level != OK {
		t.Errorf("delivery: %+v", c)
	}
}

func seedConfigured(t *testing.T, e fakeEnv, url string) *sql.DB {
	t.Helper()
	st, raw := hubDB(t, e)
	ctx := context.Background()
	if err := st.PutPushSetting(ctx, settingPublicURL, []byte(`"`+url+`"`)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPushSetting(ctx, settingVapidPublic, []byte(`"BPUB"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureHubSecret(ctx, secretVapidPrivate, func() ([]byte, error) { return make([]byte, 32), nil }); err != nil {
		t.Fatal(err)
	}
	ms := pushNow.UnixMilli()
	mustExec(t, raw, `INSERT INTO device (id, kind, name, token_hash, token_hint, scopes, created_at, created_by) VALUES ('dev_p', 'phone', 'p', zeroblob(32), 'h', '["push"]', 1, 'test')`)
	mustExec(t, raw, `INSERT INTO device (id, kind, name, token_hash, token_hint, scopes, created_at, created_by, revoked_at) VALUES ('dev_r', 'phone', 'r', x'0101010101010101010101010101010101010101010101010101010101010101', 'h', '["push"]', 1, 'test', 5)`)
	mustExec(t, raw, `INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at) VALUES ('pt_1', 'webpush', 'dev_p', 'https://push/1', ?, ?)`, ms, ms)
	mustExec(t, raw, `INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at) VALUES ('pt_2', 'ntfy', NULL, 'https://ntfy.sh/x', ?, ?)`, ms, ms)
	// not live: revoked device, gone, disabled
	mustExec(t, raw, `INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at) VALUES ('pt_3', 'webpush', 'dev_r', 'https://push/3', ?, ?)`, ms, ms)
	mustExec(t, raw, `INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at, gone_at) VALUES ('pt_4', 'webpush', 'dev_p', 'https://push/4', ?, ?, 1)`, ms, ms)
	mustExec(t, raw, `INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at, enabled) VALUES ('pt_5', 'webpush', 'dev_p', 'https://push/5', ?, ?, 0)`, ms, ms)
	return raw
}

func TestPushConfigured(t *testing.T) {
	e := newFakeHome(t, `{}`)
	seedConfigured(t, e, "https://box.ts.net")
	rep := runPush(t, e)
	for _, n := range []string{"push url", "push keys", "push targets", "push delivery"} {
		if c := pcheck(t, rep, n); c.Level != OK {
			t.Errorf("%s: %+v", n, c)
		}
	}
	if d := pcheck(t, rep, "push targets").Detail; d != "2 targets (ntfy 1, webpush 1)" {
		t.Errorf("targets detail %q", d)
	}
	if d := pcheck(t, rep, "push url").Detail; d != "https://box.ts.net" {
		t.Errorf("url detail %q", d)
	}
}

func TestPushHTTPURL(t *testing.T) {
	e := newFakeHome(t, `{}`)
	seedConfigured(t, e, "http://box:8787")
	c := pcheck(t, runPush(t, e), "push url")
	if c.Level != Warn || !strings.Contains(c.Detail, "not https") || !strings.Contains(c.Fix, "tailscale serve --bg 8787") {
		t.Fatalf("%+v", c)
	}
}

// The fix names the port the hub service listens on, not a fixed 8787.
func TestPushFixUsesTheHubsListenPort(t *testing.T) {
	e := newFakeHome(t, `{}`)
	seedConfigured(t, e, "http://127.0.0.1:19100")
	o := e.opts(nil)
	o.HubListen = "127.0.0.1:19100"
	var rep Report
	checkPush(context.Background(), &rep, o, config.Config{})
	if c := pcheck(t, rep, "push url"); !strings.Contains(c.Fix, "tailscale serve --bg 19100") {
		t.Fatalf("%+v", c)
	}
}

func TestPushRecentDeliveryFailure(t *testing.T) {
	e := newFakeHome(t, `{}`)
	raw := seedConfigured(t, e, "https://box.ts.net")
	ms := pushNow.UnixMilli()
	two := ms - (2 * time.Hour).Milliseconds()
	mustExec(t, raw, `INSERT INTO alert (id, episode, trigger, status, created_at, updated_at) VALUES ('al_1', 'ep1', 'failed', 'fired', ?, ?)`, ms, ms)
	mustExec(t, raw, `INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, last_error, created_at, updated_at) VALUES ('al_1', 'pt_1', 'webpush', 1, 'failed', ?, ?, ?)`,
		"push service 503 "+strings.Repeat("x", 300), two, two)
	c := pcheck(t, runPush(t, e), "push delivery")
	if c.Level != Warn || !strings.Contains(c.Detail, "2 h ago") || !strings.Contains(c.Detail, "push service 503") || len([]rune(c.Detail)) > 170 {
		t.Fatalf("%+v", c)
	}
	// An old failure no longer warns.
	mustExec(t, raw, `UPDATE delivery SET updated_at = ?`, ms-(48*time.Hour).Milliseconds())
	if c := pcheck(t, runPush(t, e), "push delivery"); c.Level != OK {
		t.Fatalf("%+v", c)
	}
}

func TestPushOldSchema(t *testing.T) {
	e := newFakeHome(t, `{}`)
	_, raw := hubDB(t, e)
	mustExec(t, raw, `DELETE FROM schema_version WHERE version >= 3`)
	c := pcheck(t, runPush(t, e), "push")
	if c.Level != Warn || !strings.Contains(c.Detail, "too old for push") {
		t.Fatalf("%+v", c)
	}
}
