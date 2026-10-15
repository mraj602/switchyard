// fakemachine posts hook records to a hub as a machine, for test/e2e/android/run.sh: it puts a
// session into "asked" (needs you), answers it, or ends it, with no Claude Code involved. It
// only talks HTTP to the hub it is given.
//
//	fakemachine -hub http://127.0.0.1:18787 -token sy_m_... -machine m_x -session <uuid> ask|answer
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

	"github.com/mraj602/switchyard/internal/record"
)

func main() {
	hub := flag.String("hub", "", "hub base URL")
	token := flag.String("token", "", "machine device token")
	machine := flag.String("machine", "", "machine id the token was made for")
	sid := flag.String("session", "c41e0000-0000-4000-8000-0000000000aa", "session id")
	msg := flag.String("message", "Should I also update the migration?", "the question")
	flag.Parse()
	if *hub == "" || *token == "" || *machine == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: fakemachine -hub URL -token T -machine ID [-session UUID] ask|answer")
		os.Exit(2)
	}
	p := map[string]any{"session_id": *sid, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + *sid + ".jsonl"}
	switch flag.Arg(0) {
	case "ask":
		p["hook_event_name"] = "Stop"
		p["last_assistant_message"] = "Done with the schema.\n\n" + *msg
	case "answer":
		p["hook_event_name"] = "UserPromptSubmit"
		p["prompt"] = "yes"
	default:
		fmt.Fprintln(os.Stderr, "action must be ask or answer")
		os.Exit(2)
	}
	raw, _ := json.Marshal(p)
	seat := &record.Seat{Account: "acc-e2e", Org: "org-e2e", Email: "e2e@example.com"}
	rec, err := record.NewHookEvent(record.Meta{Machine: *machine, Slot: "~/.claude", Seat: seat, Client: "sy/e2e-android", Now: time.Now()},
		raw, record.HookOptions{LastMessages: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	b, err := rec.Encode()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "sy/e2e-android", "records": []json.RawMessage{b}})
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
