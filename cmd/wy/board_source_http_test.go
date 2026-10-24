package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/board/hubsrc"
)

// A plain-http hub that wy connect would refuse never sees the bearer token: not in the
// --source hub probe, not in the source. Auto falls back to the local fold.
func TestBoardSourcePlainHTTPNeverGetsToken(t *testing.T) {
	state := boardHome(t, "")
	hub := "http://192.0.2.10:7000"
	if err := board.SaveToken(state, board.Token{URL: hub, Device: "d_1", Token: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	old := boardProbe
	boardProbe = func(context.Context, string, string, http.RoundTripper, string, bool) error {
		t.Error("the probe ran against a plain-http hub")
		return nil
	}
	defer func() { boardProbe = old }()
	sel := boardSelection{Mode: "hub", StateDir: state}
	sel.Config.Hub.URL = hub
	if _, err := selectBoardSource(sel); !errors.Is(err, errBoardNoHub) {
		t.Errorf("--source hub: %v, want errBoardNoHub", err)
	}
	sel.Mode = "auto"
	src, err := selectBoardSource(sel)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*hubsrc.Source); ok {
		t.Error("auto built a hub source for a plain-http hub")
	}
	// The real probe applies the same rule.
	if err := probeBoardHub(context.Background(), hub, "s3cret", nil, state, false); err == nil {
		t.Error("probe accepted plain http with allowHTTP=false")
	}
}

func TestRemoveTokenIfKeepsANewerToken(t *testing.T) {
	state := t.TempDir()
	if err := board.SaveToken(state, board.Token{URL: "https://h.test", Device: "d_2", Token: "new"}); err != nil {
		t.Fatal(err)
	}
	if removed, _ := board.RemoveTokenIf(state, "old"); removed {
		t.Error("a stale 401 deleted the fresh token")
	}
	if removed, _ := board.RemoveTokenIf(state, "new"); !removed {
		t.Error("the matching token stayed")
	}
}
