package desktop

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The C4 examples of desktop.md decode into the wave 0 types and encode back to the same
// JSON: every key, no extra ones, nulls kept.
func TestC4ShapesRoundTrip(t *testing.T) {
	for name, c := range map[string]struct {
		v   any
		doc string
	}{
		"info": {new(Info), `{"v": 1, "sy": "0.6.0", "machine": {"id": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", "name": "laptop"},
			"hub": {"url": "http://127.0.0.1:8787", "connected": true, "reachable": true, "version": "0.6.0",
			        "local": true, "dial": null, "service": "running"},
			"admin": "local", "admin_error": null, "outbox": {"pending": 3, "last_error": null}}`},
		"info not connected": {new(Info), `{"v": 1, "sy": "0.6.0", "machine": {"id": "m_x", "name": "laptop"},
			"hub": {"url": null, "connected": false, "reachable": false, "version": null,
			        "local": false, "dial": null, "service": null},
			"admin": null, "admin_error": null, "outbox": {"pending": 0, "last_error": "hub unreachable"}}`},
		"token": {new(Token), `{"device": "dev_01JA", "token": "sy_d_abc", "scopes": ["write", "read"],
			"hub": {"url": "https://hub.example.ts.net"}}`},
		"owner-link": {new(OwnerLink), `{"url": "https://hub.example.ts.net/#/owner?s=abc", "expires_at": "2026-10-13T10:00:00.000Z"}`},
		"hub":        {new(HubService), `{"service": "missing", "unit": "switchyard-hub.service", "health": false}`},
	} {
		dec := json.NewDecoder(bytes.NewReader([]byte(c.doc)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c.v); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		_ = json.Unmarshal([]byte(c.doc), &a)
		_ = json.Unmarshal(got, &b)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if !bytes.Equal(ja, jb) {
			t.Errorf("%s: round trip\n got  %s\n want %s", name, jb, ja)
		}
	}
}

func TestExitCodes(t *testing.T) {
	if ExitOK != 0 || ExitUsage != 2 || ExitRefused != 3 || ExitNotHere != 4 || ExitUnreachable != 5 {
		t.Fatal("exit codes differ from desktop.md C4")
	}
}
