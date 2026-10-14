package store

import (
	"context"
	"testing"
)

// The self-hosted profile turns expo off once; a hand-enabled channel survives a restart
// (push.md 13.6).
func TestApplySelfHostedOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := openTest(t, dir)
	applied, err := s.ApplySelfHosted(ctx)
	if err != nil || !applied {
		t.Fatalf("first: %v %v", applied, err)
	}
	if c, _ := s.ChannelByKind(ctx, "expo"); c.Enabled {
		t.Fatal("expo still on")
	}
	if _, err := s.SetChannel(ctx, "expo", true, nil); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.ApplySelfHosted(ctx); err != nil || applied {
		t.Fatalf("second: %v %v", applied, err)
	}
	if c, _ := s.ChannelByKind(ctx, "expo"); !c.Enabled {
		t.Fatal("hand-enabled expo turned off again")
	}
}

func TestDeliveryRefs(t *testing.T) {
	if r := DeliveryRef([]int64{3, 7}); r != "d:3,7" {
		t.Fatal(r)
	}
	if ids := DeliveryRefs("d:3,7"); len(ids) != 2 || ids[1] != 7 {
		t.Fatal(ids)
	}
	if ids := DeliveryRefs("pm:x"); len(ids) != 0 {
		t.Fatal(ids)
	}
}
