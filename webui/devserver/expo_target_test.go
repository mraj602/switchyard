package devserver

import "testing"

func TestExpoTargetRegistration(t *testing.T) {
	schemas := loadSchemas(t)
	_, ts := newServer(t)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)
	body := M{"kind": "expo", "expo_token": "ExponentPushToken[devserverAAAAAAAA]", "name": "Pixel · app",
		"platform": "android", "app": M{"version": "0.8.0", "build": "3"}}
	tg := phone.want(201, "POST", "/v1/push/targets", body)
	check(t, schemas, "PushTarget", "expo target", tg)
	if tg["endpoint_host"] != "exp.host" || tg["platform"] != "android" || tg["app_version"] != "0.8.0" {
		t.Errorf("target = %v", tg)
	}
	again := phone.want(201, "POST", "/v1/push/targets", body)
	if again["id"] != tg["id"] {
		t.Errorf("not upserted by token: %v then %v", tg["id"], again["id"])
	}
	body["expo_token"] = "ExponentPushToken[short]"
	phone.want(400, "POST", "/v1/push/targets", body)
	body["expo_token"], body["platform"] = "ExponentPushToken[devserverAAAAAAAA]", "windows"
	phone.want(400, "POST", "/v1/push/targets", body)
}
