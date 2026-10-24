package detect_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mraj602/watchyard/internal/claudecli"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type failingRunner struct{ *claudecli.Fake }

func (failingRunner) Version(context.Context) (claudecli.VersionInfo, error) {
	return claudecli.VersionInfo{}, claudecli.ErrNotFound
}
