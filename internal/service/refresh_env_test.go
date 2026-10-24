package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/model"
)

const fixtureUnit = `[Unit]
Description=Watchyard hub (merged mode)
After=network.target

[Service]
Type=simple
Environment=WY_DATA_DIR=/srv/wy
Environment="WY_HOME=/home/al ice" "TOKEN=a$$b"
ExecStart=/old/path/wy hub serve --listen 127.0.0.1:8787
Restart=on-failure

[Install]
WantedBy=default.target
`

// A bare non-interactive ssh session has none of the WY_* variables: the refresh must keep
// the ones the unit was installed with.
func TestRefreshKeepsInstalledEnvSystemd(t *testing.T) {
	home := t.TempDir()
	e := envFor("linux", home, &rec{})
	p := SystemdUnitPath(e, "watchyard-hub")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(fixtureUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(e).Refresh(context.Background(), "watchyard-hub")
	if err != nil || !res.Rewritten {
		t.Fatalf("%+v %v", res, err)
	}
	got, _ := os.ReadFile(p)
	for _, want := range []string{"Environment=WY_DATA_DIR=/srv/wy\n", `Environment="WY_HOME=/home/al ice"`, `Environment="TOKEN=a$$b"`, "ExecStart=/home/alice/.local/bin/wy hub serve"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if !reflect.DeepEqual(SystemdEnv(string(got)), SystemdEnv(fixtureUnit)) {
		t.Errorf("env changed: %v", SystemdEnv(string(got)))
	}
}

const fixturePlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.watchyard.hub</string>
	<key>ProgramArguments</key>
	<array>
		<string>/old/wy</string>
		<string>hub</string>
		<string>serve</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>WY_DATA_DIR</key>
		<string>/srv/s&amp;y</string>
	</dict>
</dict>
</plist>
`

func TestRefreshKeepsInstalledEnvLaunchd(t *testing.T) {
	home := t.TempDir()
	e := envFor("darwin", home, &rec{})
	p := UnitPath(e, model.ServiceSpec{Name: "watchyard-hub"})
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(fixturePlist), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(e).Refresh(context.Background(), "watchyard-hub")
	if err != nil || !res.Rewritten {
		t.Fatalf("%+v %v", res, err)
	}
	got, _ := os.ReadFile(p)
	if env := LaunchdEnv(string(got)); len(env) != 1 || env[0] != [2]string{"WY_DATA_DIR", "/srv/s&y"} {
		t.Errorf("env %v in:\n%s", env, got)
	}
}
