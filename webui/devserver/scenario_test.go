package devserver

import (
	"fmt"
	"strings"
	"testing"
)

func TestDevScenarios(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	count := func() int {
		return len(owner.want(200, "GET", "/v1/sessions?include_ended=true&days=2&limit=500", nil)["sessions"].([]any))
	}
	before := count()

	owner.want(400, "POST", "/_dev/scenario", M{"names": []string{}})
	if out := owner.want(400, "POST", "/_dev/scenario", M{"names": []string{"nope"}}); !strings.Contains(fmt.Sprint(out["error"]), "history") {
		t.Errorf("unknown scenario error does not list the known ones: %v", out)
	}

	owner.want(200, "POST", "/_dev/scenario", M{"names": []string{"history", "long-names", "no-push", "collide"}})
	if n := count(); n <= before+20 {
		t.Errorf("history added %d sessions", n-before)
	}
	ov := owner.want(200, "GET", "/v1/overview", nil)
	labels := map[string]any{}
	for _, s := range ov["seats"].([]any) {
		m := s.(map[string]any)
		labels[m["id"].(string)] = m["label"]
		if m["id"] == "seat_work" && m["email"] != "mayank.raj@gmail.com" {
			t.Errorf("collide: work email = %v", m["email"])
		}
	}
	// collide runs after long-names: its labels win.
	if labels["seat_personal"] != nil || labels["seat_research"] != "research" {
		t.Errorf("labels = %v", labels)
	}
	if tg := owner.want(200, "GET", "/v1/push/targets", nil)["targets"].([]any); len(tg) != 0 {
		t.Errorf("no-push left %d targets", len(tg))
	}

	owner.want(200, "POST", "/_dev/reset", nil)
	if n := count(); n != before {
		t.Errorf("reset: %d sessions, want %d", n, before)
	}
}
