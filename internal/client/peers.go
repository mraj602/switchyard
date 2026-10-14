package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mraj602/switchyard/internal/keys"
)

// The peers calls of api.yaml (tag peers): the matrix, the kill switch and the audit log.

// MatrixInfo is api.yaml MatrixInfo: the document as signed and its decoded body.
type MatrixInfo struct {
	Doc       keys.Doc        `json:"doc"`
	Matrix    keys.MatrixBody `json:"matrix"`
	Signer    string          `json:"signer"`
	Widens    bool            `json:"widens"`
	MadeBy    string          `json:"made_by"`
	CreatedAt string          `json:"created_at"`
}

// MatrixEdit is api.yaml MatrixEdit (narrowing edits the hub signs).
type MatrixEdit struct {
	BaseVersion *int       `json:"base_version,omitempty"`
	Allow       [][]string `json:"allow,omitempty"`
	Deny        [][]string `json:"deny,omitempty"`
	Paused      *bool      `json:"paused,omitempty"`
	SameAccount *bool      `json:"same_account,omitempty"`
	MaxTurns    *int       `json:"max_turns,omitempty"`
}

// PeerAuditEntry is api.yaml PeerAuditEntry.
type PeerAuditEntry struct {
	ID     int64          `json:"id"`
	At     string         `json:"at"`
	Actor  string         `json:"actor"`
	Event  string         `json:"event"`
	Thread *string        `json:"thread"`
	Msg    *string        `json:"msg"`
	Detail map[string]any `json:"detail"`
}

// Matrix is GET /v1/peers/matrix (the hub makes version 1 when it has none).
func (c *Client) Matrix(ctx context.Context) (MatrixInfo, error) {
	var m MatrixInfo
	return m, c.doJSON(ctx, http.MethodGet, "/v1/peers/matrix", nil, &m)
}

// PutMatrixDoc is PUT /v1/peers/matrix with an owner-signed document.
func (c *Client) PutMatrixDoc(ctx context.Context, d keys.Doc) (MatrixInfo, error) {
	var m MatrixInfo
	return m, c.doJSON(ctx, http.MethodPut, "/v1/peers/matrix", map[string]any{"doc": d}, &m)
}

// PutMatrixEdit is PUT /v1/peers/matrix with an edit the hub signs.
func (c *Client) PutMatrixEdit(ctx context.Context, e MatrixEdit) (MatrixInfo, error) {
	var m MatrixInfo
	return m, c.doJSON(ctx, http.MethodPut, "/v1/peers/matrix", map[string]any{"edit": e}, &m)
}

// PausePeers is POST /v1/peers/pause.
func (c *Client) PausePeers(ctx context.Context, paused bool) (MatrixInfo, error) {
	var m MatrixInfo
	return m, c.doJSON(ctx, http.MethodPost, "/v1/peers/pause", map[string]bool{"paused": paused}, &m)
}

// PeerAudit is GET /v1/peers/audit, newest first.
func (c *Client) PeerAudit(ctx context.Context, since time.Time, limit int) ([]PeerAuditEntry, error) {
	q := url.Values{}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	p := "/v1/peers/audit"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	var r struct {
		Entries []PeerAuditEntry `json:"entries"`
	}
	return r.Entries, c.doJSON(ctx, http.MethodGet, p, nil, &r)
}
