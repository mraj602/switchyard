package ntfy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/ntfy"
)

// Real ntfy (push.md 11.3). Opt in with SY_NTFY_DOCKER=1; runs binwiederhier/ntfy under
// podman (preferred) or docker on a loopback port, publishes through the Sender, reads the
// message back with GET /<topic>/json?poll=1 and checks the actions survived. The image
// is pulled on first use. SY_NTFY_RUNTIME overrides the container command.
func TestRealNtfyInContainer(t *testing.T) {
	if os.Getenv("SY_NTFY_DOCKER") != "1" {
		t.Skip("set SY_NTFY_DOCKER=1 to run (needs podman or docker)")
	}
	rt := os.Getenv("SY_NTFY_RUNTIME")
	if rt == "" {
		for _, c := range []string{"podman", "docker"} {
			if p, err := exec.LookPath(c); err == nil {
				rt = p
				break
			}
		}
	}
	if rt == "" {
		t.Fatal("neither podman nor docker found")
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(rt, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", rt, strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	id := run("run", "-d", "--rm", "-p", "127.0.0.1::80", "docker.io/binwiederhier/ntfy", "serve")
	t.Cleanup(func() { _ = exec.Command(rt, "stop", "-t", "1", id).Run() })
	// "127.0.0.1:41234" or "0.0.0.0:41234" for the container's port 80.
	hostPort := run("port", id, "80/tcp")
	hostPort = strings.Split(strings.Fields(hostPort)[0], "\n")[0]
	port := hostPort[strings.LastIndex(hostPort, ":")+1:]
	server := "http://127.0.0.1:" + port

	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(server + "/v1/health")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && strings.Contains(string(b), `"healthy":true`) {
				break
			}
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command(rt, "logs", id).CombinedOutput()
			t.Fatalf("ntfy did not become healthy: %v\n%s", err, logs)
		}
		time.Sleep(250 * time.Millisecond)
	}

	topic, err := ntfy.NewTopic()
	if err != nil {
		t.Fatal(err)
	}
	tg := model.Target{Kind: model.KindNtfy, Endpoint: ntfy.JoinEndpoint(server, topic)}
	res := ntfy.New(ntfy.Options{SnoozeMin: func() int { return 15 }}).Send(context.Background(), tg, askedMsg())
	if res.Outcome != model.OutcomeSent {
		t.Fatalf("real ntfy refused the message: %+v", res)
	}

	resp, err := http.Get(server + "/" + topic + "/json?poll=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ev struct {
		Event    string `json:"event"`
		Title    string `json:"title"`
		Message  string `json:"message"`
		Priority int    `json:"priority"`
		Tags     []string
		Click    string `json:"click"`
		Actions  []struct {
			Action  string            `json:"action"`
			Label   string            `json:"label"`
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
			Clear   bool              `json:"clear"`
		} `json:"actions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.Event != "message" || ev.Title != "work · api · asked a question" || ev.Message != "Should I also update the migration?" ||
		ev.Priority != 4 || len(ev.Tags) != 1 || ev.Tags[0] != "question" || ev.Click != hub+"/#/sessions/c41e?alert=al_01JA3B" {
		t.Fatalf("message came back wrong: %+v", ev)
	}
	if len(ev.Actions) != 3 {
		t.Fatalf("actions: %+v", ev.Actions)
	}
	a := ev.Actions
	if a[0].Action != "view" || a[0].Label != "Open in Claude" || a[0].URL != "https://claude.ai/code/session_x" || !a[0].Clear ||
		a[1].Action != "http" || a[1].Label != "Snooze 15 min" || a[1].Method != "POST" || a[1].URL != hub+"/v1/actions/a1.snz" ||
		a[1].Headers["Content-Type"] != "application/json" || a[1].Body != "{}" ||
		a[2].Label != "Seen" || a[2].URL != hub+"/v1/actions/a1.seen" {
		t.Fatalf("actions did not survive: %+v", a)
	}
}
