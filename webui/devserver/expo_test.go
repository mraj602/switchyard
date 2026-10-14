package devserver

import "testing"

func TestExpoChannelSettingsAndRefresh(t *testing.T) {
	schemas := loadSchemas(t)
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)

	ch := owner.want(200, "PUT", "/v1/push/channels/expo", M{"enabled": true, "settings": M{"access_token": "secret-tok", "access_token_set": false}})
	check(t, schemas, "Channel", "expo", ch)
	set := ch["settings"].(map[string]any)
	if set["access_token_set"] != true || set["access_token"] != nil || set["base_url"] != "https://exp.host" {
		t.Errorf("settings = %v", set)
	}
	owner.want(400, "PUT", "/v1/push/channels/expo", M{"enabled": true, "settings": M{"nope": 1}})
	ch = owner.want(200, "PUT", "/v1/push/channels/expo", M{"enabled": true, "settings": M{"access_token": ""}})
	if ch["settings"].(map[string]any)["access_token_set"] != false {
		t.Errorf("token not cleared: %v", ch)
	}

	rules := owner.want(200, "GET", "/v1/rules", nil)
	s := rules["settings"].(map[string]any)
	if s["expo_refresh_min"] != float64(15) {
		t.Errorf("settings = %v", s)
	}
	s["expo_refresh_min"] = 5
	owner.want(400, "PUT", "/v1/push/settings", s)
	s["expo_refresh_min"] = 30
	owner.want(200, "PUT", "/v1/push/settings", s)

	var found bool
	each(t, owner.want(200, "GET", "/v1/push/targets", nil)["targets"], func(x any) {
		check(t, schemas, "PushTarget", "target", x)
		if m := x.(map[string]any); m["kind"] == "expo" && m["platform"] == "ios" && m["app_version"] == "0.8.0" {
			found = true
		}
	})
	if !found {
		t.Error("no expo target")
	}
}
