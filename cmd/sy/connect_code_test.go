package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/hub/cli"
)

// claimHub fakes the hub side of `sy connect --code`.
func claimHub(t *testing.T, claimStatus int, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"ok":true,"version":"0.4.0","schema":3}`)
		case "/v1/pair/claim":
			_ = json.NewDecoder(r.Body).Decode(got)
			if claimStatus != 200 {
				w.WriteHeader(claimStatus)
				fmt.Fprint(w, `{"error":"invalid or expired code","code":"unauthorized"}`)
				return
			}
			fmt.Fprintf(w, `{"device":"dev_1","kind":"machine","name":"x","scopes":["write"],"token":%q,"hub":{"url":"x","version":"0.4.0"}}`, goodToken)
		case "/v1/ingest":
			if r.Header.Get("Authorization") != "Bearer "+goodToken {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"accepted":[],"rejected":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConnectWithCodeClaimsAsMachine(t *testing.T) {
	deps, _, _ := sandbox(t)
	var got map[string]any
	srv := claimHub(t, 200, &got)
	var out, errb bytes.Buffer
	if err := connect(context.Background(), deps, []string{srv.URL, "--code", "482-913", "--no-timer"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	if got["code"] != "482-913" || got["expect_kind"] != "machine" || !strings.HasPrefix(fmt.Sprint(got["machine"]), "m_") || got["name"] == "" {
		t.Errorf("claim body %v", got)
	}
	tok, err := client.ReadToken(deps.dirs.State.Path)
	if err != nil || tok != goodToken {
		t.Errorf("token file %q %v", tok, err)
	}
	if strings.Contains(out.String()+errb.String(), goodToken) {
		t.Error("the token was printed")
	}
}

func TestConnectWithBadCodeWritesNothing(t *testing.T) {
	deps, r, _ := sandbox(t)
	var got map[string]any
	srv := claimHub(t, 401, &got)
	var out, errb bytes.Buffer
	err := connect(context.Background(), deps, []string{srv.URL, "--code", "482-913"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("err %v", err)
	}
	if _, serr := os.Stat(filepath.Join(deps.dirs.State.Path, client.TokenFile)); !errors.Is(serr, os.ErrNotExist) {
		t.Error("a token file exists after a failed claim")
	}
	if len(r.calls) != 0 {
		t.Errorf("timer touched: %v", r.calls)
	}
	// --code excludes the other ways to give a token.
	if err := connect(context.Background(), deps, []string{srv.URL, "--code", "1", "--token", goodToken}, &out, &errb); !errors.Is(err, errUsage) {
		t.Errorf("--code with --token: %v", err)
	}
}

func TestPhoneCommandUsageAndExitCodes(t *testing.T) {
	var out, errb bytes.Buffer
	if err := runPhone(nil, &out, &errb); !errors.Is(err, errUsage) {
		t.Errorf("no subcommand: %v", err)
	}
	if err := runPhone([]string{"pair", "--bogus"}, &out, &errb); !errors.Is(err, errUsage) {
		t.Errorf("bad flag: %v", err)
	}
	var ee *exitError
	if err := hubPairResult(cli.ErrExpired); !errors.As(err, &ee) || ee.code != 3 {
		t.Errorf("expired: %v", err)
	}
}
