package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// Rules, hub settings and hub secrets for the alert engine (push.md 2, 3.4, 3.7, 8).
// Names carry Rule/Alert prefixes so they stay apart from the rest of the package.

// Setting keys of push.md 2.
const (
	AlertKeyPushSettings  = "push.settings"
	AlertKeyBaselineDone  = "push.baseline_done"
	AlertKeyHubTZ         = "hub.tz"
	AlertKeyPublicURL     = "hub.public_url"
	AlertSecretActionKey  = "action_key"
	AlertMaxRules         = 200
	alertRuleCols         = `id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at`
	alertSettingSelectSQL = `SELECT value FROM setting WHERE key = ?`
)

type alertQuerier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func alertScanRule(sc interface{ Scan(...any) error }) (model.Rule, error) {
	var r model.Rule
	var base, enabled, bypass int
	var scope string
	var updated int64
	if err := sc.Scan(&r.ID, &r.Trigger, &base, &enabled, &r.DelayS, &bypass, &scope, &updated); err != nil {
		return r, err
	}
	r.Base, r.Enabled, r.BypassQuiet, r.UpdatedAt = base == 1, enabled == 1, bypass == 1, model.Time(updated)
	_ = json.Unmarshal([]byte(scope), &r.Scope)
	return r, nil
}

func alertTriggerOrder(t model.Trigger) int {
	for i, k := range model.Triggers {
		if k == t {
			return i
		}
	}
	return len(model.Triggers)
}

func alertRuleList(ctx context.Context, q alertQuerier) ([]model.Rule, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+alertRuleCols+` FROM rule`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Rule{}
	for rows.Next() {
		r, err := alertScanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	// Base rules first in trigger order, then overrides by trigger, oldest first.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Base != b.Base {
			return a.Base
		}
		if oa, ob := alertTriggerOrder(a.Trigger), alertTriggerOrder(b.Trigger); oa != ob {
			return oa < ob
		}
		if a.UpdatedAt != b.UpdatedAt {
			return a.UpdatedAt < b.UpdatedAt
		}
		return a.ID < b.ID
	})
	return out, rows.Err()
}

// RuleList returns every rule (GET /v1/rules order: base rules, then overrides).
func (s *Store) RuleList(ctx context.Context) ([]model.Rule, error) { return alertRuleList(ctx, s.r) }

// RuleList is RuleList inside a transaction.
func (tx *Tx) RuleList() ([]model.Rule, error) { return alertRuleList(tx.ctx, tx) }

// RuleGet returns one rule (ErrNotFound).
func (tx *Tx) RuleGet(id string) (model.Rule, error) {
	r, err := alertScanRule(tx.queryRow(`SELECT `+alertRuleCols+` FROM rule WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func alertB2I(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RulePut inserts or replaces a rule.
func (tx *Tx) RulePut(r model.Rule) error {
	scope, err := json.Marshal(r.Scope)
	if err != nil {
		return err
	}
	_, err = tx.exec(`INSERT INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET enabled = excluded.enabled, delay_s = excluded.delay_s, bypass_quiet = excluded.bypass_quiet,
		scope = excluded.scope, updated_at = excluded.updated_at`,
		r.ID, string(r.Trigger), alertB2I(r.Base), alertB2I(r.Enabled), r.DelayS, alertB2I(r.BypassQuiet), string(scope), int64(r.UpdatedAt))
	return err
}

// RuleDelete deletes an override (ErrNotFound when there is no such override).
func (tx *Tx) RuleDelete(id string) error {
	res, err := tx.exec(`DELETE FROM rule WHERE id = ? AND base = 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RuleCount counts every rule.
func (tx *Tx) RuleCount() (int, error) {
	var n int
	err := tx.queryRow(`SELECT count(*) FROM rule`).Scan(&n)
	return n, err
}

// RuleScopeCheck reports the first seat or machine id of a scope that does not exist.
func (tx *Tx) RuleScopeCheck(sc model.RuleScope) error {
	for _, id := range sc.Seats {
		var one int
		if err := tx.queryRow(`SELECT 1 FROM seat WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("unknown seat %q", id)
		} else if err != nil {
			return err
		}
	}
	for _, id := range sc.Machines {
		var one int
		if err := tx.queryRow(`SELECT 1 FROM machine WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("unknown machine %q", id)
		} else if err != nil {
			return err
		}
	}
	return nil
}

func alertSettingGet(ctx context.Context, q alertQuerier, key string) ([]byte, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, alertSettingSelectSQL, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(v), true, nil
}

// AlertSetting reads one setting's JSON value (ok false when unset).
func (s *Store) AlertSetting(ctx context.Context, key string) ([]byte, bool, error) {
	return alertSettingGet(ctx, s.r, key)
}

// AlertSetting is AlertSetting inside a transaction.
func (tx *Tx) AlertSetting(key string) ([]byte, bool, error) { return alertSettingGet(tx.ctx, tx, key) }

// PutAlertSetting stores one setting (value must be JSON).
func (tx *Tx) PutAlertSetting(key string, value []byte, at int64) error {
	_, err := tx.exec(`INSERT INTO setting (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(value), at)
	return err
}

// AlertSettingString reads a setting holding a JSON string ("" when unset or not a string).
func (s *Store) AlertSettingString(ctx context.Context, key string) string {
	raw, ok, err := s.AlertSetting(ctx, key)
	if err != nil || !ok {
		return ""
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	return v
}

// ActionKey returns the hub's action-token key, making it on first use (push.md 8).
func (tx *Tx) ActionKey() ([]byte, error) {
	var key []byte
	err := tx.queryRow(`SELECT value FROM hub_secret WHERE key = ?`, AlertSecretActionKey).Scan(&key)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if _, err := tx.exec(`INSERT INTO hub_secret (key, value, created_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, AlertSecretActionKey, key, tx.NowMS()); err != nil {
		return nil, err
	}
	return key, nil
}
