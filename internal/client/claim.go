package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/mraj602/switchyard/internal/version"
)

// ClaimRequest is the body of POST /v1/pair/claim (pairing.md 3). Send exactly one of Code
// and Secret.
type ClaimRequest struct {
	Code       string `json:"code,omitempty"`
	Secret     string `json:"secret,omitempty"`
	Name       string `json:"name,omitempty"`
	Machine    string `json:"machine,omitempty"`
	ExpectKind string `json:"expect_kind,omitempty"`
	Cookie     bool   `json:"cookie,omitempty"`
	Client     string `json:"client,omitempty"`
}

// ClaimReply is the 200 body of POST /v1/pair/claim. Token is set unless Cookie was asked.
type ClaimReply struct {
	Device  string   `json:"device"`
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Scopes  []string `json:"scopes"`
	Token   string   `json:"token"`
	Machine string   `json:"machine"`
	Hub     struct {
		URL     string `json:"url"`
		Version string `json:"version"`
	} `json:"hub"`
}

// Claim swaps a pairing code for a device token at the hub baseURL. No token is needed:
// the code is the credential. A refusal comes back as *StatusError (401 invalid or expired
// code, 409 wrong kind, 429 too many tries, 400 bad request). The reply's token is never
// logged here; the caller stores it.
func Claim(ctx context.Context, baseURL string, hc *http.Client, req ClaimRequest) (*ClaimReply, error) {
	if req.Client == "" {
		req.Client = "sy/" + version.Version
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c := &Client{BaseURL: baseURL, HTTP: hc}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/v1/pair/claim"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp, b)
	}
	var out ClaimReply
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("not a Switchyard hub (bad claim reply): %w", err)
	}
	if out.Token == "" && !req.Cookie {
		return nil, errors.New("the hub answered without a token")
	}
	return &out, nil
}
