package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/model"
)

func TestSetupCodeFlagValidation(t *testing.T) {
	s := newSetupHome(t, detecttest.Laptop(t))
	for _, args := range [][]string{
		{"setup", "--dry-run", "--code", "482-913"},
		{"setup", "--dry-run", "--hub", "here", "--code", "482-913"},
		{"setup", "--dry-run", "--hub", "later", "--code", "482-913"},
		{"setup", "--dry-run", "--hub", "join:http://127.0.0.1:1", "--code", "482-913", "--token-stdin"},
	} {
		out, errOut, code := s.sy(t, "", args...)
		if code != 2 {
			t.Errorf("sy %v: exit %d, want 2\n%s%s", args, code, out, errOut)
		}
		if strings.Contains(out+errOut, "482-913") {
			t.Errorf("sy %v echoed the code", args)
		}
	}
}

func TestSetupHubJoinWithCode(t *testing.T) {
	s := newSetupHome(t, detecttest.Laptop(t))

	// A dry run never claims and never prints the code.
	var got map[string]any
	srv := claimHub(t, 200, &got)
	out, errOut, code := s.sy(t, "", "setup", "--dry-run", "--json", "--hub", "join:"+srv.URL, "--code", "482-913")
	if code != 0 {
		t.Fatalf("dry run exit %d\n%s%s", code, out, errOut)
	}
	srv.Close() // waits for the handlers, so reading got is race free
	if got != nil {
		t.Fatalf("dry run claimed the code: %v", got)
	}
	if strings.Contains(out+errOut, "482-913") {
		t.Fatalf("the code is in the plan output:\n%s", out)
	}

	// Apply claims it as a machine and connects with the returned token.
	got = nil
	srv = claimHub(t, 200, &got)
	out, errOut, code = s.sy(t, "", "setup", "--yes", "--no-timer", "--hub", "join:"+srv.URL, "--code", "482-913")
	if code != 0 {
		t.Fatalf("apply exit %d\n%s%s", code, out, errOut)
	}
	srv.Close()
	if got["code"] != "482-913" || got["expect_kind"] != "machine" {
		t.Errorf("claim body %v", got)
	}
	if strings.Contains(out+errOut, "482-913") {
		t.Errorf("the code was echoed:\n%s%s", out, errOut)
	}
	// The fake hub is on loopback, so the hub counts as local; no terminal, so no offer.
	if !strings.Contains(out, "Phone: run `sy phone pair` on this machine") {
		t.Errorf("phone line missing:\n%s", out)
	}
	found := false
	_ = filepath.Walk(s.syHome, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && filepath.Base(p) == "token" {
			b, _ := os.ReadFile(p)
			found = strings.TrimSpace(string(b)) == goodToken
		}
		return nil
	})
	if !found {
		t.Error("the claimed token was not stored")
	}

	// A refused code leaves the hub step skipped (exit 6), not a crash.
	bad := claimHub(t, 401, &map[string]any{})
	s2 := newSetupHome(t, detecttest.Laptop(t))
	out, errOut, code = s2.sy(t, "", "setup", "--yes", "--no-timer", "--hub", "join:"+bad.URL, "--code", "000-000")
	if code != 6 || !strings.Contains(errOut, "not valid") {
		t.Errorf("refused code: exit %d\n%s", code, errOut)
	}
	// The report says the setup is only partly done, which step failed and how to finish.
	for _, want := range []string{"sy setup — partly applied (hub step failed)", "✗ not done: connect: no hub token given",
		"fix: make a code on the hub (sy hub pair --kind machine), then rerun: sy setup --hub join:" + bad.URL + " --code <code>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sy setup — applied") {
		t.Errorf("a failed hub step reported as applied:\n%s", out)
	}
	s3 := newSetupHome(t, detecttest.Laptop(t))
	out, _, code = s3.sy(t, "", "setup", "--yes", "--json", "--no-timer", "--hub", "join:"+bad.URL, "--code", "000-000")
	var rep struct {
		Mode string
		Hub  struct{ Step, Error, Fix string }
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != 6 {
		t.Fatalf("json: exit %d %v\n%s", code, err, out)
	}
	if rep.Mode != "partly-applied" || rep.Hub.Step != "connect" || rep.Hub.Error == "" || !strings.Contains(rep.Hub.Fix, "sy hub pair --kind machine") {
		t.Errorf("json report %+v", rep)
	}
}

func phoneEnv(stdin string, pp func([]string, io.Writer, io.Writer) error) (*setupEnv, *bytes.Buffer) {
	var out bytes.Buffer
	e := &setupEnv{stdin: strings.NewReader(stdin), stdout: &out, stderr: io.Discard, phonePair: pp, http: http.DefaultClient}
	e.dirs.Data.Path = "/data"
	return e, &out
}

func hubURL(s string) model.State {
	var st model.State
	st.Hub.URL = &s
	return st
}

func TestPhoneStepMessages(t *testing.T) {
	never := func([]string, io.Writer, io.Writer) error { t.Error("pair must not run"); return nil }
	cases := []struct {
		name  string
		st    model.State
		hub   model.HubChoice
		offer bool
		want  string
	}{
		{"none", model.State{}, model.HubChoice{Mode: model.HubLater}, true, "Phone: later (connect a hub first, then sy phone pair)"},
		{"remote join", model.State{}, model.HubChoice{Mode: model.HubJoin, URL: "https://hub.example/"}, true, "Phone: open https://hub.example -> Settings -> Add phone, or run `sy phone pair` on the hub"},
		{"remote kept", hubURL("https://hub.example"), model.HubChoice{Mode: model.HubKeep}, true, "Phone: open https://hub.example"},
		{"local quiet", model.State{}, model.HubChoice{Mode: model.HubHere}, false, "Phone: run `sy phone pair` on this machine"},
		{"kept loopback quiet", hubURL("http://localhost:8787"), model.HubChoice{}, false, "Phone: run `sy phone pair` on this machine"},
	}
	for _, c := range cases {
		e, out := phoneEnv("y\n", never)
		e.phoneStep(c.st, c.hub, "", c.offer)
		if !strings.Contains(out.String(), c.want) {
			t.Errorf("%s: got %q, want %q", c.name, out.String(), c.want)
		}
	}
}

func TestPhoneStepInteractive(t *testing.T) {
	here := model.HubChoice{Mode: model.HubHere}

	// No: nothing runs.
	e, out := phoneEnv("n\n", func([]string, io.Writer, io.Writer) error { t.Error("pair ran after no"); return nil })
	e.phoneStep(model.State{}, here, "", true)
	if !strings.Contains(out.String(), "Pair a phone now? [y/N]") || !strings.Contains(out.String(), "sy phone pair") {
		t.Errorf("no: %q", out.String())
	}

	// Yes: runs the phone pair path with the data dir's database.
	var args []string
	e, out = phoneEnv("y\n", func(a []string, w, _ io.Writer) error { args = a; io.WriteString(w, "CODE 482-913\n"); return nil })
	e.phoneStep(model.State{}, here, "", true)
	if len(args) != 2 || args[0] != "--db" || args[1] != filepath.Join("/data", "hub.db") || !strings.Contains(out.String(), "CODE 482-913") {
		t.Errorf("yes: args %v out %q", args, out.String())
	}

	// No https address: guidance, not a failure.
	e, out = phoneEnv("y\n", func([]string, io.Writer, io.Writer) error { return pair.ErrNoURL })
	e.phoneStep(model.State{}, here, "", true)
	if !strings.Contains(out.String(), "tailscale serve --bg 8787") || !strings.Contains(out.String(), "sy phone pair --url https://<name>.ts.net") {
		t.Errorf("no url: %q", out.String())
	}

	// A hub on another port (--listen): the hint names that port, not 8787.
	e, out = phoneEnv("y\n", func([]string, io.Writer, io.Writer) error { return pair.ErrNoURL })
	e.phoneStep(model.State{}, here, "127.0.0.1:19100", true)
	if !strings.Contains(out.String(), "tailscale serve --bg 19100") || strings.Contains(out.String(), "8787") {
		t.Errorf("listen: %q", out.String())
	}

	// Another failure is reported and setup carries on.
	e, out = phoneEnv("y\n", func([]string, io.Writer, io.Writer) error { return errors.New("boom") })
	e.phoneStep(model.State{}, here, "", true)
	if !strings.Contains(out.String(), "could not pair now (boom)") {
		t.Errorf("failure: %q", out.String())
	}
}
