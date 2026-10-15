package rules

import (
	"errors"
	"testing"
)

// UI audit G9: a hub run as a systemd user service has no $TZ; its zone must still be the
// machine's (from /etc/localtime), not UTC.
func TestSystemZone(t *testing.T) {
	links := map[string]string{
		"/etc/localtime":  "../usr/share/zoneinfo/Asia/Kolkata",
		"/etc/mac":        "/var/db/timezone/zoneinfo/Europe/Paris",
		"/etc/posix":      "/usr/share/zoneinfo/posix/America/New_York",
		"/etc/bogus":      "/usr/share/zoneinfo/Not/AZone",
		"/etc/notzoneinf": "/opt/localtime",
	}
	readlink := func(p string) (string, error) {
		if v, ok := links[p]; ok {
			return v, nil
		}
		return "", errors.New("not a link")
	}
	noLink := func(string) (string, error) { return "", errors.New("no") }
	for _, tc := range []struct {
		name, tz string
		rl       func(string) (string, error)
		want     string
	}{
		{"TZ wins", "Europe/Berlin", readlink, "Europe/Berlin"},
		{"TZ with colon", ":Asia/Tokyo", readlink, "Asia/Tokyo"},
		{"no TZ: /etc/localtime", "", readlink, "Asia/Kolkata"},
		{"bad TZ: /etc/localtime", "Mars/Olympus", readlink, "Asia/Kolkata"},
		{"TZ is a link path", ":/etc/mac", readlink, "Europe/Paris"},
		{"TZ is a zoneinfo file", "/usr/share/zoneinfo/Asia/Dubai", noLink, "Asia/Dubai"},
		{"posix subtree", ":/etc/posix", readlink, "America/New_York"},
		{"unknown zone in link", ":/etc/bogus", readlink, ""},
		{"link outside zoneinfo", ":/etc/notzoneinf", readlink, ""},
		{"nothing", "", noLink, ""},
		{"UTC", "UTC", noLink, "UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemZone(tc.tz, tc.rl); got != tc.want {
				t.Fatalf("systemZone(%q) = %q, want %q", tc.tz, got, tc.want)
			}
		})
	}
}

func TestHubZoneSetting(t *testing.T) {
	if z, src := HubZone("Asia/Kolkata"); z != "Asia/Kolkata" || src != ZoneSetting {
		t.Fatalf("%s %s", z, src)
	}
	t.Setenv("TZ", "America/Chicago")
	if z, src := HubZone(""); z != "America/Chicago" || src != ZoneSystem {
		t.Fatalf("%s %s", z, src)
	}
	if z, src := HubZone("../../etc/passwd"); z != "America/Chicago" || src != ZoneSystem {
		t.Fatalf("a bad setting: %s %s", z, src)
	}
}
