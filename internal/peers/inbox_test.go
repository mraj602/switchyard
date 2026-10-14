//go:build fakeinbox

package peers_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/peers"
	"github.com/mraj602/switchyard/internal/peers/fakeinbox"
)

func sockPath(t *testing.T, name string) string {
	t.Helper()
	// Unix socket paths are short (108 bytes): use a short temp dir, never the real cc-socks.
	d, err := os.MkdirTemp("", "syp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, name)
}

func wrap(mode peers.Mode, body string) peers.Wrapper {
	return peers.Wrapper{From: "uds:/tmp/r.sock", FromSession: "sender1", FromName: "api@work.laptop", FromMode: mode, Body: body}
}

func post(t *testing.T, srv *fakeinbox.Server, sock, sid string, w peers.Wrapper, id string) {
	t.Helper()
	if err := peers.WriteFrame(sock, peers.NewFrame(id, sid, w)); err != nil {
		t.Fatal(err)
	}
}

func TestFakeInboxGate(t *testing.T) {
	type tc struct {
		name    string
		mode    peers.Mode
		setting fakeinbox.Setting
		child   bool
		w       peers.Wrapper
		plain   bool
		sid     string
		want    fakeinbox.Outcome
		reason  string
	}
	good := "recv-session"
	cases := []tc{
		{"bypass to bypass", peers.ModeBypass, "", false, wrap(peers.ModeBypass, "x"), false, good, fakeinbox.Deliver, "mode_match"},
		{"prompting to prompting", peers.ModePrompting, "", false, wrap(peers.ModePrompting, "x"), false, good, fakeinbox.Deliver, "mode_match"},
		{"prompting to bypass is held", peers.ModeBypass, "", false, wrap(peers.ModePrompting, "x"), false, good, fakeinbox.Hold, "mode_mismatch"},
		{"bypass to prompting is held", peers.ModePrompting, "", false, wrap(peers.ModeBypass, "x"), false, good, fakeinbox.Hold, "mode_mismatch"},
		{"plain frame to bypass is held", peers.ModeBypass, "", false, wrap("", "x"), true, good, fakeinbox.Hold, "no_mode_asserted"},
		{"plain frame to prompting is admitted", peers.ModePrompting, "", false, wrap("", "x"), true, good, fakeinbox.Deliver, "prompting_default"},
		{"accept admits a mismatch", peers.ModeBypass, fakeinbox.SettingAccept, false, wrap(peers.ModePrompting, "x"), false, good, fakeinbox.Deliver, "explicit_setting"},
		{"hold holds a match", peers.ModeBypass, fakeinbox.SettingHold, false, wrap(peers.ModeBypass, "x"), false, good, fakeinbox.Hold, "explicit_setting"},
		{"refuse drops", peers.ModeBypass, fakeinbox.SettingRefuse, false, wrap(peers.ModeBypass, "x"), false, good, fakeinbox.Drop, "managed_setting"},
		{"own child admitted", peers.ModeBypass, "", true, wrap(peers.ModePrompting, "x"), false, good, fakeinbox.Deliver, "own_child"},
		{"wrong session id dropped", peers.ModeBypass, "", false, wrap(peers.ModeBypass, "x"), false, "other", fakeinbox.Drop, "session_mismatch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sock := sockPath(t, "a.sock")
			srv, err := fakeinbox.Listen(fakeinbox.Config{
				Path: sock, SessionID: good, Mode: c.mode, Setting: c.setting,
				IsChild: func(int) bool { return c.child },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()
			f := peers.NewFrame("m1", c.sid, c.w)
			if c.plain {
				f.Message.Content = "just text"
			}
			if err := peers.WriteFrame(sock, f); err != nil {
				t.Fatal(err)
			}
			if !srv.Wait(1, 2*time.Second) {
				t.Fatal("frame not received")
			}
			got := srv.All()[0]
			if got.Outcome != c.want || got.Reason != c.reason {
				t.Errorf("got %s/%s, want %s/%s", got.Outcome, got.Reason, c.want, c.reason)
			}
		})
	}
}

func TestFakeInboxNonCanonicalIsPlain(t *testing.T) {
	sock := sockPath(t, "b.sock")
	srv, err := fakeinbox.Listen(fakeinbox.Config{Path: sock, SessionID: "s", Mode: peers.ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	w := wrap(peers.ModeBypass, "x")
	f := peers.NewFrame("m1", "s", w)
	// Attributes in the wrong order: Claude Code treats it as plain text, so a bypass
	// receiver holds it.
	f.Message.Content = strings.Replace(f.Message.Content, `from-session="sender1" from-name="api@work.laptop"`, `from-name="api@work.laptop" from-session="sender1"`, 1)
	if err := peers.WriteFrame(sock, f); err != nil {
		t.Fatal(err)
	}
	srv.Wait(1, 2*time.Second)
	if r := srv.All()[0]; r.Canon || r.Outcome != fakeinbox.Hold {
		t.Errorf("got canon=%v outcome=%s", r.Canon, r.Outcome)
	}
}

func TestFakeInboxLimitsAndDuplicates(t *testing.T) {
	sock := sockPath(t, "c.sock")
	now := time.Unix(1_700_000_000, 0)
	srv, err := fakeinbox.Listen(fakeinbox.Config{Path: sock, SessionID: "s", Mode: peers.ModeBypass, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	for i := 0; i < 35; i++ {
		post(t, srv, sock, "s", wrap(peers.ModeBypass, strings.Repeat("x", i+1)), "m")
	}
	srv.Wait(35, 3*time.Second)
	if n := len(srv.By(fakeinbox.Deliver)); n != fakeinbox.Burst {
		t.Errorf("delivered %d, want the burst of %d", n, fakeinbox.Burst)
	}
	if n := len(srv.By(fakeinbox.Drop)); n != 5 {
		t.Errorf("dropped %d, want 5", n)
	}
	// Same body twice within 30 s: the second is a duplicate.
	srv2sock := sockPath(t, "d.sock")
	srv2, _ := fakeinbox.Listen(fakeinbox.Config{Path: srv2sock, SessionID: "s", Mode: peers.ModeBypass})
	defer srv2.Close()
	post(t, srv2, srv2sock, "s", wrap(peers.ModeBypass, "same"), "m1")
	post(t, srv2, srv2sock, "s", wrap(peers.ModeBypass, "same"), "m2")
	srv2.Wait(2, 2*time.Second)
	if r := srv2.All(); len(r) != 2 || r[1].Reason != "duplicate" {
		t.Errorf("duplicate not dropped: %+v", r)
	}
}

func TestWriteFrameTargetGone(t *testing.T) {
	err := peers.WriteFrame(sockPath(t, "none.sock"), peers.NewFrame("m", "s", wrap(peers.ModeBypass, "x")))
	if !errors.Is(err, peers.ErrTargetGone) {
		t.Errorf("err = %v", err)
	}
}

func TestWriteFrameTooBig(t *testing.T) {
	err := peers.WriteFrame(sockPath(t, "none.sock"), peers.NewFrame("m", "s", wrap(peers.ModeBypass, strings.Repeat("x", peers.MaxFrameBytes))))
	if !errors.Is(err, peers.ErrFrameTooBig) {
		t.Errorf("err = %v", err)
	}
}
