package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the driver; the hub database is opened read-only here
)

// Names copied from internal/hub/push/webpush (vapid.go): doctor stays free of the hub's
// push packages and reads the raw rows.
const (
	secretVapidPrivate = "vapid_private"
	settingVapidPublic = "push.vapid_public"
	settingPublicURL   = "hub.public_url"
)

// pushErrorWindow is how far back a delivery failure still counts as a warning.
const pushErrorWindow = 24 * time.Hour

// checkPush adds the phone-push checks when this machine is the hub (its database is
// <data>/hub.db). It opens the file read-only, never migrates and never fails doctor.
func checkPush(ctx context.Context, rep *Report, o Options) {
	if o.Dirs.Data.Path == "" {
		return
	}
	path := filepath.Join(o.Dirs.Data.Path, "hub.db")
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return // not the hub: no push checks
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	db, err := openReadOnly(path)
	if err != nil {
		rep.add(Check{Name: "push", Level: Warn, Detail: "cannot read the hub database: " + err.Error(),
			Fix: "check " + path + " is readable"})
		return
	}
	defer db.Close()

	var ver sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_version`).Scan(&ver); err != nil {
		rep.add(Check{Name: "push", Level: Warn, Detail: "cannot read the hub database: " + err.Error(),
			Fix: "check " + path + " is a Switchyard hub database"})
		return
	}
	if ver.Int64 < 3 {
		rep.add(Check{Name: "push", Level: Warn, Detail: fmt.Sprintf("push: hub database is older than phase 4 (schema v%d)", ver.Int64),
			Fix: "restart the hub to migrate: sy hub serve"})
		return
	}
	if err := pushChecks(ctx, rep, db, o.now()); err != nil {
		rep.add(Check{Name: "push", Level: Warn, Detail: "cannot read the push settings: " + err.Error(),
			Fix: "check " + path + " is readable and not locked"})
	}
	if ver.Int64 >= 6 {
		if err := expoChecks(ctx, rep, db, o.now()); err != nil {
			rep.add(Check{Name: "push expo", Level: Warn, Detail: "cannot read the expo channel: " + err.Error(),
				Fix: "check " + path + " is readable and not locked"})
		}
	}
	if ver.Int64 >= 4 {
		if err := pullChecks(ctx, rep, db, o); err != nil {
			rep.add(Check{Name: "pull", Level: Warn, Detail: "cannot read the pull targets: " + err.Error(),
				Fix: "check " + path + " is readable and not locked"})
		}
	}
}

// Names copied from internal/hub/store (expo.go) and the expo receipts poller (push.md 12.4).
const (
	secretExpoAccessToken = "expo_access_token"
	expoTicketStale       = time.Hour // receipts are read 15 min after a send, every 5 min
)

// expoChecks is the `push expo` line (phases/8.md wave 2): the channel switch, the live app
// targets, whether an Expo access token is set, the newest receipt or send error of an app
// target, and a warning when tickets older than an hour still have no receipt (the receipts
// poller is not running, or Expo cannot be reached).
func expoChecks(ctx context.Context, rep *Report, db *sql.DB, now time.Time) error {
	var enabled sql.NullBool
	if err := db.QueryRowContext(ctx, `SELECT enabled FROM channel WHERE kind = 'expo'`).Scan(&enabled); err != nil && err != sql.ErrNoRows {
		return err
	}
	var tokens, live, stale int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hub_secret WHERE key = ?`, secretExpoAccessToken).Scan(&tokens); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM push_target t JOIN device d ON d.id = t.device_id
WHERE t.kind = 'expo' AND t.enabled = 1 AND t.gone_at IS NULL
  AND d.revoked_at IS NULL AND (d.expires_at IS NULL OR d.expires_at > ?)`, now.UnixMilli()).Scan(&live); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM delivery WHERE ticket IS NOT NULL AND receipt_at IS NULL AND updated_at <= ?`,
		now.Add(-expoTicketStale).UnixMilli()).Scan(&stale); err != nil {
		return err
	}
	var errText sql.NullString
	var errAt sql.NullInt64
	if err := db.QueryRowContext(ctx, `
SELECT last_error, last_error_at FROM push_target
WHERE kind = 'expo' AND last_error IS NOT NULL AND last_error_at IS NOT NULL
ORDER BY last_error_at DESC LIMIT 1`).Scan(&errText, &errAt); err != nil && err != sql.ErrNoRows {
		return err
	}

	if !enabled.Valid || !enabled.Bool {
		if live == 0 {
			rep.add(Check{Name: "push expo", Level: OK, Detail: "off (the phone app gets no pushes)"})
			return nil
		}
		rep.add(Check{Name: "push expo", Level: Warn, Detail: fmt.Sprintf("off, with %d app target(s) waiting", live),
			Fix: "turn the App channel on in the board: Settings, Channels"})
		return nil
	}
	token := "no access token"
	if tokens > 0 {
		token = "access token set"
	}
	detail := fmt.Sprintf("on, %d app target(s), %s", live, token)
	switch {
	case stale > 0:
		rep.add(Check{Name: "push expo", Level: Warn,
			Detail: fmt.Sprintf("%s; %d push ticket(s) older than 1 h have no receipt", detail, stale),
			Fix:    "keep `sy hub serve` running and allow outbound HTTPS to exp.host (deploy/README)"})
	case errAt.Valid && now.Sub(time.UnixMilli(errAt.Int64)) < pushErrorWindow:
		fix := "check the app's push setup (phone/README, push credentials in EAS)"
		if strings.Contains(errText.String, "access token") {
			fix = "set the Expo access token in the board: Settings, Channels, App"
		}
		rep.add(Check{Name: "push expo", Level: Warn,
			Detail: fmt.Sprintf("%s; last error %s ago: %s", detail, roundAge(now.Sub(time.UnixMilli(errAt.Int64))), trimErr(errText.String, 100)),
			Fix:    fix})
	default:
		rep.add(Check{Name: "push expo", Level: OK, Detail: detail})
	}
	return nil
}

func openReadOnly(path string) (*sql.DB, error) {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive paths
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(2000)")
	q.Add("_pragma", "query_only(1)")
	db, err := sql.Open("sqlite", "file:"+p+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func pushChecks(ctx context.Context, rep *Report, db *sql.DB, now time.Time) error {
	// 1. public URL
	var raw sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, settingPublicURL).Scan(&raw); err != nil && err != sql.ErrNoRows {
		return err
	}
	var pub string
	if raw.Valid {
		_ = json.Unmarshal([]byte(raw.String), &pub)
	}
	const urlFix = "run `tailscale serve --bg 8787` and restart with `sy hub serve --public-url https://<name>.ts.net`"
	switch {
	case pub == "":
		rep.add(Check{Name: "push url", Level: Warn, Detail: "phones cannot pair or get pushes without the hub's https address", Fix: urlFix})
	case !strings.HasPrefix(strings.ToLower(pub), "https://"):
		rep.add(Check{Name: "push url", Level: Warn, Detail: pub + " is not https (fine for the board, not for installing the app or pushes)", Fix: urlFix})
	default:
		rep.add(Check{Name: "push url", Level: OK, Detail: pub})
	}

	// 2. VAPID keys
	var n, m int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hub_secret WHERE key = ?`, secretVapidPrivate).Scan(&n); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM setting WHERE key = ?`, settingVapidPublic).Scan(&m); err != nil {
		return err
	}
	if n > 0 && m > 0 {
		rep.add(Check{Name: "push keys", Level: OK, Detail: "VAPID keys present"})
	} else {
		rep.add(Check{Name: "push keys", Level: Warn, Detail: "no VAPID keys yet",
			Fix: "they are made on the first `sy hub serve` start"})
	}

	// 3. live targets: enabled, not gone, and either an ntfy topic of the hub or a
	// device that is neither revoked nor expired.
	rows, err := db.QueryContext(ctx, `
SELECT t.kind, count(*) FROM push_target t LEFT JOIN device d ON d.id = t.device_id
WHERE t.enabled = 1 AND t.gone_at IS NULL
  AND (t.device_id IS NULL OR (d.revoked_at IS NULL AND (d.expires_at IS NULL OR d.expires_at > ?)))
GROUP BY t.kind ORDER BY t.kind`, now.UnixMilli())
	if err != nil {
		return err
	}
	total := 0
	var parts []string
	for rows.Next() {
		var kind string
		var c int
		if err := rows.Scan(&kind, &c); err != nil {
			rows.Close()
			return err
		}
		total += c
		parts = append(parts, fmt.Sprintf("%s %d", kind, c))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if total == 0 {
		rep.add(Check{Name: "push targets", Level: Warn, Detail: "no live target: no phone will get pushes",
			Fix: "open the app on the phone, then Settings, Turn on notifications (pair first with `sy phone pair`)"})
	} else {
		rep.add(Check{Name: "push targets", Level: OK, Detail: fmt.Sprintf("%d targets (%s)", total, strings.Join(parts, ", "))})
	}

	// 4. last delivery error: the newest failed delivery or target error.
	var errText sql.NullString
	var at sql.NullInt64
	err = db.QueryRowContext(ctx, `
SELECT err, at FROM (
  SELECT last_error AS err, updated_at AS at FROM delivery WHERE status IN ('failed', 'gone') AND last_error IS NOT NULL
  UNION ALL
  SELECT last_error, last_error_at FROM push_target WHERE last_error IS NOT NULL AND last_error_at IS NOT NULL
) ORDER BY at DESC LIMIT 1`).Scan(&errText, &at)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && at.Valid {
		age := now.Sub(time.UnixMilli(at.Int64))
		if age < pushErrorWindow {
			rep.add(Check{Name: "push delivery", Level: Warn,
				Detail: fmt.Sprintf("last failure %s ago: %s", roundAge(age), trimErr(errText.String, 120)),
				Fix:    "check the target in the app (Settings, Notifications) or send a test push"})
			return nil
		}
	}
	rep.add(Check{Name: "push delivery", Level: OK, Detail: "no delivery failures in the last 24 hours"})
	return nil
}

func roundAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
}

func trimErr(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
