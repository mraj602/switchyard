package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// connect must send real slot_info records, so a token bound to another machine is caught
// (an empty batch cannot show that), and store nothing when it is.
func TestConnectProbeCatchesTokenForAnotherMachine(t *testing.T) {
	deps, runner, home := sandbox(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var kinds, machines []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"ok":true,"version":"0.2.0","schema":1}`)
		case "/v1/ingest":
			var b struct {
				Records []struct {
					Kind    string `json:"kind"`
					Machine string `json:"machine"`
					Slot    string `json:"slot"`
				} `json:"records"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			for _, rec := range b.Records {
				kinds = append(kinds, rec.Kind+"@"+rec.Slot)
				machines = append(machines, rec.Machine)
			}
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":"this token is bound to another machine","code":"machine_mismatch"}`)
		}
	}))
	defer srv.Close()

	var out, errb bytes.Buffer
	err := connect(context.Background(), deps, []string{srv.URL, "--token", goodToken, "--insecure-lan"}, &out, &errb)
	if len(machines) == 0 {
		t.Fatalf("no record reached the hub: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "another machine") || !strings.Contains(err.Error(), machines[0]) {
		t.Fatalf("err = %v (machines %v)", err, machines)
	}
	if len(kinds) != 1 || kinds[0] != "slot_info@~/.claude" {
		t.Fatalf("probe sent %v, want one slot_info for ~/.claude", kinds)
	}
	if _, err := os.Stat(filepath.Join(deps.dirs.State.Path, "token")); err == nil {
		t.Error("a mismatched token must not be stored")
	}
	if len(runner.calls) != 0 {
		t.Errorf("timer touched: %v", runner.calls)
	}
}
