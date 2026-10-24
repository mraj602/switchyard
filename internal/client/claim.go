package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/version"
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
	// Keys are this device's public keys in the clear (typed codes only, keys.md 7.1).
	Keys *keys.DeviceKeysNew `json:"keys,omitempty"`
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
	// keys.md 7.3: the hub key, the active owner key document (nil until one
	// is active), this device's certificate when keys were sent, and the link settings.
	HubKey   *PublicKey `json:"hub_key"`
	OwnerKey *keys.Doc  `json:"owner_key"`
	Cert     *keys.Doc  `json:"cert"`
	Reg      string     `json:"reg"`
	Link     *LinkInfo  `json:"link"`
}

// PublicKey is api.yaml PublicKey.
type PublicKey struct {
	Alg string `json:"alg"`
	Pub string `json:"pub"`
	FP  string `json:"fp"`
}

// LinkInfo is the claim reply's link object (push.md 13.6).
type LinkInfo struct {
	URLs  []string `json:"urls"`
	PingS int      `json:"ping_s"`
}

// Claim swaps a pairing code for a device token at the hub baseURL. No token is needed:
// the code is the credential. A refusal comes back as *StatusError (401 invalid or expired
// code, 409 wrong kind, 429 too many tries, 400 bad request). The reply's token is never
// logged here; the caller stores it.
func Claim(ctx context.Context, baseURL string, hc *http.Client, req ClaimRequest) (*ClaimReply, error) {
	if req.Client == "" {
		req.Client = "wy/" + version.Version
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
		return nil, fmt.Errorf("not a Watchyard hub (bad claim reply): %w", err)
	}
	if out.Token == "" && !req.Cookie {
		return nil, errors.New("the hub answered without a token")
	}
	return &out, nil
}
