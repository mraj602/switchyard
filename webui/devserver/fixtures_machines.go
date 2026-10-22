package devserver

import "time"

// Machine fixtures (docs/spec/machines.md 6.1): every reach mode, a quiet pulled
// machine with its silent alert, a newer machine holding records because the hub is too old,
// a removed machine, and (StateMachines) one pull machine per pull state.

// pullStates are the states of api.yaml PullStatus.
var pullStates = []string{"new", "ok", "failing", "host_key", "auth", "refused", "disabled", "frozen"}

func (d *Data) machineFixtures(now time.Time) {
	ago := func(x time.Duration) string { return ts(now.Add(-x)) }
	in := func(x time.Duration) string { return ts(now.Add(x)) }
	min, hour := time.Minute, time.Hour

	base := func(m M) {
		for k, v := range map[string]any{
			"reach": nil, "backlog_oldest": nil, "backlog_at": nil, "version_status": "unknown", "record_v": 1, "held": 0,
			"direct_push": false, "timer_ok": true, "linger": nil, "pull": nil, "alert": nil,
		} {
			if _, ok := m[k]; !ok {
				m[k] = v
			}
		}
	}
	set := func(id string, kv M) {
		for _, m := range d.Machines {
			if m["id"] == id {
				for k, v := range kv {
					m[k] = v
				}
				base(m)
			}
		}
	}
	set("m_laptop", M{"sy_version": "0.5.0", "version_status": "same", "reach": "direct", "linger": nil})
	set("m_gpu", M{"sy_version": "0.4.2", "version_status": "older", "reach": "forward", "backlog": 1240,
		"backlog_oldest": ago(3 * hour), "backlog_at": ago(30 * time.Second), "direct_push": true, "linger": true})
	set("m_vps", M{"sy_version": "0.4.0", "version_status": "older", "reach": "pull", "backlog_oldest": ago(3 * hour), "backlog_at": ago(3 * hour),
		"timer_ok": false, "linger": false,
		"pull": pull("old-vps", "me@old-vps", "failing", ago(1*min), ago(3*hour), "ssh: connect to host old-vps port 22: Connection timed out", in(1*min)),
		"alert": M{"id": "al_silent_vps", "trigger": "machine_silent", "status": "fired", "reason": nil, "due_at": ago(165 * min),
			"fired_at": ago(165 * min), "fire_count": 1, "snoozed_until": nil, "seen_at": nil}})

	oci := M{"id": "m_cloud", "name": "cloud-box", "os": "Ubuntu 24.04", "arch": "arm64", "sy_version": "0.6.0", "version_status": "newer",
		"last_seen": ago(40 * time.Second), "last_record": ago(40 * time.Second), "backlog": 37, "backlog_oldest": ago(20 * min), "backlog_at": ago(40 * time.Second),
		"silent": false, "live_sessions": 0, "reach": "pull", "record_v": 2, "held": 37, "direct_push": false, "timer_ok": true, "linger": true,
		"pull":  pull("cloud-box", "admin@cloud-box", "ok", ago(40*time.Second), ago(40*time.Second), nil, in(20*time.Second)),
		"alert": nil, "slots": []M{}}
	d.Machines = append(d.Machines, oci)

	removed := M{"id": "m_scratch", "name": "scratch-vm", "os": "Debian 12", "arch": "amd64", "sy_version": "0.3.1", "version_status": "older",
		"last_seen": ago(48 * hour), "last_record": ago(48 * hour), "backlog": nil, "silent": false, "live_sessions": 0, "reach": "forward",
		"record_v": 1, "held": 0, "direct_push": false, "timer_ok": nil, "linger": nil, "pull": nil, "alert": nil, "slots": []M{},
		"removed_at": ago(24 * hour)}
	d.Machines = append(d.Machines, removed)

	d.StateMachines = nil
	for _, st := range pullStates {
		var lastErr any
		switch st {
		case "failing":
			lastErr = "ssh: connect to host pull-failing port 22: No route to host"
		case "host_key":
			lastErr = "host key changed: SHA256:Zq9H…; run sy hub pull trust pull-host_key"
		case "auth":
			lastErr = "permission denied (publickey)"
		case "refused":
			lastErr = "the remote refused: its sy is older than the hub's drain protocol"
		}
		name := "pull-" + st
		last := ago(2 * min)
		if st == "new" {
			last = ""
		}
		var lastTry, lastOK any
		if last != "" {
			lastTry = last
			if st == "ok" || st == "frozen" || st == "disabled" {
				lastOK = last
			}
		}
		p := M{"name": name, "target": "me@" + name, "interval_s": 60, "state": st, "last_try": lastTry, "last_ok": lastOK, "last_error": lastErr,
			"next_at": in(30 * time.Second)}
		if st == "disabled" || st == "frozen" {
			p["next_at"] = nil
		}
		m := M{"id": "m_" + name, "name": name, "os": "Debian 12", "arch": "amd64", "sy_version": "0.5.0", "version_status": "same",
			"last_seen": ago(2 * min), "last_record": ago(2 * min), "backlog": 0, "silent": false, "live_sessions": 0, "reach": "pull",
			"record_v": 1, "held": 0, "direct_push": false, "timer_ok": true, "linger": true, "pull": p, "alert": nil, "slots": []M{}}
		d.StateMachines = append(d.StateMachines, m)
	}
}

func pull(name, target, state, lastTry, lastOK string, lastErr any, next string) M {
	return M{"name": name, "target": target, "interval_s": 60, "state": state, "last_try": lastTry, "last_ok": lastOK, "last_error": lastErr, "next_at": next}
}

// LiveMachines are the machines that are not removed (what /v1/overview lists).
func (d *Data) LiveMachines() []M {
	out := make([]M, 0, len(d.Machines))
	for _, m := range d.Machines {
		if m["removed_at"] == nil {
			out = append(out, m)
		}
	}
	return out
}
