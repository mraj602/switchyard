package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/store"
)

// A device that is already revoked still gets its targets deleted
// and its unsent deliveries skipped.
func TestRevokeCascadeCleansAlreadyRevokedDevice(t *testing.T) {
	_, st, _ := newAuth(t)
	ctx := context.Background()
	phone, _ := Create(ctx, st, CreateRequest{Kind: "phone", Name: "phone"})
	err := st.Write(ctx, func(tx *store.Tx) error {
		for _, q := range []string{
			`INSERT INTO alert (id, episode, trigger, status, created_at, facts, updated_at) VALUES ('al_1', 'e1', 'asked', 'fired', 1, '{}', 1)`,
			`INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at) VALUES ('pt_a', 'webpush', '` + phone.Device.ID + `', 'https://push.example/a', 1, 1)`,
			`INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, next_at, created_at, updated_at) VALUES ('al_1', 'pt_a', 'webpush', 1, 'pending', 5, 1, 1)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return tx.RevokeDevice(phone.Device.ID) // the device is revoked without the cascade
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Revoke(ctx, st, phone.Device.ID); err != nil {
		t.Fatalf("cascade on an already revoked device: %v", err)
	}
	count := func(q string) (n int) {
		if err := st.R().QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM push_target WHERE device_id = '` + phone.Device.ID + `'`); n != 0 {
		t.Errorf("%d targets of the revoked device remain", n)
	}
	if n := count(`SELECT count(*) FROM delivery WHERE status = 'pending'`); n != 0 {
		t.Errorf("%d pending deliveries remain", n)
	}
	// Nothing left to clean: still not found.
	if err := Revoke(ctx, st, phone.Device.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("third revoke: %v", err)
	}
	if err := Revoke(ctx, st, "dev_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown device: %v", err)
	}
}
