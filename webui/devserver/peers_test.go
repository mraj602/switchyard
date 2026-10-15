package devserver

import "testing"

const (
	acctA = "3f1c6a2e-0000-4000-8000-000000000001"
	acctB = "3f1c6a2e-0000-4000-8000-000000000011"
)

// PE3: the matrix, pause and audit endpoints follow api.yaml and the hub's who-may-do-what.
func TestPeersMatrixPauseAudit(t *testing.T) {
	schemas := loadSchemas(t)
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)
	board := newClient(t, ts)
	board.cookie(DefaultToken)

	m := owner.want(200, "GET", "/v1/peers/matrix", nil)
	check(t, schemas, "MatrixInfo", "matrix", m)
	check(t, schemas, "Matrix", "matrix.matrix", m["matrix"])
	if v := m["matrix"].(map[string]any)["version"]; v != float64(1) {
		t.Fatalf("version %v, want 1", v)
	}
	// A read-only board sign-in reads but cannot change anything.
	board.want(200, "GET", "/v1/peers/matrix", nil)
	board.want(403, "PUT", "/v1/peers/matrix", M{"edit": M{"deny": [][]string{{acctA, acctB}}}})

	// Widening needs the owner session; a phone gets 403 and the version stays.
	phone.want(403, "PUT", "/v1/peers/matrix", M{"edit": M{"allow": [][]string{{acctA, acctB}}}})
	phone.want(403, "PUT", "/v1/peers/matrix", M{"edit": M{"same_account": true, "max_turns": 99}})
	m = owner.want(200, "PUT", "/v1/peers/matrix", M{"edit": M{"base_version": 1, "allow": [][]string{{acctA, acctB}, {acctB, acctA}}}})
	check(t, schemas, "MatrixInfo", "matrix", m)
	if v := m["matrix"].(map[string]any)["version"]; v != float64(2) {
		t.Fatalf("version %v, want 2", v)
	}
	if m["widens"] != true {
		t.Fatalf("an added pair must widen: %v", m)
	}
	// A stale base version is a 409.
	owner.want(409, "PUT", "/v1/peers/matrix", M{"edit": M{"base_version": 1, "deny": [][]string{{acctA, acctB}}}})

	// Narrowing is open to a phone.
	m = phone.want(200, "PUT", "/v1/peers/matrix", M{"edit": M{"base_version": 2, "deny": [][]string{{acctB, acctA}}}})
	if m["widens"] != false || len(m["matrix"].(map[string]any)["allow"].([]any)) != 1 {
		t.Fatalf("deny one direction leaves one pair: %v", m)
	}

	// The kill switch: any device pauses, only an owner resumes.
	m = phone.want(200, "POST", "/v1/peers/pause", M{"paused": true})
	if m["matrix"].(map[string]any)["paused"] != true {
		t.Fatalf("not paused: %v", m)
	}
	phone.want(403, "POST", "/v1/peers/pause", M{"paused": false})
	owner.want(200, "POST", "/v1/peers/pause", M{"paused": false})

	a := owner.want(200, "GET", "/v1/peers/audit", nil)
	var events []string
	each(t, a["entries"], func(e any) {
		check(t, schemas, "PeerAuditEntry", "audit", e)
		events = append(events, e.(map[string]any)["event"].(string))
	})
	want := []string{"resume", "pause", "matrix", "matrix", "loop_guard", "matrix"} // newest first
	if len(events) != len(want) {
		t.Fatalf("audit events %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("audit events %v, want %v", events, want)
		}
	}
	board.want(200, "GET", "/v1/peers/audit", nil)
}

// "Resume thread" (peers.md 6.3): owner only, 404 for an unknown thread, an audit row.
func TestPeersThreadResume(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)
	board := newClient(t, ts)
	board.cookie(DefaultToken)
	path := "/v1/peers/threads/" + mockThread + "/resume"

	phone.want(403, "POST", path, nil)
	board.want(403, "POST", path, nil)
	owner.want(404, "POST", "/v1/peers/threads/t_ffffffffffffffff/resume", nil)
	m := owner.want(200, "POST", path, nil)
	if m["thread"] != mockThread || m["was_paused"] != true || m["machines"] != float64(2) {
		t.Fatalf("resume: %v", m)
	}
	a := owner.want(200, "GET", "/v1/peers/audit", nil)
	entries := a["entries"].([]any)
	first, second := entries[0].(map[string]any), entries[1].(map[string]any)
	if first["event"] != "thread_resume_refused" || first["thread"] != mockThread || second["event"] != "thread_resume" ||
		second["thread"] != mockThread {
		t.Fatalf("newest audit entries %v, %v", first, second)
	}
	// A machine kept its stop, so the thread is paused again.
	if m := owner.want(200, "POST", path, nil); m["was_paused"] != true {
		t.Fatalf("second resume: %v", m)
	}
}
