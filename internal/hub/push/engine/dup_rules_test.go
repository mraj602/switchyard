package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// An override with the scope of another rule of its trigger (the base rule
// included) is refused with 409 duplicate_rule and the id to change (push.md 3.7).
func TestAPIDuplicateRules(t *testing.T) {
	r := newAPIRig(t)
	var api, limit model.Rule
	r.must("POST", "/v1/rules", r.owner, `{"trigger":"asked","scope":{"projects":["api","web"]}}`, 201, &api)
	r.must("POST", "/v1/rules", r.owner, `{"trigger":"limit_80","scope":{"windows":["seven_day_opus"]}}`, 201, &limit)
	var other model.Rule
	r.must("POST", "/v1/rules", r.owner, `{"trigger":"asked","scope":{"projects":["infra"]}}`, 201, &other)

	for _, tc := range []struct {
		name, method, path, body string
		want                     int
		rule                     string // the id named by a 409
	}{
		{"same scope, other order", "POST", "/v1/rules", `{"trigger":"asked","scope":{"projects":["web","api"]},"delay_s":9}`, 409, api.ID},
		{"everything is the base rule", "POST", "/v1/rules", `{"trigger":"asked","delay_s":300}`, 409, "rule_asked"},
		{"empty lists are everything", "POST", "/v1/rules", `{"trigger":"failed","scope":{"seats":[],"projects":[]}}`, 409, "rule_failed"},
		{"default windows are the base rule", "POST", "/v1/rules", `{"trigger":"limit_95","scope":{"windows":["five_hour","seven_day"]}}`, 409, "rule_limit_95"},
		{"same windows", "POST", "/v1/rules", `{"trigger":"limit_80","scope":{"windows":["seven_day_opus"]}}`, 409, limit.ID},
		{"other trigger, same scope", "POST", "/v1/rules", `{"trigger":"failed","scope":{"projects":["api","web"]}}`, 201, ""},
		{"narrower scope", "POST", "/v1/rules", `{"trigger":"asked","scope":{"projects":["api"]}}`, 201, ""},
		{"put onto another's scope", "PUT", "/v1/rules/" + other.ID, `{"scope":{"projects":["api","web"]}}`, 409, api.ID},
		{"put own scope again", "PUT", "/v1/rules/" + api.ID, `{"delay_s":30,"scope":{"projects":["web","api"]}}`, 200, ""},
		{"put an override onto every account", "PUT", "/v1/rules/" + other.ID, `{"enabled":false,"scope":{}}`, 409, "rule_asked"},
		{"put without scope", "PUT", "/v1/rules/" + other.ID, `{"delay_s":30}`, 200, ""},
		{"base windows onto an override's", "PUT", "/v1/rules/rule_limit_80", `{"scope":{"windows":["seven_day_opus"]}}`, 409, limit.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, b := r.call(tc.method, tc.path, r.owner, tc.body)
			if st != tc.want {
				t.Fatalf("%s %s = %d: %s", tc.method, tc.body, st, b)
			}
			if st != 409 {
				return
			}
			var e struct{ Error, Code, Rule string }
			if err := json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			if e.Code != "duplicate_rule" || e.Rule != tc.rule || !strings.Contains(e.Error, tc.rule) {
				t.Fatalf("%+v", e)
			}
		})
	}
	// The refused writes changed nothing.
	var list struct {
		Rules []model.Rule `json:"rules"`
	}
	r.must("GET", "/v1/rules", r.owner, nil, 200, &list)
	for _, ru := range list.Rules {
		if ru.ID == other.ID && (len(ru.Scope.Projects) != 1 || ru.Scope.Projects[0] != "infra") {
			t.Errorf("refused PUT changed the rule: %+v", ru)
		}
		if ru.ID == "rule_limit_80" && strings.Contains(strings.Join(ru.Scope.Windows, ","), "opus") {
			t.Errorf("refused base PUT changed it: %+v", ru)
		}
	}
}
