package model

import (
	"encoding/json"
	"fmt"
)

// Settings is the JSON object in setting key push.settings (push.md 2; api.yaml
// PushSettings). Load it with ParseSettings so absent keys keep their defaults.
type Settings struct {
	QuietHours   QuietHours `json:"quiet_hours"`
	Private      bool       `json:"private"`
	SkipRemote   bool       `json:"skip_remote"`
	SnoozeMin    int        `json:"snooze_min"`
	MaxAgeS      int        `json:"max_age_s"`
	BatchS       int        `json:"batch_s"`
	Flood        Flood      `json:"flood"`
	VapidSubject string     `json:"vapid_subject"` // empty: the hub's public URL (6.2)
	WebPushHosts []string   `json:"webpush_hosts"`
	// RefreshMin is the least time between two widget refresh frames to one link target, in
	// minutes (push.md 13.5; 10-240, default 15).
	RefreshMin int `json:"refresh_min"`
}

// Flood is the per-target push cap (push.md 5.3).
type Flood struct {
	Max  int `json:"max"`
	PerS int `json:"per_s"`
}

// DefaultWebPushHosts is the endpoint allow-list of push.md 6.6.
var DefaultWebPushHosts = []string{
	"fcm.googleapis.com", "*.googleapis.com", "web.push.apple.com", "*.push.apple.com",
	"updates.push.services.mozilla.com", "*.push.services.mozilla.com", "*.notify.windows.com",
}

// Defaults resets s to the documented defaults (push.md 2). QuietHours.TZ is left empty:
// ParseSettings fills it with the hub time zone.
func (s *Settings) Defaults() {
	*s = Settings{
		QuietHours:   QuietHours{Enabled: false, Start: "23:00", End: "08:00"},
		SnoozeMin:    15,
		MaxAgeS:      900,
		BatchS:       2,
		Flood:        Flood{Max: 6, PerS: 600},
		WebPushHosts: append([]string(nil), DefaultWebPushHosts...),
		RefreshMin:   15,
	}
}

// ParseSettings decodes stored JSON over the defaults (raw may be empty), fills the quiet
// hours time zone with hubTZ ("UTC" when that is empty too), and checks the ranges.
func ParseSettings(raw []byte, hubTZ string) (Settings, error) {
	var s Settings
	s.Defaults()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return Settings{}, fmt.Errorf("push settings: %w", err)
		}
	}
	if s.QuietHours.TZ == "" {
		s.QuietHours.TZ = hubTZ
		if s.QuietHours.TZ == "" {
			s.QuietHours.TZ = "UTC"
		}
	}
	return s, s.Validate()
}

// Validate checks the ranges of api.yaml PushSettings.
func (s Settings) Validate() error {
	switch {
	case s.SnoozeMin < 5 || s.SnoozeMin > 240:
		return fmt.Errorf("snooze_min must be 5-240")
	case s.MaxAgeS < 60 || s.MaxAgeS > 86400:
		return fmt.Errorf("max_age_s must be 60-86400")
	case s.BatchS < 0 || s.BatchS > 30:
		return fmt.Errorf("batch_s must be 0-30")
	case s.Flood.Max < 1 || s.Flood.PerS < 60:
		return fmt.Errorf("flood needs max >= 1 and per_s >= 60")
	case s.RefreshMin < 10 || s.RefreshMin > 240:
		return fmt.Errorf("refresh_min must be 10-240")
	}
	return nil
}
