package api_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/pair"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// testPepperPath points the server's pairing pepper at a temp file for this test (never
// the real state folder) and returns the path. Call it before the first request.
func testPepperPath(t *testing.T) string {
	t.Helper()
	if api.PairPepperPath == "" {
		old := api.PairPepperPath
		api.PairPepperPath = filepath.Join(t.TempDir(), "pair_pepper")
		t.Cleanup(func() { api.PairPepperPath = old })
	}
	return api.PairPepperPath
}

// ownerSessionToken makes an owner session as `wy hub owner-link` does: a sy_o_ token whose
// hash is keyed with the pairing pepper.
func ownerSessionToken(t *testing.T, st *store.Store) auth.Created {
	t.Helper()
	return ownerSessionBy(t, st, "owner-link:pc_test")
}

// ownerSessionBy is ownerSessionToken with another created_by (machines_test: a session
// tied to a desktop device by "login:<id>").
func ownerSessionBy(t *testing.T, st *store.Store, createdBy string) auth.Created {
	t.Helper()
	pepper, err := pair.NewService(st, testPepperPath(t)).Pepper()
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.Create(context.Background(), st, auth.CreateRequest{Kind: "browser", Name: "owner", Owner: true,
		CreatedBy: createdBy, Pepper: pepper})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
