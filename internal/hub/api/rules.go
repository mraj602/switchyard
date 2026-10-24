package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Rules and hub-wide push settings (push.md 2, 3; api.yaml tag alerts). Every change pokes
// the engine so a new delay or a disabled rule takes effect at once.

func init() {
	Register("GET", "/v1/rules", func(s *Server) http.Handler { return http.HandlerFunc(s.ruleList) })
	Register("POST", "/v1/rules", func(s *Server) http.Handler { return http.HandlerFunc(s.ruleAdd) })
	Register("PUT", "/v1/rules/{id}", func(s *Server) http.Handler { return http.HandlerFunc(s.rulePut) })
	Register("DELETE", "/v1/rules/{id}", func(s *Server) http.Handler { return http.HandlerFunc(s.ruleDelete) })
	Register("PUT", "/v1/push/settings", func(s *Server) http.Handler { return http.HandlerFunc(s.pushSettingsPut) })
}

const ruleMaxBody = 64 << 10

// errRuleInput is a 400 with its message.
type errRuleInput struct{ msg string }

func (e errRuleInput) Error() string { return e.msg }

func ruleBad(format string, a ...any) error { return errRuleInput{fmt.Sprintf(format, a...)} }

// errDuplicateRule is a 409: another rule of the trigger already has the same scope
// (push.md 3.4, 3.7). Rule is its id.
type errDuplicateRule struct {
	Rule string
	Base bool
}

func (e errDuplicateRule) Error() string {
	if e.Base {
		return "this override has the same scope as the base rule; change the base rule (" + e.Rule + ") instead"
	}
	return "an override with this trigger and scope already exists (" + e.Rule + "); change that one instead"
}

// ruleDuplicate finds a rule other than self with r's trigger and the same scope.
func ruleDuplicate(tx *store.Tx, r model.Rule) error {
	rs, err := tx.RuleList()
	if err != nil {
		return err
	}
	for _, o := range rs {
		if o.ID != r.ID && o.Trigger == r.Trigger && rules.SameScope(r.Trigger, o.Scope, r.Scope) {
			return errDuplicateRule{Rule: o.ID, Base: o.Base}
		}
	}
	return nil
}

// rulePoke wakes the engine when it has a scheduler.
func (s *Server) rulePoke() {
	if p, ok := s.cfg.Engine.(interface{ Poke() }); ok && s.cfg.Engine != nil {
		p.Poke()
	}
}

// ruleSettings reads push.settings over the defaults; a stored value that no longer
// validates still answers (defaults for what fails to decode).
func (s *Server) ruleSettings(r *http.Request) (model.Settings, error) {
	raw, _, err := s.store.AlertSetting(r.Context(), store.AlertKeyPushSettings)
	if err != nil {
		return model.Settings{}, err
	}
	st, perr := model.ParseSettings(raw, s.alertHubTZ(r))
	if perr != nil {
		st, _ = model.ParseSettings(nil, s.alertHubTZ(r))
	}
	return st, nil
}

func (s *Server) ruleList(w http.ResponseWriter, r *http.Request) {
	rs, err := s.store.RuleList(r.Context())
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	st, err := s.ruleSettings(r)
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	if rs == nil {
		rs = []model.Rule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rs, "settings": st})
}

// ruleRead decodes a RuleInput body; unknown fields are refused.
func ruleRead(w http.ResponseWriter, r *http.Request) (model.RuleInput, bool) {
	var in model.RuleInput
	body, err := io.ReadAll(io.LimitReader(r.Body, ruleMaxBody+1))
	if err != nil || len(body) > ruleMaxBody {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body too large")
		return in, false
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body must be a RuleInput object")
		return in, false
	}
	return in, true
}

// ruleCheckScope validates the parts of a scope that need no database.
func ruleCheckScope(sc model.RuleScope) error {
	for _, p := range sc.Projects {
		if n := len(p); n < 1 || n > 100 {
			return ruleBad("scope.projects entries must be 1-100 characters")
		}
	}
	for _, list := range [][]string{sc.Seats, sc.Machines, sc.Windows} {
		for _, v := range list {
			if v == "" || len(v) > 100 {
				return ruleBad("scope entries must be 1-100 characters")
			}
		}
	}
	return nil
}

func ruleCheckDelay(d *int) error {
	if d != nil && (*d < 0 || *d > 86400) {
		return ruleBad("delay_s must be 0-86400")
	}
	return nil
}

// ruleWriteErr answers a failed rule transaction.
func ruleWriteErr(w http.ResponseWriter, err error) {
	var bad errRuleInput
	var dup errDuplicateRule
	switch {
	case errors.As(err, &bad):
		auth.WriteError(w, http.StatusBadRequest, "bad_request", bad.msg)
	case errors.As(err, &dup):
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": dup.Error(), "code": "duplicate_rule", "rule": dup.Rule})
	case errors.Is(err, store.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such rule")
	case errors.Is(err, model.ErrConflict):
		auth.WriteError(w, http.StatusConflict, "conflict", "base rules can only be disabled")
	default:
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
	}
}

func (s *Server) ruleAdd(w http.ResponseWriter, r *http.Request) {
	in, ok := ruleRead(w, r)
	if !ok {
		return
	}
	if in.Trigger == nil || !in.Trigger.Valid() {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "trigger is required and must be a known trigger")
		return
	}
	if err := ruleCheckDelay(in.DelayS); err != nil {
		ruleWriteErr(w, err)
		return
	}
	var sc model.RuleScope
	if in.Scope != nil {
		sc = *in.Scope
	}
	if err := ruleCheckScope(sc); err != nil {
		ruleWriteErr(w, err)
		return
	}
	now := s.now()
	rule := model.Rule{ID: store.NewID("rule_", now), Trigger: *in.Trigger, Enabled: true, Scope: sc,
		UpdatedAt: model.Time(now.UnixMilli())}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		if n, err := tx.RuleCount(); err != nil {
			return err
		} else if n >= store.AlertMaxRules {
			return ruleBad("at most %d rules", store.AlertMaxRules)
		}
		if err := tx.RuleScopeCheck(sc); err != nil {
			if strings.HasPrefix(err.Error(), "unknown ") {
				return ruleBad("%s", err.Error())
			}
			return err
		}
		// Delay and bypass default to the base rule of the same trigger.
		base, err := ruleBase(tx, *in.Trigger)
		if err != nil {
			return err
		}
		rule.DelayS, rule.BypassQuiet = base.DelayS, base.BypassQuiet
		if in.Enabled != nil {
			rule.Enabled = *in.Enabled
		}
		if in.DelayS != nil {
			rule.DelayS = *in.DelayS
		}
		if in.BypassQuiet != nil {
			rule.BypassQuiet = *in.BypassQuiet
		}
		if err := ruleDuplicate(tx, rule); err != nil {
			return err
		}
		return tx.RulePut(rule)
	})
	if err != nil {
		ruleWriteErr(w, err)
		return
	}
	s.rulePoke()
	writeJSON(w, http.StatusCreated, rule)
}

// ruleBase is the base rule of a trigger (zero Rule when the table has none).
func ruleBase(tx *store.Tx, t model.Trigger) (model.Rule, error) {
	rs, err := tx.RuleList()
	if err != nil {
		return model.Rule{}, err
	}
	for _, r := range rs {
		if r.Base && r.Trigger == t {
			return r, nil
		}
	}
	return model.Rule{}, nil
}

func (s *Server) rulePut(w http.ResponseWriter, r *http.Request) {
	in, ok := ruleRead(w, r)
	if !ok {
		return
	}
	if err := ruleCheckDelay(in.DelayS); err != nil {
		ruleWriteErr(w, err)
		return
	}
	if in.Scope != nil {
		if err := ruleCheckScope(*in.Scope); err != nil {
			ruleWriteErr(w, err)
			return
		}
	}
	var out model.Rule
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		cur, err := tx.RuleGet(r.PathValue("id"))
		if err != nil {
			return err
		}
		if in.Trigger != nil && *in.Trigger != cur.Trigger {
			return ruleBad("trigger cannot change")
		}
		if in.Enabled != nil {
			cur.Enabled = *in.Enabled
		}
		if in.DelayS != nil {
			cur.DelayS = *in.DelayS
		}
		if in.BypassQuiet != nil {
			cur.BypassQuiet = *in.BypassQuiet
		}
		if in.Scope != nil {
			sc := *in.Scope
			if cur.Base {
				if len(sc.Seats)+len(sc.Projects)+len(sc.Machines) > 0 {
					return ruleBad("a base rule's scope may only set windows")
				}
				cur.Scope = model.RuleScope{Windows: sc.Windows}
				if err := ruleDuplicate(tx, cur); err != nil {
					return err
				}
			} else {
				if err := tx.RuleScopeCheck(sc); err != nil {
					if strings.HasPrefix(err.Error(), "unknown ") {
						return ruleBad("%s", err.Error())
					}
					return err
				}
				cur.Scope = sc
				if err := ruleDuplicate(tx, cur); err != nil {
					return err
				}
			}
		}
		cur.UpdatedAt = model.Time(s.now().UnixMilli())
		out = cur
		return tx.RulePut(cur)
	})
	if err != nil {
		ruleWriteErr(w, err)
		return
	}
	s.rulePoke()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ruleDelete(w http.ResponseWriter, r *http.Request) {
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		cur, err := tx.RuleGet(r.PathValue("id"))
		if err != nil {
			return err
		}
		if cur.Base {
			return model.ErrConflict
		}
		return tx.RuleDelete(cur.ID)
	})
	if err != nil {
		ruleWriteErr(w, err)
		return
	}
	s.rulePoke()
	w.WriteHeader(http.StatusNoContent)
}

// pushSettingsPut replaces push.settings (absent keys take their defaults).
func (s *Server) pushSettingsPut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, ruleMaxBody+1))
	if err != nil || len(body) > ruleMaxBody {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body too large")
		return
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(body, &probe) != nil || probe == nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body must be a PushSettings object")
		return
	}
	st, err := model.ParseSettings(body, s.alertHubTZ(r))
	if err == nil {
		err = rules.ValidateQuiet(st.QuietHours)
	}
	if err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// Store without a filled-in tz when none was sent, so the quiet hours keep following
	// the hub's zone.
	stored := st
	var tzProbe struct {
		QuietHours struct {
			TZ string `json:"tz"`
		} `json:"quiet_hours"`
	}
	if json.Unmarshal(body, &tzProbe) == nil && tzProbe.QuietHours.TZ == "" {
		stored.QuietHours.TZ = ""
	}
	enc, err := json.Marshal(stored)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "internal", "could not encode settings")
		return
	}
	at := s.now().UnixMilli()
	if err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		return tx.PutAlertSetting(store.AlertKeyPushSettings, enc, at)
	}); err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	s.rulePoke()
	writeJSON(w, http.StatusOK, st)
}
