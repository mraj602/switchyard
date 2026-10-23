package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedProxy(t *testing.T) {
	t.Cleanup(func() { SetTrustedProxies(nil) })
	req := func(remote string) *httptestReq {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("X-Forwarded-Proto", "https")
		return &httptestReq{r}
	}
	SetTrustedProxies(nil)
	if got := ClientIP(req("172.17.0.1:5555").r); got != "172.17.0.1" {
		t.Fatalf("untrusted bridge: %s", got)
	}
	if SecureRequest(req("172.17.0.1:5555").r) {
		t.Fatal("bridge trusted without option")
	}
	n, err := ParseTrustedProxies("172.16.0.0/12, 10.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	SetTrustedProxies(n)
	if got := ClientIP(req("172.17.0.1:5555").r); got != "203.0.113.9" {
		t.Fatalf("trusted bridge: %s", got)
	}
	if !SecureRequest(req("172.17.0.1:5555").r) {
		t.Fatal("trusted bridge should be secure")
	}
	if got := ClientIP(req("10.1.1.1:1").r); got != "203.0.113.9" {
		t.Fatalf("single addr: %s", got)
	}
	if got := ClientIP(req("192.168.1.5:1").r); got != "192.168.1.5" {
		t.Fatalf("outside range: %s", got)
	}
	if _, err := ParseTrustedProxies("nope"); err == nil {
		t.Fatal("bad value accepted")
	}
}

type httptestReq struct{ r *http.Request }
