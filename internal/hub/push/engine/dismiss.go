package engine

import (
	"encoding/json"
	"errors"
	"time"

	hublink "github.com/mraj602/switchyard/internal/hub/link"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/rules"
	"github.com/mraj602/switchyard/internal/link"
)

// dismissTTL bounds a dismiss frame: a phone offline longer has dropped the alert anyway
// (the longest alert TTL of push.md 6.4 is a day).
const dismissTTL = 24 * time.Hour

// dismiss queues {"v":1,"type":"dismiss","tag","alerts"} on every link device that was sent
// alert a (push.md 13.3), with the alert's own tag so it collapses an undelivered copy.
// Seen on one phone clears the others. A device without a link is skipped.
func (w *work) dismiss(a model.Alert) error {
	q := w.e.cfg.Link
	if q == nil {
		return nil
	}
	ts, err := w.tx.LinkDismissTargets(a.ID)
	if err != nil || len(ts) == 0 {
		return err
	}
	tag := rules.Tag(a)
	body, _ := json.Marshal(map[string]any{"v": model.PayloadVersion, "type": model.TypeDismiss, "tag": tag, "alerts": []string{a.ID}})
	for _, t := range ts {
		_, err := q.EnqueueTx(w.tx, t[1], link.TypeAlert, hublink.TagFor(tag), dismissTTL, body, "")
		switch {
		case err == nil, errors.Is(err, hublink.ErrNoLink):
		case errors.Is(err, hublink.ErrQueueFull):
			w.e.log.Printf("alert %s: dismiss for %s not queued: link queue full", a.ID, t[1])
		default:
			return err
		}
	}
	return nil
}
