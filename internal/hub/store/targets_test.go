package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
)

func openTargets(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, ctx
}

func addDevice(t *testing.T, st *store.Store, ctx context.Context, id string) {
	t.Helper()
	err := st.Write(ctx, func(tx *store.Tx) error {
		return tx.InsertDevice(store.Device{ID: id, Kind: "phone", Name: id, TokenHint: "h", Scopes: []string{"push"}, CreatedAt: 1, CreatedBy: "test"},
			append(make([]byte, 31), byte(len(id))+id[len(id)-1]))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChannelsSeededAndSettable(t *testing.T) {
	st, ctx := openTargets(t)
	cs, err := st.Channels(ctx)
	// link: seeded by schema v8 (push.md 13).
	if err != nil || len(cs) != 3 || cs[0].Kind != "link" || cs[1].Kind != "ntfy" || cs[2].Kind != "webpush" || !cs[1].Enabled {
		t.Fatalf("%+v %v", cs, err)
	}
	c, err := st.SetChannel(ctx, "ntfy", false, nil)
	if err != nil || c.Enabled {
		t.Fatalf("%+v %v", c, err)
	}
	c, _ = st.SetChannel(ctx, "ntfy", true, []byte(`{"a":1}`))
	if string(c.Settings) != `{"a":1}` {
		t.Fatalf("%s", c.Settings)
	}
	c, _ = st.SetChannel(ctx, "ntfy", true, nil)
	if string(c.Settings) != `{"a":1}` {
		t.Fatal("nil settings should keep the stored object")
	}
	if _, err := st.SetChannel(ctx, "nope", true, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestTargetUpsertReviveAndResults(t *testing.T) {
	st, ctx := openTargets(t)
	addDevice(t, st, ctx, "dev_1")
	addDevice(t, st, ctx, "dev_22")
	in := model.Target{Kind: "webpush", DeviceID: "dev_1", Name: "Phone", Endpoint: "https://fcm.googleapis.com/x",
		Keys: &model.Keys{P256dh: "pk", Auth: "au"}}
	a, err := st.UpsertTarget(ctx, in)
	if err != nil || a.ID == "" || !a.Enabled || a.Keys == nil || a.Keys.Auth != "au" || a.DeviceID != "dev_1" || a.CreatedAt == 0 {
		t.Fatalf("%+v %v", a, err)
	}
	// A 410 disables it...
	if err := st.ApplyTargetResult(ctx, a.ID, model.Result{Outcome: model.OutcomeGone, HTTPStatus: 410, Err: "410 gone"}); err != nil {
		t.Fatal(err)
	}
	g, _ := st.TargetByID(ctx, a.ID)
	if g.GoneAt == 0 || g.Enabled || g.LastError != "410 gone" {
		t.Fatalf("%+v", g)
	}
	// ...enabling does not revive it, a new subscription does (same id, new device and keys).
	en := true
	g, _ = st.PatchTarget(ctx, a.ID, store.TargetPatch{Enabled: &en})
	if g.GoneAt == 0 {
		t.Fatal("patch cleared gone_at")
	}
	in.DeviceID, in.Keys = "dev_22", &model.Keys{P256dh: "pk2", Auth: "au2"}
	b, err := st.UpsertTarget(ctx, in)
	if err != nil || b.ID != a.ID || b.GoneAt != 0 || !b.Enabled || b.DeviceID != "dev_22" || b.Keys.P256dh != "pk2" || b.LastError != "" || b.FailCount != 0 {
		t.Fatalf("%+v %v", b, err)
	}
	// Failures count; success clears.
	_ = st.ApplyTargetResult(ctx, a.ID, model.Result{Outcome: model.OutcomeRetry, Err: "503"})
	_ = st.ApplyTargetResult(ctx, a.ID, model.Result{Outcome: model.OutcomeFailed, Err: "403 x"})
	f, _ := st.TargetByID(ctx, a.ID)
	if f.FailCount != 2 || f.LastError != "403 x" || f.LastErrorAt == 0 {
		t.Fatalf("%+v", f)
	}
	_ = st.ApplyTargetResult(ctx, a.ID, model.Result{Outcome: model.OutcomeSent})
	f, _ = st.TargetByID(ctx, a.ID)
	if f.FailCount != 0 || f.LastError != "" || f.LastOKAt == 0 {
		t.Fatalf("%+v", f)
	}
	if _, err := st.UpsertTarget(ctx, model.Target{Kind: "apns", Endpoint: "x"}); !errors.Is(err, store.ErrTargetKind) {
		t.Fatalf("%v", err)
	}
}

func TestTargetListPatchDeleteAndSecrets(t *testing.T) {
	st, ctx := openTargets(t)
	addDevice(t, st, ctx, "dev_1")
	n1, _ := st.UpsertTarget(ctx, model.Target{Kind: "ntfy", Name: "hub", Endpoint: "https://ntfy.sh/wy-a", Secret: "tk_1", Prefs: &model.Prefs{Mute: []model.Trigger{"reset"}}})
	n2, _ := st.UpsertTarget(ctx, model.Target{Kind: "ntfy", DeviceID: "dev_1", Endpoint: "https://ntfy.sh/wy-b"})
	if n1.Secret != "tk_1" || n1.DeviceID != "" || n1.Prefs == nil || n1.Prefs.Mute[0] != "reset" {
		t.Fatalf("%+v", n1)
	}
	all, _ := st.ListTargets(ctx, "")
	mine, _ := st.ListTargets(ctx, "dev_1")
	if len(all) != 2 || len(mine) != 1 || mine[0].ID != n2.ID {
		t.Fatalf("%d %d", len(all), len(mine))
	}
	if got, err := st.TargetByEndpoint(ctx, "ntfy", "https://ntfy.sh/wy-a"); err != nil || got.ID != n1.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := st.TargetByEndpoint(ctx, "webpush", "https://ntfy.sh/wy-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	name, off := "New", false
	p, err := st.PatchTarget(ctx, n1.ID, store.TargetPatch{Name: &name, Enabled: &off, Prefs: &model.Prefs{Tap: "claude"}})
	if err != nil || p.Name != "New" || p.Enabled || p.Prefs.Tap != "claude" || p.Secret != "tk_1" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := st.PatchTarget(ctx, "pt_none", store.TargetPatch{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if err := st.DeleteTarget(ctx, n1.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTarget(ctx, n1.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestHubSecretAndSettings(t *testing.T) {
	st, ctx := openTargets(t)
	if _, err := st.HubSecret(ctx, "k"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([][]byte, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := st.EnsureHubSecret(ctx, "k", func() ([]byte, error) { return []byte{byte(i + 1), 2, 3}, nil })
			if err != nil {
				t.Error(err)
			}
			results[i] = b
		}()
	}
	wg.Wait()
	for _, r := range results {
		if string(r) != string(results[0]) {
			t.Fatalf("racing callers disagree: %v vs %v", r, results[0])
		}
	}
	if got, _ := st.HubSecret(ctx, "k"); string(got) != string(results[0]) {
		t.Fatal("stored value differs")
	}
	if err := st.PutPushSetting(ctx, "x.y", []byte(`"v"`)); err != nil {
		t.Fatal(err)
	}
	_ = st.PutPushSetting(ctx, "x.y", []byte(`"w"`))
	if v, err := st.PushSetting(ctx, "x.y"); err != nil || string(v) != `"w"` {
		t.Fatalf("%s %v", v, err)
	}
	if err := st.PutPushSetting(ctx, "bad", []byte(`{`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := st.PushSetting(ctx, "absent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

// The engine's write-back (AlertTargetResult) must not repeat a status the sender already put
// in the text: "410 410" and "401 HTTP 401" were seen on a live hub.
func TestAlertTargetResultErrorText(t *testing.T) {
	st, ctx := openTargets(t)
	addDevice(t, st, ctx, "dev_1")
	a, err := st.UpsertTarget(ctx, model.Target{Kind: "webpush", DeviceID: "dev_1", Name: "P", Endpoint: "https://fcm.googleapis.com/y",
		Keys: &model.Keys{P256dh: "pk", Auth: "au"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		r    model.Result
		want string
	}{
		{model.Result{Outcome: model.OutcomeFailed, HTTPStatus: 413, Err: "413"}, "413"},
		{model.Result{Outcome: model.OutcomeFailed, HTTPStatus: 413, Err: "413 payload too large"}, "413 payload too large"},
		{model.Result{Outcome: model.OutcomeFailed, HTTPStatus: 401, Err: "HTTP 401"}, "HTTP 401"},
		{model.Result{Outcome: model.OutcomeRetry, HTTPStatus: 500, Err: "upstream down"}, "500 upstream down"},
		{model.Result{Outcome: model.OutcomeRetry, Err: "timeout"}, "timeout"},
		{model.Result{Outcome: model.OutcomeGone, HTTPStatus: 410, Err: "410"}, "410"},
	} {
		if err := st.Write(ctx, func(tx *store.Tx) error { return tx.AlertTargetResult(a.ID, c.r, 1) }); err != nil {
			t.Fatal(err)
		}
		g, _ := st.TargetByID(ctx, a.ID)
		if g.LastError != c.want {
			t.Errorf("%+v: last_error %q, want %q", c.r, g.LastError, c.want)
		}
	}
}
