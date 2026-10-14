package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlushStatusIsShown(t *testing.T) {
	e := newFakeHome(t, `{}`)
	write(t, filepath.Join(e.dirs.Config.Path, "config.toml"), "version = 1\n[hub]\nurl = \"https://hub.example\"\n")
	status := filepath.Join(e.dirs.State.Path, "outbox", "flush.status.json")

	// No flush yet: nothing to show.
	rep := Run(context.Background(), e.opts(nil))
	for _, c := range rep.Checks {
		if c.Name == "flush" {
			t.Fatalf("flush check before any flush: %+v", c)
		}
	}

	write(t, status, `{"time":"2026-10-12T11:59:00.000Z","ok":true,"last_success":"2026-10-12T11:59:00.000Z"}`)
	rep = Run(context.Background(), e.opts(nil))
	if c := wantLevel(t, rep, "flush", "", OK); !strings.Contains(c.Detail, "last success 1m ago") {
		t.Errorf("%q", c.Detail)
	}

	write(t, status, `{"time":"2026-10-12T11:59:00.000Z","ok":false,"error":"hub answered 401","auth_failed":true,"last_success":"2026-10-11T11:00:00.000Z"}`)
	rep = Run(context.Background(), e.opts(nil))
	if c := wantLevel(t, rep, "flush", "", Fail); !strings.Contains(c.Detail, "token rejected") || !strings.Contains(c.Detail, "last success 25h ago") {
		t.Errorf("%q", c.Detail)
	}

	write(t, status, `{"time":"2026-10-12T11:59:00.000Z","ok":false,"error":"hub refuses records","refuses_records":true}`)
	rep = Run(context.Background(), e.opts(nil))
	if c := wantLevel(t, rep, "flush", "", Fail); !strings.Contains(c.Detail, "hub refuses records") || !strings.Contains(c.Detail, "no successful flush yet") {
		t.Errorf("%q", c.Detail)
	}

	write(t, status, `{"time":"2026-10-12T11:59:00.000Z","ok":false,"error":"connection refused","last_success":"2026-10-12T11:00:00.000Z"}`)
	rep = Run(context.Background(), e.opts(nil))
	wantLevel(t, rep, "flush", "", Warn)
}
