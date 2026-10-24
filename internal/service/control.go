package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mraj602/watchyard/internal/setup/model"
)

// Start and Stop (`wy desktop hub start|stop`, desktop.md C4) start or stop the hub
// service that Install made, without touching its unit file. They use the same Runner as
// Install, so tests record the commands and nothing real runs.

// ErrNotInstalled is returned by Start and Stop when the service was never installed (no
// unit file, no task): the desktop app then shows `wy setup --hub here`.
var ErrNotInstalled = errors.New("the hub service is not installed (run `wy setup --hub here`)")

// UnitName is the service manager's name for the service, for display: the systemd unit
// ("watchyard-hub.service"), the LaunchAgent label ("com.watchyard.hub") or the Task
// Scheduler task ("Watchyard Hub").
func UnitName(goos, name string) string {
	if name == "" {
		name = DefaultName
	}
	switch goos {
	case "darwin":
		return LaunchdLabel(name)
	case "windows":
		return WindowsTask(name)
	}
	return name + ".service"
}

// Start starts the installed service: `systemctl --user start`, `launchctl kickstart` (after
// a `bootstrap` when the agent is not loaded) or `schtasks /Run`.
func (s *Service) Start(ctx context.Context, name string) error {
	return s.control(ctx, name, true)
}

// Stop stops the service: `systemctl --user stop`, `launchctl bootout` or `schtasks /End`.
// It stays installed, and starts again with Start (or at the next login).
func (s *Service) Stop(ctx context.Context, name string) error {
	return s.control(ctx, name, false)
}

func (s *Service) control(ctx context.Context, name string, start bool) error {
	spec, err := normalize(model.ServiceSpec{Name: name})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	spec.Name = Installed(s.Env, spec.Name)
	st, err := s.Status(ctx, spec.Name)
	if err != nil {
		return err
	}
	if !st.Installed {
		return ErrNotInstalled
	}
	e := s.Env
	fail := func(what string, err error, out []byte) error {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%s: %v: %s", what, err, msg)
		}
		return fmt.Errorf("%s: %v", what, err)
	}
	switch e.GOOS {
	case "linux":
		verb := "stop"
		if start {
			verb = "start"
		}
		unit := spec.Name + ".service"
		if out, err := s.run("systemctl", "--user", verb, unit); err != nil {
			return fail("systemctl --user "+verb+" "+unit, err, out)
		}
	case "darwin":
		domain := fmt.Sprintf("gui/%d", e.UID)
		target := domain + "/" + LaunchdLabel(spec.Name)
		if !start {
			if out, err := s.run("launchctl", "bootout", target); err != nil {
				return fail("launchctl bootout "+target, err, out)
			}
			return nil
		}
		if _, err := s.run("launchctl", "kickstart", target); err != nil {
			// A stopped agent was booted out, so it is not loaded: load it, then start it.
			plist := InstalledPath(e, spec.Name)
			if out, berr := s.run("launchctl", "bootstrap", domain, plist); berr != nil {
				return fail("launchctl bootstrap "+domain+" "+plist, berr, out)
			}
			if out, kerr := s.run("launchctl", "kickstart", target); kerr != nil {
				return fail("launchctl kickstart "+target, kerr, out)
			}
		}
	case "windows":
		flag := "/End"
		if start {
			flag = "/Run"
		}
		task := WindowsTask(spec.Name)
		if out, err := s.run("schtasks", flag, "/TN", task); err != nil {
			return fail("schtasks "+flag+" /TN "+task, err, out)
		}
	default:
		return fmt.Errorf("service: no user service support for %s", e.GOOS)
	}
	return nil
}
