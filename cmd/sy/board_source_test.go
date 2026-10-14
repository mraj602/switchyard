package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mraj602/switchyard/internal/board"
	"github.com/mraj602/switchyard/internal/board/hubsrc"
)

// boardHome is an isolated home with config.toml holding cfg ("" for none); it returns the
// state folder.
func boardHome(t *testing.T, cfg string) string {
	t.Helper()
	isolateBoard(t)
	home := os.Getenv("SY_HOME")
	if cfg != "" {
		if err := os.MkdirAll(filepath.Join(home, "config"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "config", "config.toml"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(home, "state")
}

// No hub.url: auto is the local fold with hub.state none; --source hub is exit 4.
func TestBoardSourceNoHub(t *testing.T) {
	boardHome(t, "")
	code, out, errb := runBoardCmd(t, "--once", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	v, err := board.Decode([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if v.Source != board.SourceLocal || v.Hub.State != board.HubNone || v.Machine.ID == "" {
		t.Errorf("view %+v %+v %+v", v.Source, v.Hub, v.Machine)
	}
	if code, _, errb := runBoardCmd(t, "--once", "--source", "hub"); code != 4 || !strings.Contains(errb, "board connect") {
		t.Errorf("--source hub without a hub: exit %d %q", code, errb)
	}
}

// hub.url without a board token: local, unpaired; --source hub is exit 4.
func TestBoardSourceUnpaired(t *testing.T) {
	boardHome(t, "[hub]\nurl = \"https://hub.example.test\"\n")
	code, out, errb := runBoardCmd(t, "--once", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	var v board.View
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Source != board.SourceLocal || v.Hub.State != board.HubUnpaired {
		t.Errorf("view %+v %+v", v.Source, v.Hub)
	}
	if code, _, _ := runBoardCmd(t, "--once", "--source", "hub"); code != 4 {
		t.Errorf("--source hub unpaired: exit %d", code)
	}
}

// With a token: auto picks the hub source; --source hub probes the hub and exits 5 when it
// does not answer, 4 when it refuses the token (which is then dropped).
func TestBoardSourceHub(t *testing.T) {
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch st := int(status.Load()); {
		case r.Header.Get("Authorization") != "Bearer s3cret":
			w.WriteHeader(http.StatusUnauthorized)
		case st != 0:
			w.WriteHeader(st)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"time":"2026-10-20T14:00:00Z","machines":[],"stream_id":1}`))
		}
	}))
	t.Cleanup(srv.Close)
	hub := srv.URL
	state := boardHome(t, "[hub]\nurl = \""+hub+"\"\n")
	save := func() {
		if err := board.SaveToken(state, board.Token{URL: hub, Device: "d_1", Token: "s3cret"}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	sel := boardSelection{Mode: "auto", StateDir: state}
	sel.Config.Hub.URL = hub
	src, err := selectBoardSource(sel)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*hubsrc.Source); !ok {
		t.Fatalf("auto with a token: %T", src)
	}
	sel.Mode = "hub"
	if _, err := selectBoardSource(sel); err != nil {
		t.Errorf("--source hub, live hub: %v", err)
	}
	status.Store(http.StatusServiceUnavailable)
	if code, _, _ := runBoardCmd(t, "--once", "--source", "hub"); code != 5 {
		t.Errorf("hub down: exit %d, want 5", code)
	}
	status.Store(0)
	if err := board.SaveToken(state, board.Token{URL: hub, Device: "d_1", Token: "wrong"}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runBoardCmd(t, "--once", "--source", "hub"); code != 4 {
		t.Errorf("token refused: exit %d, want 4", code)
	}
	if _, ok := board.PeekToken(state); ok {
		t.Error("a refused token stayed")
	}
	// --source local never asks the hub.
	save()
	if code, out, errb := runBoardCmd(t, "--once", "--source", "local", "--json"); code != 0 || strings.Contains(out, "s3cret") {
		t.Errorf("--source local: %d %s", code, errb)
	}
}

// The `o` key opens only claude.ai links.
func TestBoardOpenURLAllowList(t *testing.T) {
	isolateBoard(t)
	if err := openRemoteURL(context.Background(), "https://evil.example/x"); err == nil {
		t.Error("a non-claude.ai link was opened")
	}
}
