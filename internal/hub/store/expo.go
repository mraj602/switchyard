package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// This file is the store side of the expo channel (push.md 12): its settings and access
// token, the open push tickets and their receipts (12.4), and what the silent widget
// refresh reads (12.9).

// ExpoAccessTokenKey is the hub_secret key of the Expo access token (push.md 12.5).
const ExpoAccessTokenKey = "expo_access_token"

// ExpoConfig is what the expo sender needs from the hub: the channel's base URL ("" means
// the default) and the access token ("" when none is set).
type ExpoConfig struct {
	BaseURL     string
	AccessToken string
}

type expoSettings struct {
	BaseURL string `json:"base_url,omitempty"`
}

// ExpoConfig reads the expo channel's base_url and the stored access token.
func (s *Store) ExpoConfig(ctx context.Context) (ExpoConfig, error) {
	var out ExpoConfig
	c, err := s.ChannelByKind(ctx, model.KindExpo)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return out, err
	}
	var set expoSettings
	if len(c.Settings) > 0 {
		_ = json.Unmarshal(c.Settings, &set)
	}
	out.BaseURL = set.BaseURL
	tok, err := s.HubSecret(ctx, ExpoAccessTokenKey)
	switch {
	case err == nil:
		out.AccessToken = string(tok)
	case !errors.Is(err, ErrNotFound):
		return out, err
	}
	return out, nil
}

// ExpoTokenSet reports whether an Expo access token is stored.
func (s *Store) ExpoTokenSet(ctx context.Context) (bool, error) {
	_, err := s.HubSecret(ctx, ExpoAccessTokenKey)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// expoHostOf is the host (and port) a base URL points at; "" is the default Expo host.
func expoHostOf(base string) string {
	if base == "" {
		base = "https://exp.host"
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	return strings.ToLower(u.Host)
}

// ExpoBaseURL is the base_url stored in an expo channel's settings ("" when none).
func ExpoBaseURL(settings json.RawMessage) string {
	var set expoSettings
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &set)
	}
	return set.BaseURL
}

// SetExpoChannel switches the expo channel and, in the same transaction, changes its
// settings: baseURL nil keeps the stored one and "" removes it (the default applies);
// accessToken nil keeps the stored token, "" deletes it, anything else replaces it. The
// caller validates both. When baseURL changes the host and no accessToken arrives with it,
// the stored token is deleted (it was issued for the old host). The token goes to hub_secret, never into the channel's settings.
func (s *Store) SetExpoChannel(ctx context.Context, enabled bool, baseURL, accessToken *string) (Channel, error) {
	err := s.Write(ctx, func(tx *Tx) error {
		now := tx.NowMS()
		var raw string
		err := tx.queryRow(`SELECT settings FROM channel WHERE kind = ?`, model.KindExpo).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		set := expoSettings{BaseURL: ExpoBaseURL(json.RawMessage(raw))}
		if baseURL != nil {
			// The stored token is Expo's credential: it must not follow a new host. A request
			// that moves the base host without a token of its own drops the stored one.
			if accessToken == nil && expoHostOf(set.BaseURL) != expoHostOf(*baseURL) {
				empty := ""
				accessToken = &empty
			}
			set.BaseURL = *baseURL
		}
		b, _ := json.Marshal(set)
		if _, err := tx.exec(`UPDATE channel SET enabled = ?, settings = ?, updated_at = ? WHERE kind = ?`,
			enabled, string(b), now, model.KindExpo); err != nil {
			return err
		}
		switch {
		case accessToken == nil:
		case *accessToken == "":
			_, err = tx.exec(`DELETE FROM hub_secret WHERE key = ?`, ExpoAccessTokenKey)
		default:
			_, err = tx.exec(`INSERT INTO hub_secret (key, value, created_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, created_at = excluded.created_at`,
				ExpoAccessTokenKey, []byte(*accessToken), now)
		}
		return err
	})
	if err != nil {
		return Channel{}, err
	}
	return s.ChannelByKind(ctx, model.KindExpo)
}

// ---------------------------------------------------------------- receipts (push.md 12.4)

// ExpoTicket is a sent expo delivery whose receipt has not been read.
type ExpoTicket struct {
	DeliveryID int64
	TargetID   string
	Ticket     string
	SentAt     int64 // ms; the delivery's updated_at when it was sent
}

// ExpoOpenTickets lists open tickets (ticket set, no receipt) of deliveries sent at or
// before sentBefore, oldest first, at most limit.
func (s *Store) ExpoOpenTickets(ctx context.Context, sentBefore int64, limit int) ([]ExpoTicket, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id, target_id, ticket, updated_at FROM delivery
		WHERE ticket IS NOT NULL AND receipt_at IS NULL AND updated_at <= ? ORDER BY updated_at, id LIMIT ?`, sentBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExpoTicket
	for rows.Next() {
		var t ExpoTicket
		if err := rows.Scan(&t.DeliveryID, &t.TargetID, &t.Ticket, &t.SentAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReceiptDeviceNotRegistered is the receipt error that makes a target gone.
const ReceiptDeviceNotRegistered = "DeviceNotRegistered"

// ExpoReceipt is one receipt outcome: Error "" is ok (or a receipt Expo no longer has);
// DeviceNotRegistered makes the target gone; any other error is recorded on the target.
type ExpoReceipt struct {
	DeliveryID int64
	TargetID   string
	Error      string // an Expo error code, never message text
}

// MarkReceipts records receipts in one transaction (push.md 12.4): receipt_at = now on
// each delivery; DeviceNotRegistered also sets the delivery's last_error and makes the
// target gone (disabled, gone_at), as a Web Push 410 does; any other error sets the
// delivery's and the target's last_error.
func (s *Store) MarkReceipts(ctx context.Context, rs []ExpoReceipt, now int64) error {
	if len(rs) == 0 {
		return nil
	}
	return s.Write(ctx, func(tx *Tx) error {
		for _, r := range rs {
			if _, err := tx.exec(`UPDATE delivery SET receipt_at = ?, last_error = coalesce(?, last_error) WHERE id = ?`,
				now, nullStr(r.Error), r.DeliveryID); err != nil {
				return err
			}
			var err error
			switch {
			case r.Error == "":
			case r.Error == ReceiptDeviceNotRegistered:
				_, err = tx.exec(`UPDATE push_target SET gone_at = coalesce(gone_at, ?), enabled = 0, last_error = ?, last_error_at = ?,
					updated_at = ? WHERE id = ?`, now, r.Error, now, now, r.TargetID)
			default:
				_, err = tx.exec(`UPDATE push_target SET last_error = ?, last_error_at = ?, updated_at = ? WHERE id = ?`,
					r.Error, now, now, r.TargetID)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------- widget refresh (push.md 12.9)

// ExpoRefreshTargets lists the expo targets that get silent widget refreshes: enabled, not
// gone, channel on, with a device that is neither revoked nor expired and has
// prefs.widgets = true. Prefs is set to the device's prefs.
func (s *Store) ExpoRefreshTargets(ctx context.Context, now int64) ([]model.Target, error) {
	return s.refreshTargets(ctx, model.KindExpo, now)
}

// LinkRefreshTargets is the same for link targets (push.md 13.5).
func (s *Store) LinkRefreshTargets(ctx context.Context, now int64) ([]model.Target, error) {
	return s.refreshTargets(ctx, model.KindLink, now)
}

func (s *Store) refreshTargets(ctx context.Context, kind string, now int64) ([]model.Target, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+targetCols+`, d.prefs FROM push_target t
		JOIN device d ON d.id = t.device_id JOIN channel c ON c.kind = t.kind
		WHERE t.kind = ? AND t.enabled = 1 AND t.gone_at IS NULL AND c.enabled = 1
		AND d.revoked_at IS NULL AND (d.expires_at IS NULL OR d.expires_at > ?)
		ORDER BY t.created_at, t.id`, kind, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Target
	for rows.Next() {
		var dprefs sql.NullString
		t, err := scanTargetWith(rows, &dprefs)
		if err != nil {
			return nil, err
		}
		p := model.Prefs{Tap: model.TapAuto}
		if dprefs.Valid {
			_ = json.Unmarshal([]byte(dprefs.String), &p)
		}
		if !p.WidgetsOn() {
			continue
		}
		t.Prefs = &p
		out = append(out, t)
	}
	return out, rows.Err()
}

// ExpoWindow is one seat's current usage window as the widgets show it.
type ExpoWindow struct {
	Seat     string
	Key      string // five_hour, seven_day
	Pct      float64
	ResetsAt int64 // ms
}

// ExpoWidgetState is what a widget refresh digest is made of (push.md 12.9).
type ExpoWidgetState struct {
	NeedsYou int
	Windows  []ExpoWindow // seat, then window, then resets_at
}

// ExpoWidgetState reads the needs-you count and the current five_hour and seven_day
// windows of visible seats (resets_at after now).
func (s *Store) ExpoWidgetState(ctx context.Context, now int64) (ExpoWidgetState, error) {
	var out ExpoWidgetState
	if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM session_state WHERE needs_you = 1
		AND state NOT IN ('ended', 'gone')`).Scan(&out.NeedsYou); err != nil {
		return out, err
	}
	rows, err := s.r.QueryContext(ctx, `SELECT w.seat_id, w.window_key, coalesce(w.final_pct, w.peak_pct, 0), w.resets_at
		FROM usage_window w JOIN seat se ON se.id = w.seat_id
		WHERE se.hidden = 0 AND w.window_key IN ('five_hour', 'seven_day') AND w.resets_at > ?
		ORDER BY w.seat_id, w.window_key, w.resets_at`, now)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var w ExpoWindow
		if err := rows.Scan(&w.Seat, &w.Key, &w.Pct, &w.ResetsAt); err != nil {
			return out, err
		}
		out.Windows = append(out.Windows, w)
	}
	return out, rows.Err()
}
