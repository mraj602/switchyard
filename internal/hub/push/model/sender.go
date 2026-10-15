package model

import "context"

// Sender is implemented by the webpush, ntfy and link packages (and by fakes in tests). Send
// must not retry, must honour ctx, and must never put the message text into Result.Err.
type Sender interface {
	Kind() string // KindWebPush, KindNtfy or KindLink
	Send(ctx context.Context, t Target, m Message) Result
}

// AlertService is what the alert handlers need from the engine (push.md 4, 8, 10). The
// engine implements it and api/alerts.go type-asserts api.Config.Engine to it (nil or not
// implemented: answer 503). Errors are the sentinels below; test with errors.Is.
type AlertService interface {
	// Snooze moves the alert's next push; minutes 0 means Settings.SnoozeMin.
	Snooze(ctx context.Context, alertID string, minutes int, by string) (AlertBrief, error)
	// Seen marks the alert seen.
	Seen(ctx context.Context, alertID, by string) (AlertBrief, error)
	// Act applies an action token (push.md 8); applied is false for a replay or no-op.
	Act(ctx context.Context, token string) (b AlertBrief, applied bool, err error)
}

// Errors of AlertService.
var (
	ErrNotFound = errorString("alert not found")                              // 404
	ErrConflict = errorString("alert is in a state that does not allow this") // 409
	ErrBadToken = errorString("malformed action token")                       // 400
	ErrBadMAC   = errorString("bad action token")                             // 401
	ErrExpired  = errorString("action token expired")                         // 410
)

type errorString string

func (e errorString) Error() string { return string(e) }
