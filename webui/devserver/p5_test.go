package devserver

import (
	"strings"
	"testing"
	"time"
)

func TestPhase5MachineFixturesMatchAPISpec(t *testing.T) {
	schemas := loadSchemas(t)
	d := Fixtures(time.Date(2026, 10, 12, 14, 3, 22, 0, time.UTC))
	reach, status := map[string]bool{}, map[string]bool{}
	all := append(append([]M{}, d.Machines...), d.StateMachines...)
	for _, m := range roundTrip(t, all).([]any) {
		check(t, schemas, "Machine", "machine", m)
		mm := m.(map[string]any)
		if r, ok := mm["reach"].(string); ok {
			reach[r] = true
		}
		if p, ok := mm["pull"].(map[string]any); ok {
			check(t, schemas, "PullStatus", "pull", p)
			status[p["state"].(string)] = true
		}
		if a, ok := mm["alert"].(map[string]any); ok {
			check(t, schemas, "AlertBrief", "alert", a)
		}
	}
	for _, r := range []string{"direct", "forward", "pull"} {
		if !reach[r] {
			t.Errorf("no fixture with reach %s", r)
		}
	}
	for _, st := range pullStates {
		if !status[st] {
			t.Errorf("no fixture in pull state %s", st)
		}
	}
	if n := len(d.LiveMachines()); n != len(d.Machines)-1 {
		t.Errorf("live machines %d of %d: the removed one must be left out", n, len(d.Machines))
	}
}

func TestMachineOwnerEndpoints(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	board := newClient(t, ts)
	board.cookie(DefaultToken)

	count := func(c *client, path string) int {
		return len(c.want(200, "GET", path, nil)["machines"].([]any))
	}
	live, all := count(owner, "/v1/machines"), count(owner, "/v1/machines?all=true")
	if all != live+1 {
		t.Fatalf("all=true: %d, live %d", all, live)
	}

	// owner only
	board.want(403, "PATCH", "/v1/machines/m_gpu", M{"name": "x"})
	board.want(403, "DELETE", "/v1/machines/m_gpu", nil)
	board.want(403, "POST", "/v1/machines/m_vps/pull", nil)

	// rename
	owner.want(400, "PATCH", "/v1/machines/m_gpu", M{"name": ""})
	owner.want(400, "PATCH", "/v1/machines/m_gpu", M{"name": strings.Repeat("x", 61)})
	owner.want(404, "PATCH", "/v1/machines/m_nope", M{"name": "x"})
	if m := owner.want(200, "PATCH", "/v1/machines/m_gpu", M{"name": "gpu-2"}); m["name"] != "gpu-2" {
		t.Errorf("rename = %v", m)
	}

	// pull now
	owner.want(404, "POST", "/v1/machines/m_gpu/pull", nil) // forward: no pull target
	if p := owner.want(202, "POST", "/v1/machines/m_vps/pull", nil); p["state"] != "failing" {
		t.Errorf("pull = %v", p)
	}
	owner.want(200, "POST", "/_dev/pull-states", nil)
	owner.want(409, "POST", "/v1/machines/m_pull-host_key/pull", nil)
	owner.want(409, "POST", "/v1/machines/m_pull-refused/pull", nil)
	owner.want(202, "POST", "/v1/machines/m_pull-ok/pull", nil)

	// remove: gone from the default lists, still there with all=true
	owner.want(404, "DELETE", "/v1/machines/m_nope", nil)
	owner.want(204, "DELETE", "/v1/machines/m_vps", nil)
	if n := count(owner, "/v1/machines"); n != live+len(pullStates)-1 {
		t.Errorf("after remove: %d", n)
	}
	ov := owner.want(200, "GET", "/v1/overview", nil)
	for _, m := range ov["machines"].([]any) {
		if m.(map[string]any)["id"] == "m_vps" || m.(map[string]any)["removed_at"] != nil {
			t.Error("overview lists a removed machine")
		}
	}
}
