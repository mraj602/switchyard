// Package pushlink is the link channel's sender (push.md 13.2): a push for a phone's `link`
// target becomes an `alert` frame on its durable queue (internal/hub/link), and a widget
// refresh a `refresh` frame (13.5). The transport, sealing and acks are the link's.
//
// Contract: docs/spec/push.md 13. Depends on: hub/link, hub/push/model,
// hub/push/webpush (the payload bytes), hub/store (the delivery ref).
package pushlink

import (
	"context"
	"errors"
	"time"

	hublink "github.com/mraj602/watchyard/internal/hub/link"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/webpush"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/link"
)

// Queue is the link's enqueue (hublink.Hub).
type Queue interface {
	Enqueue(ctx context.Context, device, t, tag string, ttl time.Duration, plaintext []byte, ref string) (int64, error)
}

// DefaultTTL is used when a message has none (a test push).
const DefaultTTL = 24 * time.Hour

// Sender implements model.Sender for KindLink.
type Sender struct {
	Q Queue
}

// New returns a sender over q.
func New(q Queue) *Sender { return &Sender{Q: q} }

// Kind is model.KindLink.
func (s *Sender) Kind() string { return model.KindLink }

// Send queues the push (push.md 13.2). Result: enqueued → sent (Ref empty: `sent` means
// queued, delivery.delivered_at says when the phone acked); no keys or revoked → gone; a
// full queue → retry.
func (s *Sender) Send(ctx context.Context, t model.Target, m model.Message) model.Result {
	if t.DeviceID == "" {
		return model.Result{Outcome: model.OutcomeGone, Err: "link target without a device"}
	}
	var (
		typ, tag, ref string
		body          []byte
		ttl           = m.TTL
	)
	if m.Payload.Type == model.TypeRefresh {
		if len(m.Data) == 0 {
			return model.Result{Outcome: model.OutcomeFailed, Err: "refresh without a snapshot"}
		}
		typ, tag, body = link.TypeRefresh, "refresh", m.Data
		ttl = hublink.TTLRefresh
	} else {
		p := m.Payload
		p.Origin = m.HubURL
		b, err := webpush.PayloadJSON(p)
		if err != nil {
			return model.Result{Outcome: model.OutcomeFailed, Err: "payload: " + err.Error()}
		}
		typ, tag, body = link.TypeAlert, hublink.TagFor(p.Tag), b
		if len(m.Deliveries) > 0 {
			ref = store.DeliveryRef(m.Deliveries)
		}
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	_, err := s.Q.Enqueue(ctx, t.DeviceID, typ, tag, ttl, body, ref)
	switch {
	case err == nil:
		return model.Result{Outcome: model.OutcomeSent}
	case errors.Is(err, hublink.ErrNoLink):
		return model.Result{Outcome: model.OutcomeGone, Err: "the device has no link keys or was removed"}
	case errors.Is(err, hublink.ErrQueueFull):
		return model.Result{Outcome: model.OutcomeRetry, Err: "link queue full"}
	case errors.Is(err, hublink.ErrBadFrame):
		return model.Result{Outcome: model.OutcomeFailed, Err: err.Error()}
	}
	return model.Result{Outcome: model.OutcomeRetry, Err: "link: " + err.Error()}
}
