package webpush

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// A summary of many alerts must still fit, not fail with ErrTooLarge.
func TestSummaryWithManyAlertsFits(t *testing.T) {
	for _, n := range []int{1, 20, 90, 500} {
		p := model.Payload{V: 1, Type: model.TypeSummary, Title: "many alerts", Body: "x", Tag: "sy-summary", URL: "/#/", Alert: "al_01JA3B00000000000000000"}
		for i := 0; i < n; i++ {
			p.Alerts = append(p.Alerts, fmt.Sprintf("al_01JA3B%020d", i))
		}
		b, err := payloadJSON(p)
		if err != nil || len(b) > MaxPayloadJSON {
			t.Fatalf("%d ids: err=%v len=%d", n, err, len(b))
		}
		var got model.Payload
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got.Title != "many alerts" || got.Body != "x" || got.Alert != p.Alert {
			t.Errorf("%d ids: text or first alert was cut: %+v", n, got)
		}
		if len(got.Alerts) == 0 || got.Alerts[0] != p.Alerts[0] || len(got.Alerts) > n {
			t.Errorf("%d ids: alerts list %v", n, got.Alerts)
		}
	}
}
