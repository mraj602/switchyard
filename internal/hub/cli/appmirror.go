package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mraj602/switchyard/internal/hub/appmirror"
	"github.com/mraj602/switchyard/internal/hub/push/netguard"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// Settings of the app mirror (phone.md P16.2), in the push settings table like hub.public_url:
// app.android_mirror (JSON bool, default on) and app.android_repo (JSON "owner/name").
const (
	settingAppMirror = "app.android_mirror"
	settingAppRepo   = "app.android_repo"
	appMirrorEvery   = 24 * time.Hour
)

// newAppMirror builds the mirror under <data>/app/android. off (--no-app-mirror) wins over the
// setting; repo (--app-repo) wins over the setting and the build's version.Repo.
func newAppMirror(st *store.Store, dataDir, repoFlag, defRepo string, off bool, logf func(string, ...any)) *appmirror.Mirror {
	setting := func(ctx context.Context, key string, into any) bool {
		raw, err := st.PushSetting(ctx, key)
		return err == nil && json.Unmarshal(raw, into) == nil
	}
	repo := defRepo
	if repoFlag != "" {
		repo = repoFlag
	} else {
		var r string
		if setting(context.Background(), settingAppRepo, &r) && r != "" {
			repo = r
		}
	}
	return appmirror.New(appmirror.Config{
		Dir:  filepath.Join(dataDir, "app", "android"),
		Repo: repo,
		Enabled: func(ctx context.Context) bool {
			if off {
				return false
			}
			var on bool
			if setting(ctx, settingAppMirror, &on) {
				return on
			}
			return true
		},
		Client: appMirrorClient(),
		Log:    logf,
	})
}

// appMirrorClient dials public addresses only (the netguard check runs after DNS) and follows
// at most 5 https redirects, which GitHub's release downloads need.
func appMirrorClient() *http.Client {
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second, Control: netguard.Control}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect to a non-https address")
			}
			return nil
		}}
}
