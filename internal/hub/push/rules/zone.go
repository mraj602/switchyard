package rules

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Zone sources of HubZone.
const (
	ZoneSetting = "setting" // the hub.tz setting
	ZoneSystem  = "system"  // the hub machine's zone ($TZ, /etc/localtime)
	ZoneUTC     = "utc"     // nothing named a zone
)

// HubZone is the hub's time zone (push.md 2): the hub.tz setting when it names a zone the
// hub can load, else the machine's own zone (SystemZone), else UTC. Hub-wide quiet hours,
// push text and analytics days use it. source says where it came from.
func HubZone(setting string) (name, source string) {
	if setting != "" && loadable(setting) {
		return setting, ZoneSetting
	}
	if z := SystemZone(); z != "" {
		return z, ZoneSystem
	}
	return "UTC", ZoneUTC
}

// SystemZone is the IANA name of this machine's zone: $TZ when it names one, else the zone
// /etc/localtime links to, else "" (Windows, a container without zone data). A systemd user
// service has no $TZ, so the link is what makes an IST machine's hub IST and not UTC.
func SystemZone() string { return systemZone(os.Getenv("TZ"), os.Readlink) }

func systemZone(tz string, readlink func(string) (string, error)) string {
	tz = strings.TrimPrefix(tz, ":")
	if tz != "" && !filepath.IsAbs(tz) && loadable(tz) {
		return tz
	}
	link := "/etc/localtime"
	if filepath.IsAbs(tz) {
		link = tz // TZ=:/etc/localtime or a zoneinfo file
	}
	target, err := readlink(link)
	if err != nil {
		if filepath.IsAbs(tz) {
			return zoneOfPath(tz)
		}
		return ""
	}
	return zoneOfPath(target)
}

// zoneOfPath is the part of a zoneinfo path after "zoneinfo/" when it is a loadable zone:
// ../usr/share/zoneinfo/Asia/Kolkata → Asia/Kolkata.
func zoneOfPath(p string) string {
	p = filepath.ToSlash(p)
	i := strings.LastIndex(p, "zoneinfo/")
	if i < 0 {
		return ""
	}
	name := p[i+len("zoneinfo/"):]
	// zoneinfo/posix/Asia/Kolkata and zoneinfo/right/… hold the same zones.
	for _, pre := range []string{"posix/", "right/"} {
		name = strings.TrimPrefix(name, pre)
	}
	if name == "" || !loadable(name) {
		return ""
	}
	return name
}

// ValidZone says whether name is an IANA zone the hub can load (not a path).
func ValidZone(name string) bool { return loadable(name) }

func loadable(name string) bool {
	if strings.ContainsAny(name, " \\") || strings.Contains(name, "..") || filepath.IsAbs(name) {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
