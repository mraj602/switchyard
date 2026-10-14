package scan

import (
	"context"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/identity"
)

// Given the explicit view of ~/.claude, both views (variable unset and set) are still run
// and merged, same as for the default slot.
func TestAgentsExplicitViewRunsBothViews(t *testing.T) {
	_, slots := agentsHome(t)
	explicit := slots[0]
	explicit.Default, explicit.Explicit = false, true
	var sawUnset, sawSet bool
	run := func(_ context.Context, env []string, _ string, _ ...string) ([]byte, error) {
		set := false
		for _, kv := range env {
			if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
				set = true
			}
		}
		if set {
			sawSet = true
		} else {
			sawUnset = true
		}
		return []byte("[]"), nil
	}
	got := CollectList(context.Background(), explicit, nil, run, identity.OSFS{})
	if !got.Complete || !sawSet || !sawUnset {
		t.Fatalf("complete=%v set=%v unset=%v", got.Complete, sawSet, sawUnset)
	}
}
