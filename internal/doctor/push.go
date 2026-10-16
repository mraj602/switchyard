package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/service"
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
// <data>/hub.db and hub.url is empty or names this machine). When hub.url names another
// machine, a hub.db here is a stale copy: doctor checks the remote hub's /health instead and
// leaves the database alone. It opens the file read-only, never migrates and never fails
// doctor.
func checkPush(ctx context.Context, rep *Report, o Options, cfg config.Config) {
	if strings.TrimSpace(cfg.Hub.URL) != "" && !hubIsLocal(cfg, hubListen(o)) {
		checkRemoteHub(ctx, rep, o, cfg)
		return
	}
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
	if err := pushChecks(ctx, rep, db, o.now(), hubListen(o)); err != nil {
		rep.add(Check{Name: "push", Level: Warn, Detail: "cannot read the push settings: " + err.Error(),
			Fix: "check " + path + " is readable and not locked"})
	}
	if ver.Int64 >= 4 {
		if err := pullChecks(ctx, rep, db, o); err != nil {
			rep.add(Check{Name: "pull", Level: Warn, Detail: "cannot read the pull targets: " + err.Error(),
				Fix: "check " + path + " is readable and not locked"})
		}
	}
}

// hubListen is the installed hub service's --listen, else the default.
func hubListen(o Options) string {
	if o.HubListen != "" {
		return o.HubListen
	}
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return service.InstalledListen(service.Env{GOOS: runtime.GOOS, Home: o.Home, Getenv: getenv})
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

// pushChecks: listen is the address the hub on this machine listens on (its service unit's
// --listen), for the `tailscale serve` fix.
func pushChecks(ctx context.Context, rep *Report, db *sql.DB, now time.Time, listen string) error {
	// 1. public URL
	var raw sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, settingPublicURL).Scan(&raw); err != nil && err != sql.ErrNoRows {
		return err
	}
	var pub string
	if raw.Valid {
		_ = json.Unmarshal([]byte(raw.String), &pub)
	}
	urlFix := service.HTTPSFix(listen)
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
