package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// Analytics is GET /v1/analytics/{report} (analytics.md): the hub's reply, undecoded. It
// needs a token with the read scope; a machine token (write only) is answered 403.
func (c *Client) Analytics(ctx context.Context, report string, q url.Values) (json.RawMessage, error) {
	p := "/v1/analytics/" + url.PathEscape(report)
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	var out json.RawMessage
	return out, c.doJSON(ctx, http.MethodGet, p, nil, &out)
}
