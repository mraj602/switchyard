// fakemachine posts hook records to a hub as a machine, for test/e2e/android/run.sh: it puts a
// session into "asked" (needs you), answers it, or ends it, with no Claude Code involved. It
// only talks HTTP to the hub it is given.
//
//	fakemachine -hub http://127.0.0.1:18787 -token sy_m_... -machine m_x -session <uuid> ask|answer|tokens
//
// tokens posts one token_rows record for the session (1M in, 100k out of claude-sonnet-5: $3.00).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

func main() {
	hub := flag.String("hub", "", "hub base URL")
	token := flag.String("token", "", "machine device token")
	machine := flag.String("machine", "", "machine id the token was made for")
	sid := flag.String("session", "c41e0000-0000-4000-8000-0000000000aa", "session id")
	msg := flag.String("message", "Should I also update the migration?", "the question")
	account := flag.String("account", "acc-e2e", "account uuid of the seat (the hub makes one seat_<ULID> per account+org)")
	org := flag.String("org", "org-e2e", "org uuid of the seat")
	email := flag.String("email", "e2e@example.com", "the seat's email")
	remote := flag.String("remote-url", "", "a Remote Control link for the session (https://claude.ai/code/...)")
	flag.Parse()
	if *hub == "" || *token == "" || *machine == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: fakemachine -hub URL -token T -machine ID [-session UUID] [-account A -org O -email E -remote-url U] ask|answer")
		os.Exit(2)
	}
	p := map[string]any{"session_id": *sid, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + *sid + ".jsonl"}
	action := flag.Arg(0)
	switch action {
	case "tokens":
		// no hook payload: a token_rows record, built below
	case "ask":
		p["hook_event_name"] = "Stop"
		p["last_assistant_message"] = "Done with the schema.\n\n" + *msg
	case "answer":
		p["hook_event_name"] = "UserPromptSubmit"
		p["prompt"] = "yes"
	default:
		fmt.Fprintln(os.Stderr, "action must be ask, answer or tokens")
		os.Exit(2)
	}
	raw, _ := json.Marshal(p)
	seat := &record.Seat{Account: *account, Org: *org, Email: *email}
	meta := record.Meta{Machine: *machine, Slot: "~/.claude", Seat: seat, Client: "wy/e2e-android", Now: time.Now()}
	var rec *record.Record
	var err error
	if action == "tokens" {
		rec, err = record.NewTokenRows(meta, record.TokenRowsPayload{Rows: []record.TokenRow{{
			Key: "e2e-" + *sid, TS: time.Now().UTC().Format(time.RFC3339Nano), SessionID: *sid, Cwd: "/home/u/api",
			Model: "claude-sonnet-5", Input: 1_000_000, Output: 100_000}}})
	} else {
		rec, err = record.NewHookEvent(meta, raw, record.HookOptions{LastMessages: true})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *remote != "" {
		if rec.Derived == nil {
			rec.Derived = record.Derived{}
		}
		rec.Derived.Set("remote_url", *remote)
	}
	b, err := rec.Encode()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "wy/e2e-android", "records": []json.RawMessage{b}})
	req, _ := http.NewRequest("POST", *hub+"/v1/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	fmt.Printf("%d %s\n", resp.StatusCode, out)
	if resp.StatusCode != 200 || bytes.Contains(out, []byte(`"rejected":[{`)) {
		os.Exit(1)
	}
}
