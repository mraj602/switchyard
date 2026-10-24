package main

// The steps `wy update --post-update <old>` runs with the new binary (machines.md 7.3 step 7):
// refresh the wy-local plugin per plugin root, rewrite the timer units if this binary renders
// them differently, rewrite and restart the hub service if it is installed here, send
// machine_info, and summarise `wy doctor`. internal/update runs them in this order; the
// logic lives in pluginmkt, timer, service and machineinfo.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/doctor"
	"github.com/mraj602/watchyard/internal/service"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/pluginmkt"
	"github.com/mraj602/watchyard/internal/timer"
	"github.com/mraj602/watchyard/internal/update"
	"github.com/mraj602/watchyard/internal/version"
)

// postUpdateEnv builds the machine as the post-update steps see it; tests replace it.
var postUpdateEnv = func() (*setupEnv, error) { return newSetupEnv(io.Discard, io.Discard) }

func init() {
	for _, s := range []update.PostStep{
		{Name: "plugin", Run: postUpdatePlugin},
		{Name: "timer", Run: postUpdateTimer},
		{Name: "hub service", Run: postUpdateService},
		{Name: "agent service", Run: postUpdateAgent},
		{Name: "machine_info", Run: postUpdateMachineInfo},
		{Name: "doctor", Run: postUpdateDoctor},
	} {
		update.RegisterPostUpdate(s)
	}
}

// postUpdatePlugin rewrites <state>/plugin/marketplace for this version, then per plugin
// root (as the folder that owns it) `claude plugin marketplace update wy-local` and
// `claude plugin update watchyard@wy-local` (setup.md 5.2). Nothing without a wy-local
// install in the ledger (an adopted plugin is not ours to update).
func postUpdatePlugin(ctx context.Context, _ string) (string, error) {
	e, err := postUpdateEnv()
	if err != nil {
		return "", err
	}
	led, err := ledger.Read(e.dirs.State.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "no wy setup here", nil
		}
		return "", err
	}
	if led.Plugin == nil || led.Plugin.Adopted {
		return "no wy-local plugin here", nil
	}
	mkt := led.Plugin.Marketplace
	if mkt == "" {
		mkt = filepath.Join(e.dirs.State.Path, "plugin", "marketplace")
	}
	inst := pluginmkt.New(e.runner)
	if err := inst.WriteMarketplace(mkt, e.wy, version.Version); err != nil {
		return "", fmt.Errorf("rewrite %s: %w", mkt, err)
	}
	var failed []string
	for _, root := range led.Plugin.Roots {
		if err := inst.Update(ctx, pluginRootOwner(e.home, root)); err != nil {
			failed = append(failed, root+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return "", errors.New(strings.Join(failed, "; "))
	}
	return fmt.Sprintf("%s %s in %d plugin root(s)", pluginmkt.PluginName, version.Version, len(led.Plugin.Roots)), nil
}

// pluginRootOwner is the folder whose own plugins/ is root (setup.md 5.2 step 2); for
// ~/.claude, CLAUDE_CONFIG_DIR is unset.
func pluginRootOwner(home, root string) claudecli.Folder {
	dir := filepath.Dir(filepath.Clean(root))
	return claudecli.Folder{Dir: dir, Unset: dir == filepath.Join(home, ".claude")}
}

// postUpdateTimer rewrites the timer's unit files when this binary renders them differently
// (timer.Install, which also re-enables the timer). Nothing when there is no timer.
func postUpdateTimer(_ context.Context, _ string) (string, error) {
	e, err := postUpdateEnv()
	if err != nil {
		return "", err
	}
	te := e.timerEnv()
	te.Out = io.Discard
	// Keep the folder overrides the timer was installed with: this process (often a
	// non-interactive ssh `wy update`) may not carry them.
	if inst := installedTimerEnv(e, te); inst != nil {
		te.Getenv = inst
	}
	want := map[string][]byte{}
	switch e.goos {
	case "linux":
		dir := timer.SystemdDir(te)
		want[filepath.Join(dir, timer.SystemdName+".service")] = []byte(timer.SystemdService(te))
		want[filepath.Join(dir, timer.SystemdName+".timer")] = []byte(timer.SystemdTimer())
	case "darwin":
		want[timer.LaunchAgentPath(te)] = []byte(timer.LaunchdPlist(te))
	default:
		return "unchanged (" + e.goos + ")", nil
	}
	installed, same := false, true
	for p, b := range want {
		cur, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		installed = true
		if !bytes.Equal(cur, b) {
			same = false
		}
	}
	switch {
	case !installed:
		return "no timer here", nil
	case same:
		return "unchanged", nil
	}
	if _, err := timer.Install(te); err != nil {
		return "", err
	}
	return "rewritten", nil
}

// installedTimerEnv returns a Getenv over the Environment= / EnvironmentVariables of the
// installed timer unit, or nil when there is none to read.
func installedTimerEnv(e *setupEnv, te timer.Env) func(string) string {
	var p string
	var parse func(string) [][2]string
	switch e.goos {
	case "linux":
		p, parse = filepath.Join(timer.SystemdDir(te), timer.SystemdName+".service"), service.SystemdEnv
	case "darwin":
		p, parse = timer.LaunchAgentPath(te), service.LaunchdEnv
	default:
		return nil
	}
	return readUnitEnv(p, parse)
}

func readUnitEnv(p string, parse func(string) [][2]string) func(string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, kv := range parse(string(b)) {
		m[kv[0]] = kv[1]
	}
	return func(k string) string { return m[k] }
}

// postUpdateService rewrites (if needed) and restarts the hub service when it is installed
// here, so the hub runs the new binary.
func postUpdateService(ctx context.Context, _ string) (string, error) {
	e, err := postUpdateEnv()
	if err != nil {
		return "", err
	}
	name := service.DefaultName
	if led, err := ledger.Read(e.dirs.State.Path); err == nil && led.HubService != nil && led.HubService.Name != "" {
		name = led.HubService.Name
	}
	se := e.serviceEnv()
	se.Out = io.Discard
	res, err := service.New(se).Refresh(ctx, name)
	if err != nil {
		return "", err
	}
	switch {
	case !res.Installed:
		return "no hub service here", nil
	case res.Rewritten:
		return "unit rewritten, restarted", nil
	}
	return "restarted", nil
}

// postUpdateAgent rewrites (if needed) and restarts the peers agent when its unit exists, so it
// runs the new binary. Nothing is installed where `wy agent enable` never ran.
func postUpdateAgent(ctx context.Context, _ string) (string, error) {
	e, err := postUpdateEnv()
	if err != nil {
		return "", err
	}
	se := e.serviceEnv()
	se.Out = io.Discard
	res, err := service.New(se).Refresh(ctx, service.AgentName)
	if err != nil {
		return "", err
	}
	if !res.Installed {
		return "no agent service here", nil
	}
	return "restarted", nil
}

// postUpdateMachineInfo queues machine_info with the new version (machines.md 6.3).
func postUpdateMachineInfo(_ context.Context, _ string) (string, error) {
	e, err := postUpdateEnv()
	if err != nil {
		return "", err
	}
	if _, err := sendMachineInfo(e.dirs, e.home, true); err != nil {
		return "", err
	}
	return "queued", nil
}

// postUpdateDoctor is the `wy doctor` summary line. Its findings never fail the update.
func postUpdateDoctor(ctx context.Context, _ string) (string, error) {
	e, err := postUpdateEnv()
	if err != nil {
		return "", err
	}
	rep := doctor.Run(ctx, doctor.Options{Home: e.home, Dirs: e.dirs, Getenv: e.getenv, Environ: os.Environ()})
	s := fmt.Sprintf("wy doctor: %d ok, %d warn, %d fail", rep.OK, rep.Warn, rep.Fail)
	if rep.Warn > 0 || rep.Fail > 0 {
		s += " (run wy doctor)"
	}
	return s, nil
}
